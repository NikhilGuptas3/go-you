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
// existence check. Ports of the Python token-pool spiders.
//
//   EVENTBRITE  (email)  cookie-only; x-csrftoken read from the jar
//   TRIVAGO     (email)  cookie w/ XSRF-TOKEN echoed into x-xsrf-token
//   OYOROOMS    (phone)  cookie w/ XSRF-TOKEN + hardcoded deviceid/sdata; uTLS
//   ZOHO        (both)   iamcsr regex from cookie -> X-ZCSRF-TOKEN
//   VIMEO       (email)  JSON xsrft -> token= body field
//   SHOPCLUES   (both)   cookie + hardcoded csrf_test_name
//
// Each is a TokenCrawler: step 1 (GenerateToken) fetches the identifier-agnostic
// session token (a cookie/CSRF), captured INTO the token map alongside the cookie
// string so a pooled token works on a fresh step-2 client (an empty jar cannot
// replay cookies). step 2 (CheckWithToken) sends the cookie as an explicit
// header. Check delegates via tokenVia: warm pooled token on a hit, inline
// generate on a miss (the get_or_generate_token fallback).

// --- EVENTBRITE (email) ---
// step1 GET / -> csrftoken cookie; step2 POST /api/v3/users/lookup/ {email}
// with cookie + x-csrftoken. exists&&user_id => true; !exists&&!user_id => false.
type Eventbrite struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewEventbrite(timeout time.Duration) *Eventbrite { return &Eventbrite{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating inline.
func (c *Eventbrite) WithTokenSource(src TokenSource) *Eventbrite { c.tokens = src; return c }

func (c *Eventbrite) Website() string { return "EVENTBRITE" }
func (c *Eventbrite) Kind() Kind      { return KindEmail }

const eventbriteCookieURL = "https://www.eventbrite.com/"
const eventbriteLoginURL = "https://www.eventbrite.com/api/v3/users/lookup/"

func (c *Eventbrite) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	if _, _, _, err := doRequestFull(ctx, client, "GET", eventbriteCookieURL, nil, map[string]string{
		"Referer": "https://www.google.com/", "Upgrade-Insecure-Requests": "1", "User-Agent": ua,
	}); err != nil {
		return nil, fmt.Errorf("eventbrite cookie: %w", err)
	}
	csrf := cookieValue(client, eventbriteLoginURL, "csrftoken")
	if csrf == "" {
		return nil, fmt.Errorf("eventbrite: no csrftoken cookie")
	}
	return map[string]string{
		"csrftoken": csrf,
		"cookie":    cookieStringFor(client, eventbriteLoginURL),
	}, nil
}

func (c *Eventbrite) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	csrf := token["csrftoken"]
	if csrf == "" {
		return false, fmt.Errorf("eventbrite: empty token")
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	body, _ := json.Marshal(map[string]any{"email": identifier, "source_user_id": "", "source_provider": ""})
	headers := map[string]string{
		"accept": "*/*", "content-type": "application/json",
		"origin": "https://www.eventbrite.com", "referer": "https://www.eventbrite.com/",
		"x-requested-with": "XMLHttpRequest", "x-csrftoken": csrf, "User-Agent": ua,
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["Cookie"] = cookie
	}
	status, respBody, _, err := doRequestFull(ctx, client, "POST", eventbriteLoginURL, strings.NewReader(string(body)), headers)
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

func (c *Eventbrite) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- TRIVAGO (email) ---
// step1 GET /server/init -> XSRF-TOKEN cookie; step2 POST /server/members/exist
// with cookie + x-xsrf-token. JSON exists bool.
type Trivago struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewTrivago(timeout time.Duration) *Trivago { return &Trivago{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating inline.
func (c *Trivago) WithTokenSource(src TokenSource) *Trivago { c.tokens = src; return c }

func (c *Trivago) Website() string { return "TRIVAGO" }
func (c *Trivago) Kind() Kind      { return KindEmail }

const trivagoInitURL = "https://auth.trivago.com/server/init?locale=en-US&target=https%3A%2F%2Fauth.trivago.com%2Fen-US"
const trivagoLoginURL = "https://auth.trivago.com/server/members/exist?locale=en-IN"

func (c *Trivago) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	if _, _, _, err := doRequestFull(ctx, client, "GET", trivagoInitURL, nil, map[string]string{
		"authority": "auth.trivago.com", "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"referer": "https://auth.trivago.com/en-US", "upgrade-insecure-requests": "1", "user-agent": ua,
	}); err != nil {
		return nil, fmt.Errorf("trivago init: %w", err)
	}
	xsrf := cookieValue(client, trivagoLoginURL, "XSRF-TOKEN")
	if xsrf == "" {
		return nil, fmt.Errorf("trivago: XSRF-TOKEN not present")
	}
	return map[string]string{
		"xsrf":   xsrf,
		"cookie": cookieStringFor(client, trivagoLoginURL),
	}, nil
}

func (c *Trivago) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	xsrf := token["xsrf"]
	if xsrf == "" {
		return false, fmt.Errorf("trivago: empty token")
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	body, _ := json.Marshal(map[string]any{"email": identifier})
	headers := map[string]string{
		"authority": "auth.trivago.com", "accept": "application/json", "content-type": "application/json",
		"origin": "https://auth.trivago.com", "referer": "https://auth.trivago.com/en-IN",
		"x-requested-with": "XMLHttpRequest", "x-xsrf-token": xsrf, "user-agent": ua,
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["cookie"] = cookie
	}
	status, respBody, _, err := doRequestFull(ctx, client, "POST", trivagoLoginURL, strings.NewReader(string(body)), headers)
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

func (c *Trivago) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- OYOROOMS (phone) ---
// step1 GET / -> XSRF-TOKEN cookie; step2 POST /api/pwa/userSignIn with cookie +
// xsrf-token + hardcoded deviceid/sdata. HTTP 422 error.code 68=exist, 70=not.
// uTLS (Python curl_cffi). Phone only (national number).
type Oyorooms struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewOyorooms(timeout time.Duration) *Oyorooms { return &Oyorooms{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating inline.
func (c *Oyorooms) WithTokenSource(src TokenSource) *Oyorooms { c.tokens = src; return c }

func (c *Oyorooms) Website() string { return "OYOROOMS" }
func (c *Oyorooms) Kind() Kind      { return KindPhone }

// oyoDeviceID / oyoSdata are the hardcoded values from the Python spider
// (oyo_rooms.py:92-93). The upstream TODO notes these could be per-request from a
// token service post-NFR; go-you carries the fixed values, matching current prod.
// They are NOT part of the pooled token (not fetched in step 1) — they stay
// hardcoded in step 2.
const (
	oyoDeviceID = "92477f3559d2a84441f8852bb5291f21682218"
	oyoSdata    = "eyJrdWQiOls2MjQwMCw3NDIwMCw2NzAwMCw1NzYwMCw3MjkwMCw1MzAwMCw2NTIwMCw3ODYwMCw0NzUwMCw1NTYwMCw4MjMwMCw4NzgwMCw3MzcwMCw3MDEwMCwxMTg3MDBdLCJhY2MiOltdLCJneXIiOltdLCJ0dWQiOltdLCJ0aWQiOltdLCJraWQiOlsyNDY1OTQwMCwzMDI1MDAsMTM4NDAwLDEzMzUwMCwxMDQxMDAsMjAyNzAwLDE2MzIwMCwxMDY4MDAsNTMyMDAsOTkyMDAsMTY3MjIwMCwxODIzMDAwLDE3MDMwMCw0MDcwMCw2MzIwMF0sInRtdiI6W119"
)

func (c *Oyorooms) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	const cookieURL = "https://www.oyorooms.com"
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
	if _, _, _, err := doRequestFull(ctx, client, "GET", cookieURL, nil, map[string]string{
		"authority": "www.oyorooms.com", "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "en-GB,en;q=0.9", "upgrade-insecure-requests": "1",
	}); err != nil {
		return nil, fmt.Errorf("oyorooms cookie: %w", err)
	}
	xsrf := cookieValue(client, oyoroomsLoginURL, "XSRF-TOKEN")
	if xsrf == "" {
		return nil, fmt.Errorf("oyorooms: XSRF-TOKEN not present")
	}
	return map[string]string{
		"xsrf":   xsrf,
		"cookie": cookieStringFor(client, oyoroomsLoginURL),
	}, nil
}

const oyoroomsLoginURL = "https://www.oyorooms.com/api/pwa/userSignIn?locale=en"

func (c *Oyorooms) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	xsrf := token["xsrf"]
	if xsrf == "" {
		return false, fmt.Errorf("oyorooms: empty token")
	}
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
	payload := `{"phone":"` + nationalNumber(identifier) + `","password":"sdsvf"}`
	headers := map[string]string{
		"authority": "www.oyorooms.com", "accept": "*/*", "content-type": "text/plain;charset=UTF-8",
		"deviceid": oyoDeviceID, "loc": "223", "origin": "https://www.oyorooms.com",
		"referer": "https://www.oyorooms.com/login?country=&retUrl=/", "sdata": oyoSdata,
		"xsrf-token": xsrf,
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["cookie"] = cookie
	}
	status, respBody, _, err := doRequestFull(ctx, client, "POST", oyoroomsLoginURL, strings.NewReader(payload), headers)
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

func (c *Oyorooms) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- ZOHO (both) ---
// step1 GET /signin -> iamcsr cookie (regex the value); step2 POST
// /signin/v2/lookup/{id} with cookie + X-ZCSRF-TOKEN=iamcsrcoo={csr}. JSON message.
type Zoho struct {
	kind    Kind
	timeout time.Duration
	tokens  TokenSource
}

func NewZohoPhone(timeout time.Duration) *Zoho { return &Zoho{kind: KindPhone, timeout: timeout} }
func NewZohoEmail(timeout time.Duration) *Zoho { return &Zoho{kind: KindEmail, timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating inline.
func (c *Zoho) WithTokenSource(src TokenSource) *Zoho { c.tokens = src; return c }

func (c *Zoho) Website() string { return "ZOHO" }
func (c *Zoho) Kind() Kind      { return c.kind }

var zohoIamcsrRe = regexp.MustCompile(`iamcsr=(.*?);`)

const zohoCookieURL = "https://accounts.zoho.in/signin"
const zohoLoginURL = "https://accounts.zoho.in/signin/v2/lookup/"

func (c *Zoho) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	if _, _, _, err := doRequestFull(ctx, client, "GET", zohoCookieURL, nil, map[string]string{
		"User-Agent": ua, "Referer": "https://accounts.zoho.in/signin",
	}); err != nil {
		return nil, fmt.Errorf("zoho cookie: %w", err)
	}
	// Python regexes the cookie string for iamcsr; the value is also a plain
	// cookie, so read it from the jar (equivalent) and fall back to the regex on
	// the reconstructed cookie string for parity.
	csr := cookieValue(client, zohoLoginURL, "iamcsr")
	if csr == "" {
		if m := zohoIamcsrRe.FindStringSubmatch(cookieStringFor(client, zohoLoginURL) + ";"); len(m) == 2 {
			csr = m[1]
		}
	}
	if csr == "" {
		return nil, fmt.Errorf("zoho: iamcsr not present")
	}
	return map[string]string{
		"iamcsr": csr,
		"cookie": cookieStringFor(client, zohoLoginURL),
	}, nil
}

func (c *Zoho) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	csr := token["iamcsr"]
	if csr == "" {
		return false, fmt.Errorf("zoho: empty token")
	}
	loginID := identifier
	if c.kind == KindPhone {
		loginID = "91-" + nationalNumber(identifier)
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	payload := fmt.Sprintf("mode=primary&cli_time=%d&servicename=AaaServer&serviceurl=https%%3A%%2F%%2Faccounts.zoho.in%%2Fu%%2Fh", time.Now().UnixMilli())
	status, respBody, _, err := doRequestFull(ctx, client, "POST", zohoLoginURL+url.PathEscape(loginID), strings.NewReader(payload), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
		"Origin":       "https://accounts.zoho.in", "Referer": "https://accounts.zoho.in/signin",
		"User-Agent": ua, "X-ZCSRF-TOKEN": "iamcsrcoo=" + csr, "Cookie": token["cookie"],
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

func (c *Zoho) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- VIMEO (email) ---
// step1 GET /me/info -> cookies + JSON xsrft; step2 POST /log_in with cookie +
// body token=xsrft. has_error_invalid_credentials=='invalid_credentials'=>exist.
type Vimeo struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewVimeo(timeout time.Duration) *Vimeo { return &Vimeo{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating inline.
func (c *Vimeo) WithTokenSource(src TokenSource) *Vimeo { c.tokens = src; return c }

func (c *Vimeo) Website() string { return "VIMEO" }
func (c *Vimeo) Kind() Kind      { return KindEmail }

const vimeoTokenURL = "https://vimeo.com/me/info"
const vimeoLoginURL = "https://vimeo.com/log_in"

func (c *Vimeo) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	_, tokBody, _, err := doRequestFull(ctx, client, "GET", vimeoTokenURL, nil, map[string]string{
		"Accept": "*/*", "Referer": "https://vimeo.com/", "User-Agent": ua,
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
	})
	if err != nil {
		return nil, fmt.Errorf("vimeo token: %w", err)
	}
	var tok struct {
		Xsrft string `json:"xsrft"`
	}
	if json.Unmarshal(tokBody, &tok) != nil || tok.Xsrft == "" {
		return nil, fmt.Errorf("vimeo: xsrft not present")
	}
	return map[string]string{
		"xsrft":  tok.Xsrft,
		"cookie": cookieStringFor(client, vimeoLoginURL),
	}, nil
}

func (c *Vimeo) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	xsrft := token["xsrft"]
	if xsrft == "" {
		return false, fmt.Errorf("vimeo: empty token")
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	form := url.Values{}
	form.Set("email", identifier)
	form.Set("password", "sfafaadad")
	form.Set("token", xsrft)
	form.Set("action", "login")
	form.Set("service", "vimeo")
	headers := map[string]string{
		"Accept": "*/*", "Origin": "https://vimeo.com", "Referer": "https://vimeo.com/",
		"content-type": "application/x-www-form-urlencoded", "x-requested-with": "XMLHttpRequest", "User-Agent": ua,
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["Cookie"] = cookie
	}
	status, respBody, _, err := doRequestFull(ctx, client, "POST", vimeoLoginURL, strings.NewReader(form.Encode()), headers)
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

func (c *Vimeo) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- SHOPCLUES (both) ---
// step1 GET /popup -> session cookie; step2 POST /user/checkexistence with cookie
// + body user_login={id}&csrf_test_name={HARDCODED}. JSON user_exists==1 => true.
// (Python parses a csrf from the cookie but then overwrites it with a constant, so
// only the cookie + the fixed csrf matter — shopclues.py:141.) The csrf is NOT
// fetched in step 1, so it stays hardcoded in step 2; only the cookie is pooled.
type Shopclues struct {
	kind    Kind
	timeout time.Duration
	tokens  TokenSource
}

func NewShopcluesPhone(timeout time.Duration) *Shopclues {
	return &Shopclues{kind: KindPhone, timeout: timeout}
}
func NewShopcluesEmail(timeout time.Duration) *Shopclues {
	return &Shopclues{kind: KindEmail, timeout: timeout}
}

// WithTokenSource wires the crawler to consult a token pool before generating inline.
func (c *Shopclues) WithTokenSource(src TokenSource) *Shopclues { c.tokens = src; return c }

func (c *Shopclues) Website() string { return "SHOPCLUES" }
func (c *Shopclues) Kind() Kind      { return c.kind }

const shopcluesCSRF = "bc46087d19da950007aa702bdbe96114"
const shopcluesCookieURL = "https://login.shopclues.com/popup"
const shopcluesLoginURL = "https://login.shopclues.com/user/checkexistence"

func (c *Shopclues) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	if _, _, _, err := doRequestFull(ctx, client, "GET", shopcluesCookieURL, nil, map[string]string{
		"accept": "*/*", "origin": "https://www.shopclues.com",
		"referer": "https://www.shopclues.com/", "user-agent": ua,
	}); err != nil {
		return nil, fmt.Errorf("shopclues cookie: %w", err)
	}
	cookie := cookieStringFor(client, shopcluesLoginURL)
	if cookie == "" {
		return nil, fmt.Errorf("shopclues: no cookie obtained")
	}
	return map[string]string{"cookie": cookie}, nil
}

func (c *Shopclues) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	cookie := token["cookie"]
	if cookie == "" {
		return false, fmt.Errorf("shopclues: empty token")
	}
	loginID := identifier
	if c.kind == KindPhone {
		loginID = nationalNumber(identifier)
	} else {
		loginID = url.QueryEscape(identifier) // Python urllib.parse.quote
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	payload := "user_login=" + loginID + "&csrf_test_name=" + shopcluesCSRF
	status, respBody, _, err := doRequestFull(ctx, client, "POST", shopcluesLoginURL, strings.NewReader(payload), map[string]string{
		"accept": "*/*", "content-type": "application/x-www-form-urlencoded; charset=UTF-8",
		"origin": "https://www.shopclues.com", "referer": "https://www.shopclues.com/", "user-agent": ua,
		"Cookie": cookie,
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

func (c *Shopclues) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}
