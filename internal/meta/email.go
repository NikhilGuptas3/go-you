package meta

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// EmailMetaService assembles email_meta's domain-intelligence V2 block, ported
// from email_intelligence/domain_meta_v2.py + domain_base.py. Lanes: WHOIS
// (whoisxmlapi), is_disposable (mailboxvalidator), SPF/DMARC (DNS TXT), valid_mx
// (DNS MX), website_exists (GET https://domain). IPQS and the OpenAI name lane
// are omitted (prod-disabled / dead). No DynamoDB cache — always live.
type EmailMetaService struct {
	cfg      ConfigGetter
	proxyURL *url.URL
	timeout  time.Duration
}

func NewEmailMetaService(cfg ConfigGetter, proxyURL *url.URL, timeout time.Duration) *EmailMetaService {
	return &EmailMetaService{cfg: cfg, proxyURL: proxyURL, timeout: timeout}
}

// EmailMetaResult is the internal domain-intelligence output. The handler maps
// it into model.EmailMeta (is_disposable + domain_attributes). On a hard error
// the attributes collapse to {domain, error:true}.
type EmailMetaResult struct {
	IsDisposable     *bool
	DomainAttributes map[string]any
}

// Fetch runs the six lanes concurrently and assembles domain_attributes. domain
// is the part after '@'.
func (s *EmailMetaService) Fetch(ctx context.Context, email string) *EmailMetaResult {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return &EmailMetaResult{DomainAttributes: map[string]any{"error": true}}
	}
	domain := email[at+1:]

	var (
		wg         sync.WaitGroup
		disposable *bool
		whois      whoisInfo
		spf, dmarc string
		validMX    string
		websiteOK  bool
	)

	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	run(func() { disposable = s.isDisposable(ctx, domain) })
	run(func() { whois = s.whois(ctx, domain) })
	run(func() { spf = ynFromTXT(domain, "v=spf1") })
	run(func() { dmarc = ynFromTXT("_dmarc."+domain, "v=DMARC1") })
	run(func() { validMX = s.validMX(domain) })
	run(func() { websiteOK = s.websiteExists(ctx, domain) })
	wg.Wait()

	tld := ""
	if i := strings.LastIndex(domain, "."); i >= 0 {
		tld = domain[i:] // includes the leading dot, matching Python "." + split[-1]
	}
	registered := "No"
	if whois.registered {
		registered = "Yes"
	}
	attrs := map[string]any{
		"domain":         domain,
		"created_at":     nilIfEmpty(whois.createdAt),
		"updated_at":     nilIfEmpty(whois.updatedAt),
		"expires_at":     nilIfEmpty(whois.expiresAt),
		"tld":            tld,
		"registered":     registered,
		"registrar_name": nilIfEmpty(whois.registrarName),
		"registered_to":  nilIfEmpty(whois.registrarOrg),
		"dmarc_enforced": dmarc,
		"spf_strict":     spf,
		"valid _mx":      validMX, // literal space in key, matches prod verbatim
		"website_exists": websiteOK,
	}
	// Python only computes spf/dmarc/valid_mx when whois_info is present; when
	// not registered they default to "No". Mirror that.
	if !whois.registered {
		attrs["dmarc_enforced"] = "No"
		attrs["spf_strict"] = "No"
		attrs["valid _mx"] = "No"
	}
	return &EmailMetaResult{IsDisposable: disposable, DomainAttributes: attrs}
}

// --- is_disposable: mailboxvalidator ---

func (s *EmailMetaService) isDisposable(ctx context.Context, domain string) *bool {
	// Blacklisted (trusted) domains skip the check and are non-disposable.
	if !s.domainCheckRequired(domain) {
		f := false
		return &f
	}
	apiKey := s.tpiString("mail_box_validator", "api_key")
	u := "https://api.mailboxvalidator.com/v2/email/disposable?email=" +
		url.QueryEscape("test-sign3@"+domain) + "&key=" + url.QueryEscape(apiKey) + "&format=json"
	status, body, err := doHTTP(ctx, s.proxyURL, s.timeout, false, "GET", u, nil, map[string]string{})
	if err != nil || status != 200 {
		return nil
	}
	var parsed struct {
		IsDisposable any `json:"is_disposable"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	// The API returns "true"/"false" strings or bools depending on version.
	switch t := parsed.IsDisposable.(type) {
	case bool:
		return &t
	case string:
		b := strings.EqualFold(t, "true")
		return &b
	default:
		return nil
	}
}

func (s *EmailMetaService) domainCheckRequired(domain string) bool {
	if s.cfg == nil {
		return true
	}
	bl, ok := s.cfg.Get("email_validator_blacklist", nil).([]any)
	if !ok {
		return true
	}
	for _, e := range bl {
		if str, ok := e.(string); ok && strings.Contains(domain, str) {
			return false
		}
	}
	return true
}

// --- WHOIS: whoisxmlapi ---

type whoisInfo struct {
	registered                      bool
	createdAt, updatedAt, expiresAt string
	registrarName, registrarOrg     string
}

func (s *EmailMetaService) whois(ctx context.Context, domain string) whoisInfo {
	apiKey := s.tpiString("whois", "api_key")
	if apiKey == "" {
		return whoisInfo{}
	}
	u := "https://www.whoisxmlapi.com/whoisserver/WhoisService?apiKey=" + url.QueryEscape(apiKey) +
		"&domainName=" + url.QueryEscape(domain) + "&outputFormat=JSON"
	status, body, err := doHTTP(ctx, nil, s.timeout, false, "GET", u, nil, map[string]string{})
	if err != nil || status != 200 {
		return whoisInfo{}
	}
	var parsed struct {
		WhoisRecord struct {
			DataError     string `json:"dataError"`
			CreatedDate   string `json:"createdDate"`
			UpdatedDate   string `json:"updatedDate"`
			ExpiresDate   string `json:"expiresDate"`
			RegistrarName string `json:"registrarName"`
			Registrant    struct {
				Organization  string `json:"organization"`
				RegistrarName string `json:"registrarName"`
			} `json:"registrant"`
			RegistryData struct {
				CreatedDate   string `json:"createdDate"`
				UpdatedDate   string `json:"updatedDate"`
				ExpiresDate   string `json:"expiresDate"`
				RegistrarName string `json:"registrarName"`
				Registrant    struct {
					Organization  string `json:"organization"`
					RegistrarName string `json:"registrarName"`
				} `json:"registrant"`
			} `json:"registryData"`
		} `json:"WhoisRecord"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return whoisInfo{}
	}
	w := parsed.WhoisRecord
	if w.DataError == "MISSING_WHOIS_DATA" {
		return whoisInfo{registered: false}
	}
	info := whoisInfo{
		registered:    true,
		createdAt:     dateOnly(w.CreatedDate),
		updatedAt:     dateOnly(w.UpdatedDate),
		expiresAt:     dateOnly(w.ExpiresDate),
		registrarName: w.Registrant.RegistrarName,
		registrarOrg:  w.Registrant.Organization,
	}
	// registryData overrides top-level when present (matches Python precedence).
	rd := w.RegistryData
	if rd.UpdatedDate != "" {
		info.updatedAt = dateOnly(rd.UpdatedDate)
	}
	if rd.CreatedDate != "" {
		info.createdAt = dateOnly(rd.CreatedDate)
	}
	if rd.ExpiresDate != "" {
		info.expiresAt = dateOnly(rd.ExpiresDate)
	}
	if rd.RegistrarName != "" {
		info.registrarName = rd.RegistrarName
	}
	if rd.Registrant.Organization != "" {
		info.registrarOrg = rd.Registrant.Organization
	}
	if rd.Registrant.RegistrarName != "" {
		info.registrarName = rd.Registrant.RegistrarName
	}
	return info
}

// --- DNS lanes ---

// ynFromTXT returns "Yes" if any TXT record for name begins with the marker
// prefix (e.g. "v=spf1" for SPF, "v=DMARC1" for DMARC), else "No".
func ynFromTXT(name, marker string) string {
	records, err := net.LookupTXT(name)
	if err != nil {
		return "No"
	}
	for _, r := range records {
		if strings.HasPrefix(strings.TrimSpace(r), marker) {
			return "Yes"
		}
	}
	return "No"
}

func (s *EmailMetaService) validMX(domain string) string {
	mx, err := net.LookupMX(domain)
	if err != nil || len(mx) == 0 {
		return "No"
	}
	return "Yes"
}

func (s *EmailMetaService) websiteExists(ctx context.Context, domain string) bool {
	status, _, err := doHTTP(ctx, s.proxyURL, s.timeout, false, "GET", "https://"+domain, nil, map[string]string{})
	return err == nil && status == 200
}

// --- helpers ---

func (s *EmailMetaService) tpiString(section, key string) string {
	if s.cfg == nil {
		return ""
	}
	tpi, ok := s.cfg.Get("tpi_global_config", nil).(map[string]any)
	if !ok {
		return ""
	}
	sec, ok := tpi[section].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := sec[key].(string)
	return v
}

func dateOnly(s string) string {
	if i := strings.Index(s, "T"); i >= 0 {
		return s[:i]
	}
	return s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
