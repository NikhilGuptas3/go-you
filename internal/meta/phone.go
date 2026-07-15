// Package meta produces phone_meta and email_meta, ported from the Python
// PhoneNumberInfoService (service/phone_number_info_service.py) and
// EmailInfoService / DomainIntelligencev2. IPQS is prod-disabled and omitted;
// there is no DynamoDB cache (always live) and no MNRL serverless lane (the
// revocations come only from the Outris TPI HTTP lane).
package meta

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

// ConfigGetter is the subset of appconfig.Fetcher the meta lanes need (the
// freecharge operator mapping + tpi_global_config). Declared as an interface to
// avoid an import cycle and to allow a stub in tests.
type ConfigGetter interface {
	Get(key string, def any) any
}

// PhoneMetaService assembles phone_meta: operator/circle (Freecharge),
// postpaid (Airtel/Jio/VI any-True-wins), and revocations (Outris TPI). All
// lanes run concurrently under the caller's context; the leaf-only timeout
// model applies (no internal deadline beyond ctx).
type PhoneMetaService struct {
	cfg      ConfigGetter
	proxyURL *url.URL
	timeout  time.Duration
}

func NewPhoneMetaService(cfg ConfigGetter, proxyURL *url.URL, timeout time.Duration) *PhoneMetaService {
	return &PhoneMetaService{cfg: cfg, proxyURL: proxyURL, timeout: timeout}
}

// PhoneMetaResult is the assembled phone_meta (internal form). The handler maps
// it into model.PhoneMeta. Postpaid nil means "no usable verdict".
type PhoneMetaResult struct {
	Operator    string
	Circle      string
	Postpaid    *bool
	Revocations map[string]any // {total_revocations, year, month} or empty
}

// Fetch runs the operator, postpaid (if enabled), and revocation lanes and
// merges them. national is the 10-digit number; intl is "+91...". postpaidOn
// gates the postpaid lane (tenant is_postpaid_enabled).
func (s *PhoneMetaService) Fetch(ctx context.Context, national, intl string, postpaidOn bool) *PhoneMetaResult {
	res := &PhoneMetaResult{Revocations: map[string]any{}}

	var wg sync.WaitGroup
	var mu sync.Mutex

	// operator / circle (Freecharge)
	wg.Add(1)
	go func() {
		defer wg.Done()
		op, circle := s.freecharge(ctx, national)
		mu.Lock()
		res.Operator, res.Circle = op, circle
		mu.Unlock()
	}()

	// postpaid (Airtel/Jio/VI any-True-wins)
	if postpaidOn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pp := s.postpaid(ctx, national)
			mu.Lock()
			res.Postpaid = pp
			mu.Unlock()
		}()
	}

	// revocations (Outris TPI lane only)
	wg.Add(1)
	go func() {
		defer wg.Done()
		rev := s.revocations(ctx, national)
		mu.Lock()
		res.Revocations = rev
		mu.Unlock()
	}()

	wg.Wait()
	return res
}

// --- operator / circle: OperatorFreecharge ---

func (s *PhoneMetaService) freecharge(ctx context.Context, national string) (operator, circle string) {
	body, _ := json.Marshal(map[string]any{"serviceNumber": national, "productCode": "MR"})
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-GB,en;q=0.9",
		"content-type": "application/json", "csrfrequestidentifier": "",
		"origin": "https://www.freecharge.in", "priority": "u=1, i", "referer": "https://www.freecharge.in/",
		"sec-ch-ua":        `"Chromium";v="142", "Google Chrome";v="142", "Not_A Brand";v="99"`,
		"sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": `"macOS"`, "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
	}
	// Freecharge uses curl_cffi in Python -> Chrome TLS impersonation.
	status, respBody, err := s.doTLS(ctx, "POST", "https://www.freecharge.in/api/fulfilment/nosession/fetch/operatorMapping", strings.NewReader(string(body)), headers)
	if err != nil || status != 200 {
		return "", ""
	}
	var parsed struct {
		Data struct {
			OperatorID any `json:"operatorId"`
			CircleID   any `json:"circleId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", ""
	}
	// Resolve via the freecharge_operator_mapping config (operator_mapping[id].operator,
	// circles[id]). postpaid from the mapping is intentionally ignored (the
	// dedicated lane owns it, matching parse_digitalapiproxy_data).
	mapping := s.freechargeMapping()
	opID := anyToStr(parsed.Data.OperatorID)
	circleID := anyToStr(parsed.Data.CircleID)
	if om, ok := mapping.operatorMapping[opID]; ok {
		operator = om
	}
	if c, ok := mapping.circles[circleID]; ok {
		circle = c
	} else if circleID != "" {
		circle = "Unknown"
	}
	return operator, circle
}

type freechargeMap struct {
	operatorMapping map[string]string // operatorId -> operator name
	circles         map[string]string // circleId -> circle name
}

func (s *PhoneMetaService) freechargeMapping() freechargeMap {
	out := freechargeMap{operatorMapping: map[string]string{}, circles: map[string]string{}}
	if s.cfg == nil {
		return out
	}
	raw, ok := s.cfg.Get("freecharge_operator_mapping", nil).(map[string]any)
	if !ok {
		return out
	}
	if om, ok := raw["operator_mapping"].(map[string]any); ok {
		for id, v := range om {
			if entry, ok := v.(map[string]any); ok {
				if name, ok := entry["operator"].(string); ok {
					out.operatorMapping[id] = name
				}
			}
		}
	}
	if circles, ok := raw["circles"].(map[string]any); ok {
		for id, v := range circles {
			if name, ok := v.(string); ok {
				out.circles[id] = name
			}
		}
	}
	return out
}

// --- postpaid: Airtel / Jio / VI, any-True-wins ---

func (s *PhoneMetaService) postpaid(ctx context.Context, national string) *bool {
	var wg sync.WaitGroup
	results := make([]*bool, 3)
	fns := []func(context.Context, string) *bool{s.postpaidAirtel, s.postpaidJio, s.postpaidVI}
	for i, fn := range fns {
		wg.Add(1)
		go func(i int, fn func(context.Context, string) *bool) {
			defer wg.Done()
			results[i] = fn(ctx, national)
		}(i, fn)
	}
	wg.Wait()
	anyFalse := false
	for _, r := range results {
		if r == nil {
			continue
		}
		if *r {
			t := true
			return &t // any-True wins immediately
		}
		anyFalse = true
	}
	if anyFalse {
		f := false
		return &f
	}
	return nil
}

func (s *PhoneMetaService) postpaidAirtel(ctx context.Context, national string) *bool {
	headers := map[string]string{
		"Accept": "application/json, text/plain, */*", "Accept-Language": "en-GB,en-US;q=0.9,en;q=0.8",
		"Connection": "keep-alive", "Origin": "https://www.airtel.in", "Referer": "https://www.airtel.in/",
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-site",
		"User-Agent":   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		"googleCookie": "airtel.com", "requesterId": "WEB",
		"sec-ch-ua":        `"Not)A;Brand";v="8", "Chromium";v="138", "Google Chrome";v="138"`,
		"sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": `"macOS"`,
	}
	u := "https://digi-api.airtel.in/airtel-billing/rest/billing/v1/bill/easy/details/fetch?lob=POSTPAID&siNumber=" +
		url.QueryEscape(national) + "&skipDueAmount=true"
	status, _, err := s.doProxy(ctx, "GET", u, nil, headers)
	if err != nil {
		return nil
	}
	switch status {
	case 200:
		t := true
		return &t
	case 400:
		f := false
		return &f
	default:
		return nil
	}
}

func (s *PhoneMetaService) postpaidJio(ctx context.Context, national string) *bool {
	headers := map[string]string{
		"Accept": "*/*", "Accept-Language": "en-GB,en-US;q=0.9,en;q=0.8", "Connection": "keep-alive",
		"Referer": "https://www.jio.com/selfcare/paybill/mobility/", "Sec-Fetch-Dest": "empty",
		"Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
		"User-Agent":       "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Mobile Safari/537.36",
		"sec-ch-ua":        `"Not;A=Brand";v="99", "Google Chrome";v="139", "Chromium";v="139"`,
		"sec-ch-ua-mobile": "?1", "sec-ch-ua-platform": `"Android"`,
	}
	u := "https://www.jio.com/api/jio-paybill-service/paybill/submitDetail/" + url.PathEscape(national) + "/" +
		rand3digits() + "?source=undefined&rechargeType=fetchBill&serviceType=mobility"
	status, _, err := s.doProxy(ctx, "GET", u, nil, headers)
	if err != nil {
		return nil
	}
	switch status {
	case 200:
		t := true
		return &t
	case 400:
		f := false
		return &f
	default:
		return nil
	}
}

func (s *PhoneMetaService) postpaidVI(ctx context.Context, national string) *bool {
	data, err := viPrepareData(national)
	if err != nil {
		return nil
	}
	headers := map[string]string{
		"Accept": "*/*", "Accept-Language": "en-GB,en;q=0.9", "Connection": "keep-alive",
		"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8", "Origin": "https://www.myvi.in",
		"Referer": "https://www.myvi.in/prepaid/online-mobile-recharge", "Sec-Fetch-Dest": "empty",
		"Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin", "X-Requested-With": "XMLHttpRequest",
	}
	status, respBody, err := s.doProxy(ctx, "POST", "https://www.myvi.in/bin/selected/prepaidrechargevalidation", strings.NewReader(data), headers)
	if err != nil || status != 200 {
		return nil
	}
	var parsed struct {
		Status         string `json:"STATUS"`
		SubscriberType string `json:"subscriberType"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil
	}
	if parsed.Status == "SUCCESS" {
		switch parsed.SubscriberType {
		case "POSTPAID":
			t := true
			return &t
		case "PREPAID":
			f := false
			return &f
		}
		return nil
	}
	// NOT_FOUND or other => not postpaid.
	f := false
	return &f
}

// --- revocations: Outris TPI lane ---

func (s *PhoneMetaService) revocations(ctx context.Context, national string) map[string]any {
	empty := map[string]any{}
	if s.cfg == nil {
		return empty
	}
	tpi, ok := s.cfg.Get("tpi_global_config", nil).(map[string]any)
	if !ok {
		return empty
	}
	outris, ok := tpi["outris"].(map[string]any)
	if !ok {
		return empty
	}
	if enabled, _ := outris["enabled"].(bool); !enabled {
		return empty
	}
	baseURL, _ := outris["base_url"].(string)
	endpoint, _ := outris["endpoint"].(string)
	apiKey, _ := outris["api_key"].(string)
	if baseURL == "" || endpoint == "" {
		return empty
	}
	body, _ := json.Marshal(map[string]any{"phone": national})
	headers := map[string]string{"x-api-key": apiKey, "Content-Type": "application/json"}
	// Outris runs without proxy in Python.
	status, respBody, err := s.doDirect(ctx, "POST", baseURL+endpoint, strings.NewReader(string(body)), headers)
	if err != nil || status != 200 {
		return empty
	}
	var parsed struct {
		Data struct {
			MnrlRevocation []struct {
				DateOfDisconnection string `json:"dateOfDisconnection"`
			} `json:"mnrlRevocation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return empty
	}
	// Collect unique (year, month) from dd-mm-yyyy dates; latest is the max.
	type ym struct{ y, m int }
	set := map[ym]struct{}{}
	for _, r := range parsed.Data.MnrlRevocation {
		parts := strings.Split(r.DateOfDisconnection, "-")
		if len(parts) != 3 {
			continue
		}
		y, err1 := strconv.Atoi(parts[2])
		m, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		set[ym{y, m}] = struct{}{}
	}
	if len(set) == 0 {
		return empty
	}
	var latest ym
	for k := range set {
		if k.y > latest.y || (k.y == latest.y && k.m > latest.m) {
			latest = k
		}
	}
	return map[string]any{
		"total_revocations": len(set),
		"year":              latest.y,
		"month":             latest.m,
	}
}

// --- HTTP helpers (proxy-aware / TLS-mode-aware) ---

func (s *PhoneMetaService) doProxy(ctx context.Context, method, u string, body io.Reader, headers map[string]string) (int, []byte, error) {
	return doHTTP(ctx, s.proxyURL, s.timeout, false, method, u, body, headers)
}
func (s *PhoneMetaService) doTLS(ctx context.Context, method, u string, body io.Reader, headers map[string]string) (int, []byte, error) {
	return doHTTP(ctx, s.proxyURL, s.timeout, true, method, u, body, headers)
}
func (s *PhoneMetaService) doDirect(ctx context.Context, method, u string, body io.Reader, headers map[string]string) (int, []byte, error) {
	return doHTTP(ctx, nil, s.timeout, false, method, u, body, headers)
}

// --- VI encryption (AES-CBC/PKCS7 + PBKDF2-HMAC-SHA1) ---

// viPrepareData reproduces postopaid_vi.prepare_data: build the request params
// JSON, encrypt it, and return the "mobile={...}" form body.
func viPrepareData(national string) (string, error) {
	paramsJSON, _ := json.Marshal(map[string]string{
		"mobNumber":          national,
		"isCouponIdentifier": "COUPON",
		"YbbCheck":           "YbbCheck",
		"journeyType":        "ORC-category",
	})
	enc, err := viEncrypt(string(paramsJSON))
	if err != nil {
		return "", err
	}
	obj, _ := json.Marshal(map[string]string{
		"params": enc.encryptedNumber, "sl": enc.salt, "algf": enc.iv, "sps": enc.secretPassPhrase,
	})
	return "mobile=" + string(obj), nil
}

type viEnc struct {
	encryptedNumber, salt, iv, secretPassPhrase string
}

func viEncrypt(plaintext string) (viEnc, error) {
	salt := make([]byte, 16)
	iv := make([]byte, 16)
	secret := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return viEnc{}, err
	}
	if _, err := rand.Read(iv); err != nil {
		return viEnc{}, err
	}
	if _, err := rand.Read(secret); err != nil {
		return viEnc{}, err
	}
	// PBKDF2 password = hex string of the random secret bytes (matches CryptoJS
	// secretPassPhrase.toString()); SHA1, 100 iters, 16-byte key.
	secretHex := hex.EncodeToString(secret)
	key := pbkdf2.Key([]byte(secretHex), salt, 100, 16, sha1New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return viEnc{}, err
	}
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	b64 := base64.StdEncoding.EncodeToString(ct)
	b64 = strings.ReplaceAll(b64, "+", "%2B") // match the JS handle_special_chars
	return viEnc{
		encryptedNumber:  b64,
		salt:             hex.EncodeToString(salt),
		iv:               hex.EncodeToString(iv),
		secretPassPhrase: secretHex,
	}, nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// --- small utils ---

func anyToStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON numbers decode as float64; render as an integer id.
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}
