package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Phase B2 — the three token-gated crawlers that extract their token by parsing
// the HTML BODY of step 1 (regex / string-scan / <meta> tag), rather than reading
// a cookie. These are the most brittle to upstream markup changes. Same two-step
// shape as B0/B1; stateless (token per request).
//
//   TUMBLR      (email)  regex "API_TOKEN":"..." from HTML -> Bearer; NO cookie
//   QUORA       (email)  4 values string-scraped from HTML -> quora-* headers
//   CODECADEMY  (email)  two GETs; CSRF from <meta name="csrf-token"> -> header

// --- TUMBLR (email) ---
// step1 GET /explore/trending (Python sends NO proxy for this) -> regex the
// API_TOKEN out of the HTML. step2 POST /api/v2/login/mode with Bearer token,
// body {email, authentication:"oauth2_cookie"}. response.mode: password_reset=>
// exist, registration=>not.
//
// TokenCrawler: the API_TOKEN is identifier-agnostic and poolable. There is NO
// cookie (Tumblr uses a Bearer header), so the token map carries only api_token.
// The token fetch stays proxyless (Python proxy=None); step 2 uses the proxy.
type Tumblr struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewTumblr(timeout time.Duration) *Tumblr { return &Tumblr{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating
// inline. Called once at composition; the base constructor stays pool-agnostic.
func (c *Tumblr) WithTokenSource(src TokenSource) *Tumblr { c.tokens = src; return c }

func (c *Tumblr) Website() string { return "TUMBLR" }
func (c *Tumblr) Kind() Kind      { return KindEmail }

var tumblrAPITokenRe = regexp.MustCompile(`"API_TOKEN":"(.*?)",`)

// GenerateToken performs step 1: GET the trending page WITHOUT a proxy
// (Python proxy=None) and regex the API_TOKEN out of the HTML. Identifier-
// agnostic, so poolable. No cookie is needed (step 2 authenticates with a
// Bearer header).
func (c *Tumblr) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	const trendingURL = "https://www.tumblr.com/explore/trending"
	// Python fetches the auth token WITHOUT a proxy (proxy=None); mirror that.
	tokenClient := newHTTPClient(nil, c.timeout)
	status, tokBody, err := doRequest(ctx, tokenClient, "GET", trendingURL, nil, map[string]string{
		"authority":  "www.tumblr.com",
		"accept":     "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"user-agent": randomUA(), "upgrade-insecure-requests": "1",
	})
	if err != nil {
		return nil, fmt.Errorf("tumblr token: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("tumblr: token page status %d", status)
	}
	m := tumblrAPITokenRe.FindSubmatch(tokBody)
	if len(m) != 2 || len(m[1]) == 0 {
		return nil, fmt.Errorf("tumblr: API_TOKEN not found")
	}
	return map[string]string{"api_token": string(m[1])}, nil
}

// CheckWithToken performs step 2 for identifier using the Bearer api_token. Uses
// the proxy as normal.
func (c *Tumblr) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	const loginURL = "https://www.tumblr.com/api/v2/login/mode"
	authToken := token["api_token"]
	if authToken == "" {
		return false, fmt.Errorf("tumblr: empty token")
	}
	// step 2 uses the proxy as normal.
	loginClient := newHTTPClient(proxyURL, c.timeout)
	body, _ := json.Marshal(map[string]any{"email": identifier, "authentication": "oauth2_cookie"})
	lstatus, respBody, err := doRequest(ctx, loginClient, "POST", loginURL, strings.NewReader(string(body)), map[string]string{
		"authority": "www.tumblr.com", "accept": "application/json;format=camelcase",
		"authorization": "Bearer " + authToken, "content-type": "application/json; charset=utf8",
		"origin": "https://www.tumblr.com", "user-agent": randomUA(),
		"x-ad-blocker-enabled": "0", "x-version": "redpop/3/0//redpop/",
	})
	if err != nil {
		return false, err
	}
	if lstatus != 200 {
		return false, fmt.Errorf("tumblr: no condition matched (status=%d)", lstatus)
	}
	var p struct {
		Response struct {
			Mode string `json:"mode"`
		} `json:"response"`
	}
	if json.Unmarshal(respBody, &p) != nil {
		return false, fmt.Errorf("tumblr decode failed")
	}
	if strings.Contains(p.Response.Mode, "password_reset") {
		return true, nil
	}
	if strings.Contains(p.Response.Mode, "registration") {
		return false, nil
	}
	return false, fmt.Errorf("tumblr: no condition matched (mode=%q)", p.Response.Mode)
}

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *Tumblr) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- QUORA (email) ---
// step1 GET / (uTLS) -> cookie + 4 tokens string-scraped from HTML (formkey,
// revision, window_id, broadcastId). step2 POST the GraphQL query with those in
// quora-* headers. data.loginInfoPreview.success bool.
//
// FIDELITY NOTE: Python impersonates SAFARI here (special-cased); go-you only has
// a Chrome uTLS fingerprint, so this uses TLSChrome. If Quora TLS-gates on the
// Safari hello, this may fail where Python passes — a known divergence to revisit
// if a Safari uTLS mode is added.
// TokenCrawler: the 4 scraped tokens + the home-page cookie are identifier-
// agnostic and poolable. A pooled token runs on a fresh step-2 client, so the
// cookie travels in the token map (via cookieStringFor) rather than a jar.
type Quora struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewQuora(timeout time.Duration) *Quora { return &Quora{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating
// inline. Called once at composition; the base constructor stays pool-agnostic.
func (c *Quora) WithTokenSource(src TokenSource) *Quora { c.tokens = src; return c }

func (c *Quora) Website() string { return "QUORA" }
func (c *Quora) Kind() Kind      { return KindEmail }

// Quora's GraphQL persisted-query hash (quora.py:103), carried verbatim.
const quoraGQLHash = "d99ac500aef54a8162e1f1e40a4ed5fe3df59f89599bdbb2ec9f9838a5e80d02"

const quoraCookieURL = "https://www.quora.com/"

// GenerateToken performs step 1: GET the home page (uTLS Chrome), string-scan
// the 4 quora-* tokens from the HTML, and capture the cookie. All identifier-
// agnostic, so poolable.
func (c *Quora) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
	status, homeBody, _, err := doRequestFull(ctx, client, "GET", quoraCookieURL, nil, map[string]string{
		"authority": "www.quora.com", "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"referer": "https://www.google.com/", "upgrade-insecure-requests": "1", "user-agent": randomUA(),
	})
	if err != nil {
		return nil, fmt.Errorf("quora cookie: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("quora: home status %d", status)
	}
	formkey := quoraScan(homeBody, "formkey")
	revision := quoraScan(homeBody, "revision")
	windowID := quoraScan(homeBody, "window_id")
	broadcastID := quoraScan(homeBody, "broadcastId")
	if formkey == "" || broadcastID == "" {
		return nil, fmt.Errorf("quora: tokens not found")
	}
	return map[string]string{
		"formkey":     formkey,
		"revision":    revision,
		"window_id":   windowID,
		"broadcastId": broadcastID,
		"cookie":      cookieStringFor(client, quoraCookieURL),
	}, nil
}

// CheckWithToken performs step 2 for identifier: POST the GraphQL query with the
// quora-* headers from the token. The cookie is sent as an explicit header so
// the token works whether pooled or freshly generated.
func (c *Quora) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	const loginURL = "https://www.quora.com/graphql/gql_para_POST?q=LoginForm_loginInfoPreview_Query"
	formkey := token["formkey"]
	broadcastID := token["broadcastId"]
	if formkey == "" || broadcastID == "" {
		return false, fmt.Errorf("quora: empty token")
	}
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
	payload, _ := json.Marshal(map[string]any{
		"queryName":  "LoginForm_loginInfoPreview_Query",
		"variables":  map[string]any{"email": identifier},
		"extensions": map[string]any{"hash": quoraGQLHash},
	})
	headers := map[string]string{
		"authority": "www.quora.com", "accept": "*/*", "content-type": "application/json",
		"origin": "https://www.quora.com", "referer": "https://www.quora.com/",
		"quora-broadcast-id": broadcastID, "quora-canary-revision": "false",
		"quora-formkey": formkey, "quora-revision": token["revision"], "quora-window-id": token["window_id"],
		"user-agent": randomUA(),
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["cookie"] = cookie
	}
	lstatus, respBody, _, err := doRequestFull(ctx, client, "POST", loginURL, strings.NewReader(string(payload)), headers)
	if err != nil {
		return false, err
	}
	_ = lstatus
	var p struct {
		Data *struct {
			LoginInfoPreview struct {
				Success *bool `json:"success"`
			} `json:"loginInfoPreview"`
		} `json:"data"`
	}
	if json.Unmarshal(respBody, &p) != nil || p.Data == nil || p.Data.LoginInfoPreview.Success == nil {
		return false, fmt.Errorf("quora: no condition matched")
	}
	return *p.Data.LoginInfoPreview.Success, nil
}

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *Quora) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// quoraScan reproduces Python's extract_tokens: find the key in the body, then
// take the 3rd double-quote-delimited field from that point. For text
// `..."formkey":"abc"...` the substring from "formkey" splits on `"` into
// ["formkey", ":", "abc", ...] and index [2] is the value.
func quoraScan(body []byte, key string) string {
	s := string(body)
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	parts := strings.Split(s[i:], `"`)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// --- CODECADEMY (email) ---
// step1a GET / -> session cookie; step1b GET /register -> CSRF from
// <meta name="csrf-token" content="...">. step2 POST /register/validate with
// cookie + x-csrf-token, body {"user":{"email":id}}. Parse: HTTP 400 +
// errors.email=="is already taken"=>exist; 400+{}=>not; 200+"{}"=>not.
// TokenCrawler: the session cookie + register-page CSRF are identifier-agnostic
// and poolable. A pooled token runs on a fresh step-2 client, so the cookie
// travels in the token map (via cookieStringFor) rather than a jar.
type Codecademy struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewCodecademy(timeout time.Duration) *Codecademy { return &Codecademy{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating
// inline. Called once at composition; the base constructor stays pool-agnostic.
func (c *Codecademy) WithTokenSource(src TokenSource) *Codecademy { c.tokens = src; return c }

func (c *Codecademy) Website() string { return "CODECADEMY" }
func (c *Codecademy) Kind() Kind      { return KindEmail }

const codecademyLoginURL = "https://www.codecademy.com/register/validate"

// matches <meta name="csrf-token" content="VALUE"> (attribute order tolerant).
var codecademyCSRFRe = regexp.MustCompile(`(?is)<meta[^>]*name=["']csrf-token["'][^>]*content=["']([^"']+)["']`)
var codecademyCSRFRe2 = regexp.MustCompile(`(?is)<meta[^>]*content=["']([^"']+)["'][^>]*name=["']csrf-token["']`)

// GenerateToken performs step 1: GET the site (session cookie) then GET the
// register page and scrape the CSRF from its <meta> tag. Both the CSRF and the
// cookie are identifier-agnostic, so poolable.
func (c *Codecademy) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	const sessionURL = "https://www.codecademy.com"
	const csrfURL = "https://www.codecademy.com/register"
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	cookieHeaders := map[string]string{
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.7",
		"accept-language": "en-IN,en;q=0.9", "referer": "https://www.codecademy.com/",
		"upgrade-insecure-requests": "1", "User-Agent": randomUA(),
	}
	// step 1a: session cookie (lands in the jar).
	if _, _, _, err := doRequestFull(ctx, client, "GET", sessionURL, nil, cookieHeaders); err != nil {
		return nil, fmt.Errorf("codecademy session: %w", err)
	}
	// step 1b: CSRF from the register page's <meta> tag.
	_, regBody, _, err := doRequestFull(ctx, client, "GET", csrfURL, nil, cookieHeaders)
	if err != nil {
		return nil, fmt.Errorf("codecademy csrf page: %w", err)
	}
	csrf := ""
	if m := codecademyCSRFRe.FindSubmatch(regBody); len(m) == 2 {
		csrf = string(m[1])
	} else if m := codecademyCSRFRe2.FindSubmatch(regBody); len(m) == 2 {
		csrf = string(m[1])
	}
	if csrf == "" {
		return nil, fmt.Errorf("codecademy: csrf-token meta not found")
	}
	return map[string]string{
		"csrf":   csrf,
		"cookie": cookieStringFor(client, codecademyLoginURL),
	}, nil
}

// CheckWithToken performs step 2 for identifier: POST /register/validate with
// the CSRF + cookie from the token (cookie as an explicit header).
func (c *Codecademy) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	csrf := token["csrf"]
	if csrf == "" {
		return false, fmt.Errorf("codecademy: empty token")
	}
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)
	body, _ := json.Marshal(map[string]any{"user": map[string]any{"email": identifier}})
	headers := map[string]string{
		"accept": "application/json", "content-type": "application/json",
		"origin": "https://www.codecademy.com", "referer": "https://www.codecademy.com/register",
		"x-csrf-token": csrf, "User-Agent": randomUA(),
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["cookie"] = cookie
	}
	status, respBody, _, err := doRequestFull(ctx, client, "POST", codecademyLoginURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	text := strings.TrimSpace(string(respBody))
	if status == 400 {
		var jr map[string]any
		if json.Unmarshal(respBody, &jr) == nil {
			if errs, ok := jr["errors"].(map[string]any); ok {
				if email, ok := errs["email"].(string); ok && email == "is already taken" {
					return true, nil
				}
			}
			if len(jr) == 0 {
				return false, nil
			}
		}
		return false, fmt.Errorf("codecademy: no condition matched (400)")
	}
	if status == 200 && text == "{}" {
		return false, nil
	}
	return false, fmt.Errorf("codecademy: no condition matched (status=%d)", status)
}

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *Codecademy) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}
