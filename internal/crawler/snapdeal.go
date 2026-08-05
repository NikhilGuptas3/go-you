package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SNAPDEAL is the reference TWO-STEP (token-gated) crawler and the template for
// the rest of Phase B. It ports crawler/spiders/ecommerce/snapdeal:
//
//	step 1: GET https://www.snapdeal.com/web/getUserDetails  -> Set-Cookie (session)
//	step 2: POST https://www.snapdeal.com/isUserExists {"userName": id}
//	        with the session cookie -> JSON {"userExist": bool}
//
// Both steps hit the same host, so the shared cookie jar (newHTTPClientJar)
// replays step-1's cookies on step 2 automatically — no manual cookie string.
// Snapdeal fingerprints TLS (Python uses curl_cffi), so it uses TLSChrome (uTLS).
// It serves BOTH channels: email is passed through; phone uses the national
// number (snapdeal_phone.get_login_identifier -> national_number).
type Snapdeal struct {
	kind    Kind
	timeout time.Duration
}

func NewSnapdealPhone(timeout time.Duration) *Snapdeal {
	return &Snapdeal{kind: KindPhone, timeout: timeout}
}
func NewSnapdealEmail(timeout time.Duration) *Snapdeal {
	return &Snapdeal{kind: KindEmail, timeout: timeout}
}

func (c *Snapdeal) Website() string { return "SNAPDEAL" }
func (c *Snapdeal) Kind() Kind      { return c.kind }

const (
	snapdealCookieURL = "https://www.snapdeal.com/web/getUserDetails"
	snapdealLoginURL  = "https://www.snapdeal.com/isUserExists"
)

func (c *Snapdeal) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	userName := identifier
	if c.kind == KindPhone {
		userName = nationalNumber(identifier)
	}

	// One jar client for both steps; step-1 cookies replay on step 2 (same host).
	// TLSChrome because Snapdeal blocks the stock Go ClientHello (Python: curl_cffi).
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)

	// --- step 1: obtain the session cookie ---
	cookieHeaders := map[string]string{
		"accept":          "*/*",
		"accept-language": "en-IN,en;q=0.9",
		"priority":        "u=1, i",
		"referer":         "https://www.snapdeal.com/",
		"sec-fetch-dest":  "empty",
		"sec-fetch-mode":  "cors",
		"sec-fetch-site":  "same-origin",
		"user-agent":      randomUA(),
	}
	if _, _, _, err := doRequestFull(ctx, client, "GET", snapdealCookieURL, nil, cookieHeaders); err != nil {
		return false, fmt.Errorf("snapdeal cookie fetch: %w", err)
	}
	if cookieStringFor(client, snapdealLoginURL) == "" {
		return false, fmt.Errorf("snapdeal: no session cookie obtained")
	}

	// --- step 2: existence check (jar carries the cookie automatically) ---
	body, _ := json.Marshal(map[string]any{"userName": userName})
	loginHeaders := map[string]string{
		"accept":           "*/*",
		"accept-language":  "en-IN,en;q=0.9",
		"content-type":     "application/json",
		"origin":           "https://www.snapdeal.com",
		"priority":         "u=1, i",
		"referer":          "https://www.snapdeal.com/",
		"sec-fetch-dest":   "empty",
		"sec-fetch-mode":   "cors",
		"sec-fetch-site":   "same-origin",
		"x-requested-with": "XMLHttpRequest",
		"user-agent":       randomUA(),
	}
	status, respBody, _, err := doRequestFull(ctx, client, "POST", snapdealLoginURL, strings.NewReader(string(body)), loginHeaders)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("snapdeal: no condition matched (status=%d)", status)
	}
	var parsed struct {
		UserExist *bool `json:"userExist"`
	}
	if json.Unmarshal(respBody, &parsed) != nil || parsed.UserExist == nil {
		return false, fmt.Errorf("snapdeal: no condition matched (unparseable userExist)")
	}
	return *parsed.UserExist, nil
}
