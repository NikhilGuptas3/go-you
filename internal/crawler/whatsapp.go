package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// WhatsappWappsure ports crawler/spiders/social/whatsapp/sources/wappsure_api_v2.py
// — the WHATSAPP_WAPPSURE_API source, which is the DEFAULT (weight 5) enabled
// WhatsApp source in hey-you. It is NOT a self-crawl: it calls the third-party
// wa-validator.xyz vendor API, so all go-you does is reproduce that one POST.
//
// POST https://validator.wa-validator.xyz/v2/wa_id/profile
//
//	body: {"number": "<international_number>"}
//	header: Authorization: <bearer key from tpi_global_config.wappsure.api_key>
//	curl_cffi (impersonate=chrome) => TLSChrome; NO proxy (Python sends proxy=None).
//
// Verdict (parse_login_response):
//
//	non-200                    -> error (TPINotDesiredStatusCode)
//	status != true             -> error
//	error != null              -> error
//	valid == true              -> exist (+ rich profile: phone/status/status_updated_on/image)
//	valid == false             -> not-exist
//	else                       -> NoConditionMatched
//
// This is a DetailCrawler: on a positive verdict it returns the profile fields
// as rich per-site data, matching the WhatsappProfile Python builds. The
// business-lookup parallel call and the image-event push are out of scope
// (that is separate orchestration, not this source).
type WhatsappWappsure struct {
	timeout time.Duration
	// bearer is the vendor API key, verbatim (already includes the "Bearer "
	// prefix), from tpi_global_config.wappsure.api_key. Empty => the crawler
	// errors on every call (no key configured) rather than silently no-ops, so a
	// misconfig is visible in spider_error rather than looking like "not found".
	bearer string
	// overrideURL is a test-only seam pointing the vendor call at an httptest
	// server. Empty in production => the real wappsureProfileURL.
	overrideURL string
}

// NewWhatsappWappsure builds the crawler with the vendor bearer key. Pass the
// resolved tpi_global_config.wappsure.api_key.
func NewWhatsappWappsure(timeout time.Duration, bearer string) *WhatsappWappsure {
	return &WhatsappWappsure{timeout: timeout, bearer: bearer}
}

func (c *WhatsappWappsure) Website() string { return "WHATSAPP" }
func (c *WhatsappWappsure) Kind() Kind      { return KindPhone }

// Check satisfies the base Crawler interface (existence-only). The runner
// prefers CheckDetail (rich path) when a crawler implements DetailCrawler, so
// this is the fallback; it delegates and drops the profile data.
func (c *WhatsappWappsure) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	exist, _, err := c.CheckDetail(ctx, identifier, proxyURL)
	if err != nil {
		return false, err
	}
	return exist != nil && *exist, nil
}

const wappsureProfileURL = "https://validator.wa-validator.xyz/v2/wa_id/profile"

// CheckDetail performs the vendor call and returns the verdict plus (on exist)
// the WhatsApp profile fields.
func (c *WhatsappWappsure) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	if c.bearer == "" {
		return nil, nil, fmt.Errorf("whatsapp: no wappsure api_key configured")
	}
	number := internationalNumber(identifier)
	// Python sends the international number WITHOUT the leading '+' in the
	// __main__ example ("91"+national); internationalNumber yields "+91…", so
	// strip the '+' to match "<cc><national>".
	number = strings.TrimPrefix(number, "+")

	payload, _ := json.Marshal(map[string]any{"number": number})
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": c.bearer,
	}
	// curl_cffi impersonate=chrome, proxy=None (the vendor allowlists by key, not
	// by IP) => TLSChrome client with a nil proxy regardless of the global proxy.
	// In test mode (overrideURL set), use the stock stack so a plain-HTTP
	// httptest server is reachable.
	target := wappsureProfileURL
	mode := TLSChrome
	if c.overrideURL != "" {
		target = c.overrideURL
		mode = TLSDefault
	}
	client := newHTTPClientTLS(nil, c.timeout, mode)
	status, body, err := doRequest(ctx, client, "POST", target, strings.NewReader(string(payload)), headers)
	if err != nil {
		return nil, nil, err
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("whatsapp wappsure: status %d", status)
	}

	var resp struct {
		Status bool   `json:"status"`
		Error  any    `json:"error"`
		Valid  *bool  `json:"valid"`
		Phone  string `json:"phone"`
		About  *struct {
			Text string `json:"text"`
			Meta *struct {
				LastUpdatedAt any `json:"last_updated_at"`
			} `json:"meta"`
		} `json:"about"`
		Image *struct {
			Exist bool   `json:"exist"`
			URL   string `json:"url"`
		} `json:"image"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("whatsapp wappsure decode: %w", err)
	}
	if !resp.Status {
		return nil, nil, fmt.Errorf("whatsapp wappsure: api status not true")
	}
	if resp.Error != nil {
		return nil, nil, fmt.Errorf("whatsapp wappsure: profile api error: %v", resp.Error)
	}
	if resp.Valid == nil {
		return nil, nil, fmt.Errorf("whatsapp wappsure: no condition matched")
	}
	if !*resp.Valid {
		return boolPtr(false), nil, nil
	}

	// valid == true: build the rich profile (WhatsappProfile fields, snake_case
	// keys matching the Python to_dict output).
	exist := true
	data := map[string]any{
		"phone":      resp.Phone,
		"user_exist": true,
	}
	if resp.About != nil {
		data["status"] = resp.About.Text
		if resp.About.Meta != nil {
			data["status_updated_on"] = resp.About.Meta.LastUpdatedAt
		}
	}
	if resp.Image != nil && resp.Image.Exist && resp.Image.URL != "" {
		data["image"] = resp.Image.URL
	}
	return &exist, data, nil
}
