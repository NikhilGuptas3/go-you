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
// The session cookie from step 1 is identifier-agnostic, so Snapdeal is a
// TokenCrawler: GenerateToken fetches the cookie (poolable), CheckWithToken runs
// the existence check with it. The cookie is carried in the token map (not left
// in a jar) so a pooled token works on a fresh step-2 client — a pooled token
// runs on a different client than the one that minted it.
// Snapdeal fingerprints TLS (Python uses curl_cffi), so it uses TLSChrome (uTLS).
// It serves BOTH channels: email is passed through; phone uses the national
// number (snapdeal_phone.get_login_identifier -> national_number).
type Snapdeal struct {
	kind    Kind
	timeout time.Duration
	tokens  TokenSource
}

func NewSnapdealPhone(timeout time.Duration) *Snapdeal {
	return &Snapdeal{kind: KindPhone, timeout: timeout}
}
func NewSnapdealEmail(timeout time.Duration) *Snapdeal {
	return &Snapdeal{kind: KindEmail, timeout: timeout}
}

// WithTokenSource wires the crawler to consult a token pool before generating
// inline. Called once at composition; the base constructor stays pool-agnostic.
func (c *Snapdeal) WithTokenSource(src TokenSource) *Snapdeal { c.tokens = src; return c }

func (c *Snapdeal) Website() string { return "SNAPDEAL" }
func (c *Snapdeal) Kind() Kind      { return c.kind }

const (
	snapdealCookieURL = "https://www.snapdeal.com/web/getUserDetails"
	snapdealLoginURL  = "https://www.snapdeal.com/isUserExists"
)

// GenerateToken performs step 1: GET the session endpoint and capture the
// Set-Cookie session. The cookie is identifier-agnostic, so the pool can hand it
// to any lookup. It is returned in the token map (not left in a jar) so a pooled
// token works on a fresh step-2 client.
func (c *Snapdeal) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	// TLSChrome because Snapdeal blocks the stock Go ClientHello (Python: curl_cffi).
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
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
		return nil, fmt.Errorf("snapdeal cookie fetch: %w", err)
	}
	cookie := cookieStringFor(client, snapdealLoginURL)
	if cookie == "" {
		return nil, fmt.Errorf("snapdeal: no session cookie obtained")
	}
	return map[string]string{"cookie": cookie}, nil
}

// CheckWithToken performs step 2 for identifier using the session cookie from
// the token. The cookie is sent as an explicit header so the token works whether
// it came from the pool or an inline fetch.
func (c *Snapdeal) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	cookie := token["cookie"]
	if cookie == "" {
		return false, fmt.Errorf("snapdeal: empty token")
	}
	userName := identifier
	if c.kind == KindPhone {
		userName = nationalNumber(identifier)
	}

	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
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
		"cookie":           cookie,
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

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *Snapdeal) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}
