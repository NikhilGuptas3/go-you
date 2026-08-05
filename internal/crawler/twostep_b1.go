package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Phase B1 — the six "easy" token-gated crawlers: a cookie jar + at most a light
// value pulled from the cookie or a JSON field. All follow the SNAPDEAL reference
// shape (twostep.go / snapdeal.go): one jar client, step-1 token fetch, step-2
// existence check. Ports of the Python token-pool spiders, run STATELESSLY (a
// token fetch per request) — faithful in result without the background pool.
//
//   EVENTBRITE  (email)  cookie-only; x-csrftoken read from the jar
//   TRIVAGO     (email)  cookie w/ XSRF-TOKEN echoed into x-xsrf-token
//   OYOROOMS    (phone)  cookie w/ XSRF-TOKEN + hardcoded deviceid/sdata; uTLS
//   ZOHO        (both)   iamcsr regex from cookie -> X-ZCSRF-TOKEN
//   VIMEO       (email)  JSON xsrft -> token= body field
//   SHOPCLUES   (both)   cookie + hardcoded csrf_test_name

// --- EVENTBRITE (email) ---
// step1 GET / -> csrftoken cookie; step2 POST /api/v3/users/lookup/ {email}
// with cookie + x-csrftoken. exists&&user_id => true; !exists&&!user_id => false.
type Eventbrite struct{ timeout time.Duration }

func NewEventbrite(timeout time.Duration) *Eventbrite { return &Eventbrite{timeout: timeout} }
func (c *Eventbrite) Website() string                 { return "EVENTBRITE" }
func (c *Eventbrite) Kind() Kind                      { return KindEmail }

func (c *Eventbrite) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const cookieURL = "https://www.eventbrite.com/"
	const loginURL = "https://www.eventbrite.com/api/v3/users/lookup/"
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	if _, _, _, err := doRequestFull(ctx, client, "GET", cookieURL, nil, map[string]string{
		"Referer": "https://www.google.com/", "Upgrade-Insecure-Requests": "1", "User-Agent": ua,
	}); err != nil {
		return false, fmt.Errorf("eventbrite cookie: %w", err)
	}
	csrf := cookieValue(client, loginURL, "csrftoken")
	if csrf == "" {
		return false, fmt.Errorf("eventbrite: no csrftoken cookie")
	}
	body, _ := json.Marshal(map[string]any{"email": identifier, "source_user_id": "", "source_provider": ""})
	status, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL, strings.NewReader(string(body)), map[string]string{
		"accept": "*/*", "content-type": "application/json",
		"origin": "https://www.eventbrite.com", "referer": "https://www.eventbrite.com/",
		"x-requested-with": "XMLHttpRequest", "x-csrftoken": csrf, "User-Agent": ua,
	})
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("eventbrite: no condition matched (status=%d)", status)
	}
	var p struct {
		Exists *bool           `json:"exists"`
		UserID json.RawMessage `json:"user_id"`
	}
	if json.Unmarshal(respBody, &p) != nil || p.Exists == nil {
		return false, fmt.Errorf("eventbrite: no condition matched")
	}
	hasUserID := len(p.UserID) > 0 && string(p.UserID) != "null"
	if *p.Exists && hasUserID {
		return true, nil
	}
	if !*p.Exists && !hasUserID {
		return false, nil
	}
	return false, fmt.Errorf("eventbrite: no condition matched (exists=%v user_id=%v)", *p.Exists, hasUserID)
}

// --- TRIVAGO (email) ---
// step1 GET /server/init -> XSRF-TOKEN cookie; step2 POST /server/members/exist
// with cookie + x-xsrf-token. JSON exists bool.
type Trivago struct{ timeout time.Duration }

func NewTrivago(timeout time.Duration) *Trivago { return &Trivago{timeout: timeout} }
func (c *Trivago) Website() string              { return "TRIVAGO" }
func (c *Trivago) Kind() Kind                   { return KindEmail }

func (c *Trivago) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const initURL = "https://auth.trivago.com/server/init?locale=en-US&target=https%3A%2F%2Fauth.trivago.com%2Fen-US"
	const loginURL = "https://auth.trivago.com/server/members/exist?locale=en-IN"
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	if _, _, _, err := doRequestFull(ctx, client, "GET", initURL, nil, map[string]string{
		"authority": "auth.trivago.com", "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"referer": "https://auth.trivago.com/en-US", "upgrade-insecure-requests": "1", "user-agent": ua,
	}); err != nil {
		return false, fmt.Errorf("trivago init: %w", err)
	}
	xsrf := cookieValue(client, loginURL, "XSRF-TOKEN")
	if xsrf == "" {
		return false, fmt.Errorf("trivago: XSRF-TOKEN not present")
	}
	body, _ := json.Marshal(map[string]any{"email": identifier})
	status, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL, strings.NewReader(string(body)), map[string]string{
		"authority": "auth.trivago.com", "accept": "application/json", "content-type": "application/json",
		"origin": "https://auth.trivago.com", "referer": "https://auth.trivago.com/en-IN",
		"x-requested-with": "XMLHttpRequest", "x-xsrf-token": xsrf, "user-agent": ua,
	})
	if err != nil {
		return false, err
	}
	var p struct {
		Exists *bool `json:"exists"`
	}
	if status != 200 || json.Unmarshal(respBody, &p) != nil || p.Exists == nil {
		return false, fmt.Errorf("trivago: no condition matched (status=%d)", status)
	}
	return *p.Exists, nil
}

// --- OYOROOMS (phone) ---
// step1 GET / -> XSRF-TOKEN cookie; step2 POST /api/pwa/userSignIn with cookie +
// xsrf-token + hardcoded deviceid/sdata. HTTP 422 error.code 68=exist, 70=not.
// uTLS (Python curl_cffi). Phone only (national number).
type Oyorooms struct{ timeout time.Duration }

func NewOyorooms(timeout time.Duration) *Oyorooms { return &Oyorooms{timeout: timeout} }
func (c *Oyorooms) Website() string               { return "OYOROOMS" }
func (c *Oyorooms) Kind() Kind                    { return KindPhone }

// oyoDeviceID / oyoSdata are the hardcoded values from the Python spider
// (oyo_rooms.py:92-93). The upstream TODO notes these could be per-request from a
// token service post-NFR; go-you carries the fixed values, matching current prod.
const (
	oyoDeviceID = "92477f3559d2a84441f8852bb5291f21682218"
	oyoSdata    = "eyJrdWQiOls2MjQwMCw3NDIwMCw2NzAwMCw1NzYwMCw3MjkwMCw1MzAwMCw2NTIwMCw3ODYwMCw0NzUwMCw1NTYwMCw4MjMwMCw4NzgwMCw3MzcwMCw3MDEwMCwxMTg3MDBdLCJhY2MiOltdLCJneXIiOltdLCJ0dWQiOltdLCJ0aWQiOltdLCJraWQiOlsyNDY1OTQwMCwzMDI1MDAsMTM4NDAwLDEzMzUwMCwxMDQxMDAsMjAyNzAwLDE2MzIwMCwxMDY4MDAsNTMyMDAsOTkyMDAsMTY3MjIwMCwxODIzMDAwLDE3MDMwMCw0MDcwMCw2MzIwMF0sInRtdiI6W119"
)

func (c *Oyorooms) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const cookieURL = "https://www.oyorooms.com"
	const loginURL = "https://www.oyorooms.com/api/pwa/userSignIn?locale=en"
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)

	if _, _, _, err := doRequestFull(ctx, client, "GET", cookieURL, nil, map[string]string{
		"authority": "www.oyorooms.com", "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "en-GB,en;q=0.9", "upgrade-insecure-requests": "1",
	}); err != nil {
		return false, fmt.Errorf("oyorooms cookie: %w", err)
	}
	xsrf := cookieValue(client, loginURL, "XSRF-TOKEN")
	if xsrf == "" {
		return false, fmt.Errorf("oyorooms: XSRF-TOKEN not present")
	}
	payload := `{"phone":"` + nationalNumber(identifier) + `","password":"sdsvf"}`
	status, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL, strings.NewReader(payload), map[string]string{
		"authority": "www.oyorooms.com", "accept": "*/*", "content-type": "text/plain;charset=UTF-8",
		"deviceid": oyoDeviceID, "loc": "223", "origin": "https://www.oyorooms.com",
		"referer": "https://www.oyorooms.com/login?country=&retUrl=/", "sdata": oyoSdata,
		"xsrf-token": xsrf,
	})
	if err != nil {
		return false, err
	}
	if status != 422 {
		return false, fmt.Errorf("oyorooms: no condition matched (status=%d)", status)
	}
	var p struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(respBody, &p) != nil {
		return false, fmt.Errorf("oyorooms decode failed")
	}
	switch p.Error.Code {
	case 68:
		return true, nil
	case 70:
		return false, nil
	default:
		return false, fmt.Errorf("oyorooms: no condition matched (code=%d)", p.Error.Code)
	}
}

// --- ZOHO (both) ---
// step1 GET /signin -> iamcsr cookie (regex the value); step2 POST
// /signin/v2/lookup/{id} with cookie + X-ZCSRF-TOKEN=iamcsrcoo={csr}. JSON message.
type Zoho struct {
	kind    Kind
	timeout time.Duration
}

func NewZohoPhone(timeout time.Duration) *Zoho { return &Zoho{kind: KindPhone, timeout: timeout} }
func NewZohoEmail(timeout time.Duration) *Zoho { return &Zoho{kind: KindEmail, timeout: timeout} }
func (c *Zoho) Website() string                { return "ZOHO" }
func (c *Zoho) Kind() Kind                     { return c.kind }

var zohoIamcsrRe = regexp.MustCompile(`iamcsr=(.*?);`)

func (c *Zoho) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const cookieURL = "https://accounts.zoho.in/signin"
	const loginURL = "https://accounts.zoho.in/signin/v2/lookup/"
	loginID := identifier
	if c.kind == KindPhone {
		loginID = "91-" + nationalNumber(identifier)
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	if _, _, _, err := doRequestFull(ctx, client, "GET", cookieURL, nil, map[string]string{
		"User-Agent": ua, "Referer": "https://accounts.zoho.in/signin",
	}); err != nil {
		return false, fmt.Errorf("zoho cookie: %w", err)
	}
	// Python regexes the cookie string for iamcsr; the value is also a plain
	// cookie, so read it from the jar (equivalent) and fall back to the regex on
	// the reconstructed cookie string for parity.
	csr := cookieValue(client, loginURL, "iamcsr")
	if csr == "" {
		if m := zohoIamcsrRe.FindStringSubmatch(cookieStringFor(client, loginURL) + ";"); len(m) == 2 {
			csr = m[1]
		}
	}
	if csr == "" {
		return false, fmt.Errorf("zoho: iamcsr not present")
	}
	cookie := cookieStringFor(client, loginURL)
	payload := fmt.Sprintf("mode=primary&cli_time=%d&servicename=AaaServer&serviceurl=https%%3A%%2F%%2Faccounts.zoho.in%%2Fu%%2Fh", time.Now().UnixMilli())
	status, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL+url.PathEscape(loginID), strings.NewReader(payload), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
		"Origin":       "https://accounts.zoho.in", "Referer": "https://accounts.zoho.in/signin",
		"User-Agent": ua, "X-ZCSRF-TOKEN": "iamcsrcoo=" + csr, "Cookie": cookie,
	})
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("zoho: no condition matched (status=%d)", status)
	}
	var p struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(respBody, &p) != nil {
		return false, fmt.Errorf("zoho decode failed")
	}
	switch p.Message {
	case "User exists", "User exists in another DC":
		return true, nil
	case "User does not exists":
		return false, nil
	default:
		return false, fmt.Errorf("zoho: no condition matched (message=%q)", p.Message)
	}
}

// --- VIMEO (email) ---
// step1 GET /me/info -> cookies + JSON xsrft; step2 POST /log_in with cookie +
// body token=xsrft. has_error_invalid_credentials=='invalid_credentials'=>exist.
type Vimeo struct{ timeout time.Duration }

func NewVimeo(timeout time.Duration) *Vimeo { return &Vimeo{timeout: timeout} }
func (c *Vimeo) Website() string            { return "VIMEO" }
func (c *Vimeo) Kind() Kind                 { return KindEmail }

func (c *Vimeo) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const tokenURL = "https://vimeo.com/me/info"
	const loginURL = "https://vimeo.com/log_in"
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	_, tokBody, _, err := doRequestFull(ctx, client, "GET", tokenURL, nil, map[string]string{
		"Accept": "*/*", "Referer": "https://vimeo.com/", "User-Agent": ua,
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
	})
	if err != nil {
		return false, fmt.Errorf("vimeo token: %w", err)
	}
	var tok struct {
		Xsrft string `json:"xsrft"`
	}
	if json.Unmarshal(tokBody, &tok) != nil || tok.Xsrft == "" {
		return false, fmt.Errorf("vimeo: xsrft not present")
	}
	form := url.Values{}
	form.Set("email", identifier)
	form.Set("password", "sfafaadad")
	form.Set("token", tok.Xsrft)
	form.Set("action", "login")
	form.Set("service", "vimeo")
	status, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL, strings.NewReader(form.Encode()), map[string]string{
		"Accept": "*/*", "Origin": "https://vimeo.com", "Referer": "https://vimeo.com/",
		"content-type": "application/x-www-form-urlencoded", "x-requested-with": "XMLHttpRequest", "User-Agent": ua,
	})
	if err != nil {
		return false, err
	}
	_ = status
	var p struct {
		Err *string `json:"has_error_invalid_credentials"`
	}
	if json.Unmarshal(respBody, &p) != nil || p.Err == nil {
		return false, fmt.Errorf("vimeo: no condition matched")
	}
	switch *p.Err {
	case "invalid_credentials":
		return true, nil
	case "":
		return false, nil
	default:
		return false, fmt.Errorf("vimeo: no condition matched (%q)", *p.Err)
	}
}

// --- SHOPCLUES (both) ---
// step1 GET /popup -> session cookie; step2 POST /user/checkexistence with cookie
// + body user_login={id}&csrf_test_name={HARDCODED}. JSON user_exists==1 => true.
// (Python parses a csrf from the cookie but then overwrites it with a constant, so
// only the cookie + the fixed csrf matter — shopclues.py:141.)
type Shopclues struct {
	kind    Kind
	timeout time.Duration
}

func NewShopcluesPhone(timeout time.Duration) *Shopclues {
	return &Shopclues{kind: KindPhone, timeout: timeout}
}
func NewShopcluesEmail(timeout time.Duration) *Shopclues {
	return &Shopclues{kind: KindEmail, timeout: timeout}
}
func (c *Shopclues) Website() string { return "SHOPCLUES" }
func (c *Shopclues) Kind() Kind      { return c.kind }

const shopcluesCSRF = "bc46087d19da950007aa702bdbe96114"

func (c *Shopclues) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const cookieURL = "https://login.shopclues.com/popup"
	const loginURL = "https://login.shopclues.com/user/checkexistence"
	loginID := identifier
	if c.kind == KindPhone {
		loginID = nationalNumber(identifier)
	} else {
		loginID = url.QueryEscape(identifier) // Python urllib.parse.quote
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	if _, _, _, err := doRequestFull(ctx, client, "GET", cookieURL, nil, map[string]string{
		"accept": "*/*", "origin": "https://www.shopclues.com",
		"referer": "https://www.shopclues.com/", "user-agent": ua,
	}); err != nil {
		return false, fmt.Errorf("shopclues cookie: %w", err)
	}
	if cookieStringFor(client, loginURL) == "" {
		return false, fmt.Errorf("shopclues: no cookie obtained")
	}
	payload := "user_login=" + loginID + "&csrf_test_name=" + shopcluesCSRF
	status, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL, strings.NewReader(payload), map[string]string{
		"accept": "*/*", "content-type": "application/x-www-form-urlencoded; charset=UTF-8",
		"origin": "https://www.shopclues.com", "referer": "https://www.shopclues.com/", "user-agent": ua,
	})
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("shopclues: no condition matched (status=%d)", status)
	}
	var p struct {
		UserExists json.RawMessage `json:"user_exists"`
	}
	if json.Unmarshal(respBody, &p) != nil || len(p.UserExists) == 0 {
		return false, fmt.Errorf("shopclues: no condition matched")
	}
	// user_exists == 1 (Python compares == 1). Accept the numeric/bool/string forms.
	s := strings.Trim(string(p.UserExists), `"`)
	if n, err := strconv.Atoi(s); err == nil {
		return n == 1, nil
	}
	return s == "true", nil
}
