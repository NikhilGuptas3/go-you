package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tier-1 stateless single-request crawlers ported from hey-you origin/aws-migration
// (audited 2026-08-10). Each is one HTTP call with a simple verdict — no token
// pool, no external vendor, no captcha solving (captcha is detected and surfaced
// as an error, matching Python's CaptchaException → not a false verdict).

// --- SHAADI (phone) ---
// crawler/spiders/matrimonial/shaadi/shaadi_phone.py: SINGLE GET to the same
// check-if-email-exist endpoint with the national number injected (the path says
// "email" but Python sends the phone number there). httpx (plain TLS). Verdict is
// body-text: "This Mobile No. is not registered with us" => not-exist; "Please
// enter a valid password to Login" => exist. status 500 / else => NoConditionMatched.
type ShaadiPhone struct{ timeout time.Duration }

func NewShaadiPhone(timeout time.Duration) *ShaadiPhone { return &ShaadiPhone{timeout: timeout} }
func (c *ShaadiPhone) Website() string                  { return "SHAADI" }
func (c *ShaadiPhone) Kind() Kind                       { return KindPhone }

func (c *ShaadiPhone) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	national := nationalNumber(identifier)
	u := "https://www.shaadi.com/ajax/check-if-email-exist/email/" + url.PathEscape(national) + "/duplicate/1/frompage/reg1"
	headers := map[string]string{
		"accept":           "application/json, text/javascript, */*",
		"accept-language":  "en-GB,en-US;q=0.9,en;q=0.8",
		"referer":          "https://www.shaadi.com/registration/user?btn=2",
		"sec-ch-ua-mobile": "?1", "sec-fetch-dest": "empty", "sec-fetch-mode": "cors",
		"sec-fetch-site":   "same-origin",
		"user-agent":       "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
		"x-requested-with": "XMLHttpRequest",
	}
	client := newHTTPClient(proxyURL, c.timeout) // httpx in Python => plain TLS
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status == 500 {
		return false, fmt.Errorf("shaadi: no condition matched (status=500)")
	}
	text := string(respBody)
	switch {
	case strings.Contains(text, "This Mobile No. is not registered with us"):
		return false, nil
	case strings.Contains(text, "Please enter a valid password to Login"):
		return true, nil
	default:
		return false, fmt.Errorf("shaadi phone: no condition matched (status=%d)", status)
	}
}

// --- GOOGLE (email) ---
// crawler/spiders/social/google/google_account_check/google_account_check_email.py:
// SINGLE GET mail.google.com/mail/gxlu?email=<email>. HTTP 204 WITH a Set-Cookie
// header => account exists; anything else => not. Plain requests, no proxy in
// Python (we still route via the shared proxy, harmless). The enriched Maps
// profile (needs a GID TokenService) is intentionally NOT ported — existence only.
type GoogleEmail struct{ timeout time.Duration }

func NewGoogleEmail(timeout time.Duration) *GoogleEmail { return &GoogleEmail{timeout: timeout} }
func (c *GoogleEmail) Website() string                  { return "GOOGLE" }
func (c *GoogleEmail) Kind() Kind                       { return KindEmail }

func (c *GoogleEmail) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://mail.google.com/mail/gxlu?email=" + url.QueryEscape(identifier)
	headers := map[string]string{"user-agent": randomUA()}
	client := newHTTPClient(proxyURL, c.timeout)
	// Need the Set-Cookie header, so use doRequestFull (returns response headers).
	status, _, respHeaders, err := doRequestFull(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status == 204 && respHeaders.Get("Set-Cookie") != "" {
		return true, nil
	}
	return false, nil
}

// --- NETFLIX (email) ---
// crawler/spiders/social/netflix/netlix_email.py: the token logic is COMMENTED
// OUT upstream, so this is a single self-contained GraphQL POST with a
// client-generated uuid. data.clcsWebInitSignup.location == "LOGIN" => exists;
// "SIGNUP" => not; else NoConditionMatched. httpx (plain TLS).
type NetflixEmail struct{ timeout time.Duration }

func NewNetflixEmail(timeout time.Duration) *NetflixEmail { return &NetflixEmail{timeout: timeout} }
func (c *NetflixEmail) Website() string                   { return "NETFLIX" }
func (c *NetflixEmail) Kind() Kind                        { return KindEmail }

// netflixPQID / netflixPQVersion are the persisted-query id + version carried
// verbatim from netflix.py (must match the server's registered query).
const (
	netflixPQID      = "216994f2-8e63-4f2c-ba59-fc2e396e906d"
	netflixPQVersion = 102
)

func (c *NetflixEmail) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const u = "https://web.prod.cloud.netflix.com/graphql"
	// inputFields shape is {name, value:{stringValue}} exactly as Python sends;
	// recaptchaToken is empty (no live captcha), recaptchaError is {}.
	payload, _ := json.Marshal(map[string]any{
		"operationName": "CLCSWebInitSignup",
		"variables": map[string]any{
			"inputUserJourneyNode": "WELCOME",
			"locale":               "en-IN",
			"inputFields": []any{
				map[string]any{"name": "flwssn", "value": map[string]any{"stringValue": uuid.NewString()}},
				map[string]any{"name": "email", "value": map[string]any{"stringValue": identifier}},
				map[string]any{"name": "recaptchaError", "value": map[string]any{}},
				map[string]any{"name": "recaptchaResponseTime", "value": map[string]any{"stringValue": "286"}},
				map[string]any{"name": "recaptchaSiteKey", "value": map[string]any{"stringValue": "6LdqW_EqAAAAAO87Fb_kcZfNzs0IqJRcKiJDYpUv"}},
				map[string]any{"name": "recaptchaToken", "value": map[string]any{"stringValue": ""}},
			},
		},
		"extensions": map[string]any{
			"persistedQuery": map[string]any{"id": netflixPQID, "version": netflixPQVersion},
		},
	})
	headers := map[string]string{
		"accept": "*/*", "accept-language": "en-IN,en;q=0.9", "content-type": "application/json",
		"origin": "https://www.netflix.com", "priority": "u=1, i", "referer": "https://www.netflix.com/",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-site",
		"user-agent":                        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
		"x-netflix.context.app-version":     "v7cd1475c",
		"x-netflix.context.hawkins-version": "5.10.0",
		"x-netflix.context.locales":         "en-in",
		"x-netflix.context.operation-name":  "CLCSWebInitSignup",
		"x-netflix.context.ui-flavor":       "akira",
		"x-netflix.request.attempt":         "1",
		"x-netflix.request.clcs.bucket":     "high",
		"x-netflix.request.client.context":  `{"appstate":"foreground"}`,
		"x-netflix.request.id":              strings.ReplaceAll(uuid.NewString(), "-", ""),
		"x-netflix.request.originating.url": "https://www.netflix.com/in/",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", u, strings.NewReader(string(payload)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("netflix status %d", status)
	}
	var parsed struct {
		Data struct {
			ClcsWebInitSignup struct {
				Location string `json:"location"`
			} `json:"clcsWebInitSignup"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("netflix decode: %w", err)
	}
	switch parsed.Data.ClcsWebInitSignup.Location {
	case "LOGIN":
		return true, nil
	case "SIGNUP":
		return false, nil
	default:
		return false, fmt.Errorf("netflix: no condition matched (location=%q)", parsed.Data.ClcsWebInitSignup.Location)
	}
}

// --- FACEBOOK (phone + email) ---
// crawler/spiders/social/facebook/facebook{phone,email}.py: SINGLE GraphQL POST
// with a static doc_id and client-generated uuids; the target goes in
// variables.params.search_query. httpx (plain TLS), no token. Verdict text:
// captcha text => CaptchaException (error, not false); "recover/initiate" or
// non-empty accounts => exist; "no account found" / empty accounts => not.
type Facebook struct {
	timeout time.Duration
	kind    Kind
}

func NewFacebookPhone(timeout time.Duration) *Facebook {
	return &Facebook{timeout: timeout, kind: KindPhone}
}
func NewFacebookEmail(timeout time.Duration) *Facebook {
	return &Facebook{timeout: timeout, kind: KindEmail}
}
func (c *Facebook) Website() string { return "FACEBOOK" }
func (c *Facebook) Kind() Kind      { return c.kind }

const facebookDocID = "25765860633071922"

func (c *Facebook) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	// Phone flow sends the national number; email flow sends the address.
	query := identifier
	if c.kind == KindPhone {
		query = nationalNumber(identifier)
	}
	variables, _ := json.Marshal(map[string]any{
		"params": map[string]any{
			"cipher_text":      nil,
			"context":          "recover",
			"event_request_id": uuid.NewString(),
			"friend_name":      "",
			"search_query":     query,
			"waterfall_id":     uuid.NewString(),
		},
	})
	form := url.Values{}
	form.Set("variables", string(variables))
	form.Set("doc_id", facebookDocID)

	headers := map[string]string{
		"accept": "*/*", "accept-language": "en-GB,en-US;q=0.9,en;q=0.8",
		"content-type": "application/x-www-form-urlencoded", "origin": "https://www.facebook.com",
		"priority": "u=1, i", "referer": "https://www.facebook.com/login/identify/?ctx=recover&from_login_screen=0",
		"sec-ch-ua-platform": `"Android"`, "sec-ch-ua-platform-version": `"6.0"`,
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Mobile Safari/537.36",
	}
	client := newHTTPClient(proxyURL, c.timeout) // httpx in Python => plain TLS
	status, respBody, err := doRequest(ctx, client, "POST", "https://www.facebook.com/api/graphql/", strings.NewReader(form.Encode()), headers)
	if err != nil {
		return false, err
	}
	text := string(respBody)
	// Captcha / rate-limit — surfaced as an error (Python raises CaptchaException),
	// NOT a false verdict, so it lands in the logs + spider_error, never as "not found".
	captchaTexts := []string{
		"There was a problem with this request. We're working on getting it",
		"It looks like you were misusing this feature by going too fast.",
	}
	for _, s := range captchaTexts {
		if strings.Contains(text, s) {
			return false, fmt.Errorf("facebook: captcha/rate-limit")
		}
	}
	if status != 200 {
		return false, fmt.Errorf("facebook: no condition matched (status=%d)", status)
	}
	// exist / not-exist text signals, verbatim from facebook.py parse_login_response.
	existTexts := []string{`\/recover\/initiate\/?ldata=`, "This is my account", "Identify your account"}
	notExistTexts := []string{
		"Your search did not return any results. Please try again with other information.",
		"Please enter your email address or mobile number to search for your account",
		"No account found. Check your mobile number or email address and try again.",
	}
	for _, s := range existTexts {
		if strings.Contains(text, s) {
			return true, nil
		}
	}
	for _, s := range notExistTexts {
		if strings.Contains(text, s) {
			return false, nil
		}
	}
	// accounts array: empty => not-exist, non-empty => exist (Python's final branch).
	var parsed struct {
		Data struct {
			Search struct {
				Accounts []any `json:"accounts"`
			} `json:"caa_ar_fb_account_search"`
		} `json:"data"`
	}
	if json.Unmarshal(respBody, &parsed) == nil {
		return len(parsed.Data.Search.Accounts) > 0, nil
	}
	return false, fmt.Errorf("facebook: no condition matched (status=%d)", status)
}
