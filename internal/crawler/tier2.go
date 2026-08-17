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

// (Apple lives at the bottom of this file — same two-step inline shape as
// Microsoft/Twitter.)

// Tier-2 two-step crawlers ported from hey-you origin/aws-migration (audited
// 2026-08-10). Each fetches a token/cookie inline (step 1), then does the check
// (step 2) — the same stateless two-step shape as the Phase B crawlers (no
// background token pool; the pool is a latency optimization we deliberately skip).

// --- MICROSOFT (phone + email) ---
// crawler/spiders/social/microsoft/microsoft.py (+ microsoft_{phone,email}.py).
// step 1: GET the OAuth2 authorize init_url → harvest the session cookie + 7
// tokens scraped from the HTML (sFT/sCtx/apiCanary/correlationId/sessionId/
// hpgact/hpgid). step 2: POST GetCredentialType with those + the cookie.
// Verdict: IfExistsResult 5 => exist; 0 or 1 => not; else NoConditionMatched.
// Plain TLS (Python uses the default requests client — no impersonation).
// Identifier: email = the address; phone = the international number (+CC).
// Microsoft is a TokenCrawler: its step-1 token (the 7 scraped values + the
// step-1 cookie) is identifier-agnostic and poolable. tokens is the optional
// warm-token source (nil => always generate inline).
type Microsoft struct {
	timeout time.Duration
	kind    Kind
	tokens  TokenSource
}

func NewMicrosoftPhone(timeout time.Duration) *Microsoft {
	return &Microsoft{timeout: timeout, kind: KindPhone}
}
func NewMicrosoftEmail(timeout time.Duration) *Microsoft {
	return &Microsoft{timeout: timeout, kind: KindEmail}
}

// WithTokenSource wires the crawler to consult a token pool before generating
// inline. Called once at composition; the base constructor stays pool-agnostic.
func (c *Microsoft) WithTokenSource(src TokenSource) *Microsoft { c.tokens = src; return c }

func (c *Microsoft) Website() string { return "MICROSOFT" }
func (c *Microsoft) Kind() Kind      { return c.kind }

const microsoftInitURL = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?scope=service::account.microsoft.com::MBI_SSL+openid+profile+offline_access&response_type=code&client_id=81feaced-5ddd-41e7-8bef-3e20a2689bb7&redirect_uri=https:%2F%2Faccount.microsoft.com%2Fauth%2Fcomplete-signin-oauth&client-request-id=b30d487f-5078-4583-94c5-40d45668e9f0&x-client-SKU=MSAL.Desktop&x-client-Ver=4.45.0.0&x-client-CPU=x64&x-client-OS=Windows+Server+2019+Datacenter&prompt=login&client_info=1&msaoauth2=true&lc=2057"

const microsoftLoginURL = "https://login.microsoftonline.com/common/GetCredentialType?mkt=en-GB"

var (
	msFT      = regexp.MustCompile(`"sFT":"(.*?)"`)
	msCtx     = regexp.MustCompile(`"sCtx":"(.*?)"`)
	msCanary  = regexp.MustCompile(`"apiCanary":"(.*?)"`)
	msCorrID  = regexp.MustCompile(`"correlationId":"(.*?)"`)
	msSession = regexp.MustCompile(`"sessionId":"(.*?)"`)
	msHpgact  = regexp.MustCompile(`"hpgact":(.*?),`)
	msHpgid   = regexp.MustCompile(`"hpgid":(.*?),`)
)

// msTokenAttempts is how many times GenerateToken re-fetches the authorize page
// looking for one that carries all 7 session tokens. Microsoft serves two page
// variants at random: a full login page (all 7 tokens) and a smaller account-
// picker interstitial that omits sFT/sCtx. Measured live at ~50/50, so a single
// fetch fails half the time. Retrying makes the pool refill (and the inline
// fallback) reliable: P(all fail) = 0.5^n → 4 attempts ≈ 94%, matching what the
// Python source never did (it single-shots and inherits the ~50% failure).
const msTokenAttempts = 4

// GenerateToken performs step 1: GET the authorize page, scrape the 7 session
// tokens from the HTML, and capture the cookies the page set. All are
// identifier-agnostic, so the pool can hand this token to any lookup. The cookie
// string is carried in the token (not left in a jar) so a pooled token works on
// a fresh step-2 client.
//
// Because Microsoft randomly serves a token-less interstitial ~50% of the time,
// the fetch+scrape is retried up to msTokenAttempts times; the first page that
// yields all 7 tokens wins. Each attempt uses a fresh client+UA (an independent
// coin flip). ctx cancellation aborts the loop.
func (c *Microsoft) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	var lastErr error
	for attempt := 0; attempt < msTokenAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tok, err := c.generateTokenOnce(ctx, proxyURL)
		if err == nil {
			return tok, nil
		}
		lastErr = err
		// Only the token-less-page case is worth retrying; a transport/status
		// error (proxy down, non-200) won't improve by re-fetching immediately.
		if err.Error() != "microsoft: token parsing failed" {
			return nil, err
		}
	}
	return nil, lastErr
}

// generateTokenOnce is a single authorize-page fetch + scrape attempt.
func (c *Microsoft) generateTokenOnce(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault) // Python: default requests, no impersonation

	// step 1: authorize page → cookie (in jar) + tokens from HTML.
	st1, body1, _, err := doRequestFull(ctx, client, "GET", microsoftInitURL, nil, map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Host":   "login.microsoftonline.com", "Referer": "https://account.microsoft.com/",
		"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "none",
		"Sec-Fetch-User": "?1", "Upgrade-Insecure-Requests": "1", "User-Agent": ua,
	})
	if err != nil {
		return nil, fmt.Errorf("microsoft init: %w", err)
	}
	if st1 != 200 {
		return nil, fmt.Errorf("microsoft: init status %d", st1)
	}
	get := func(re *regexp.Regexp) string {
		if m := re.FindSubmatch(body1); len(m) == 2 {
			return string(m[1])
		}
		return ""
	}
	flowToken, sCtx := get(msFT), get(msCtx)
	apiCanary, corrID := get(msCanary), get(msCorrID)
	sessionID, hpgact, hpgid := get(msSession), get(msHpgact), get(msHpgid)
	if flowToken == "" || sCtx == "" || apiCanary == "" || corrID == "" || sessionID == "" || hpgact == "" || hpgid == "" {
		return nil, fmt.Errorf("microsoft: token parsing failed")
	}
	// Capture any cookie the authorize page set so a pooled token (fresh step-2
	// client, empty jar) still carries it; the inline path relied on the jar.
	cookie := cookieStringFor(client, microsoftLoginURL)
	return map[string]string{
		"flowToken": flowToken,
		"sCtx":      sCtx,
		"apiCanary": apiCanary,
		"corrID":    corrID,
		"sessionID": sessionID,
		"hpgact":    hpgact,
		"hpgid":     hpgid,
		"cookie":    cookie,
	}, nil
}

// CheckWithToken performs step 2 (GetCredentialType) for identifier using the
// given token values. The cookie is sent as an explicit header so the token
// works whether it came from the pool or an inline fetch.
func (c *Microsoft) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	flowToken, sCtx := token["flowToken"], token["sCtx"]
	apiCanary, corrID := token["apiCanary"], token["corrID"]
	sessionID, hpgact, hpgid := token["sessionID"], token["hpgact"], token["hpgid"]
	if flowToken == "" || sCtx == "" || apiCanary == "" || corrID == "" || sessionID == "" || hpgact == "" || hpgid == "" {
		return false, fmt.Errorf("microsoft: empty token")
	}
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	// step 2: GetCredentialType. Body built to match Python byte-for-byte
	// (username, originalRequest=sCtx, flowToken, country IN).
	payload, _ := json.Marshal(map[string]any{
		"username":                       identifier,
		"isOtherIdpSupported":            true,
		"checkPhones":                    false,
		"isRemoteNGCSupported":           true,
		"isCookieBannerShown":            false,
		"isFidoSupported":                true,
		"originalRequest":                sCtx,
		"country":                        "IN",
		"forceotclogin":                  false,
		"isExternalFederationDisallowed": false,
		"isRemoteConnectSupported":       false,
		"federationFlags":                0,
		"isSignup":                       false,
		"flowToken":                      flowToken,
		"isAccessPassSupported":          true,
	})
	headers := map[string]string{
		"Accept": "application/json", "Content-type": "application/json; charset=UTF-8",
		"Origin": "https://login.microsoftonline.com", "Referer": microsoftInitURL,
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
		"User-Agent": ua, "canary": apiCanary, "client-request-id": corrID,
		"hpgact": hpgact, "hpgid": hpgid, "hpgrequestid": sessionID,
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["Cookie"] = cookie
	}
	st2, body2, _, err := doRequestFull(ctx, client, "POST", microsoftLoginURL, strings.NewReader(string(payload)), headers)
	if err != nil {
		return false, err
	}
	if st2 != 200 {
		return false, fmt.Errorf("microsoft: no condition matched (status=%d)", st2)
	}
	var parsed struct {
		IfExistsResult *int `json:"IfExistsResult"`
	}
	if err := json.Unmarshal(body2, &parsed); err != nil || parsed.IfExistsResult == nil {
		return false, fmt.Errorf("microsoft: no condition matched")
	}
	switch *parsed.IfExistsResult {
	case 5:
		return true, nil
	case 0, 1:
		return false, nil
	default:
		return false, fmt.Errorf("microsoft: no condition matched (IfExistsResult=%d)", *parsed.IfExistsResult)
	}
}

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *Microsoft) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- TWITTER (phone) ---
// crawler/spiders/social/twitter/twitter.py + twitter_phone.py.
// step 1: GET begin_password_reset → cookie (jar) + authenticity_token scraped
// from the HTML. step 2: POST begin_password_reset with
// authenticity_token=<tok>&account_identifier=<international_number>.
// curl_cffi => Chrome uTLS (TLSChrome). Verdict on 200:
//
//	"We couldn't find your account with that information"  => not-exist
//	"JavaScript is not available."                          => captcha (error)
//	"You've exceeded the number of attempts..."             => too-many (error)
//	any user_found_text                                     => exist
//	else => NoConditionMatched. non-200 => NotDesiredStatusCode.
//
// (go-you already has TWITTER *email* — the token-free email_available.json
// crawler in simple_email.go. This is the phone flow, which needs the two-step.)
// TwitterPhone is a TokenCrawler: its step-1 token (authenticity_token + the
// step-1 cookies) is identifier-agnostic and poolable. tokens is the optional
// warm-token source (nil => always generate inline).
type TwitterPhone struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewTwitterPhone(timeout time.Duration) *TwitterPhone { return &TwitterPhone{timeout: timeout} }

// WithTokenSource returns the crawler wired to consult a token pool before
// generating inline. Called once at composition; the base constructor stays
// pool-agnostic so tests and the stateless path need no pool.
func (c *TwitterPhone) WithTokenSource(src TokenSource) *TwitterPhone { c.tokens = src; return c }

func (c *TwitterPhone) Website() string { return "TWITTER" }
func (c *TwitterPhone) Kind() Kind      { return KindPhone }

const twitterResetURL = "https://twitter.com/account/begin_password_reset"

var twitterAuthTokenRe = regexp.MustCompile(`name="authenticity_token" value="(.*?)"`)

// GenerateToken performs step 1: GET the reset page, scrape the
// authenticity_token, and capture the cookies the page set. Both are
// identifier-agnostic, so the pool can hand this token to any lookup. The cookie
// string is carried in the token (not left in a jar) so a pooled token works on
// a fresh step-2 client — matching Python threading the cookie through by hand.
func (c *TwitterPhone) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome) // Python: curl_cffi impersonate=chrome
	st1, body1, _, err := doRequestFull(ctx, client, "GET", twitterResetURL, nil, map[string]string{
		"authority":       "twitter.com",
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "en-GB,en;q=0.9", "sec-fetch-dest": "document",
		"sec-fetch-mode": "navigate", "sec-fetch-site": "none", "sec-fetch-user": "?1",
		"upgrade-insecure-requests": "1", "user-agent": randomUA(),
	})
	if err != nil {
		return nil, fmt.Errorf("twitter cookie: %w", err)
	}
	if st1 != 200 {
		return nil, fmt.Errorf("twitter: cookie page status %d", st1)
	}
	m := twitterAuthTokenRe.FindSubmatch(body1)
	if len(m) != 2 || len(m[1]) == 0 {
		return nil, fmt.Errorf("twitter: authenticity_token not found")
	}
	// Python strips the transient "fm=0; " marker cookie before reuse.
	cookie := strings.Replace(cookieStringFor(client, twitterResetURL), "fm=0; ", "", 1)
	return map[string]string{
		"authenticity_token": string(m[1]),
		"cookie":             cookie,
	}, nil
}

// CheckWithToken performs step 2 for identifier using the given token
// (authenticity_token + cookie). The cookie is sent as an explicit header so the
// token works whether it came from the pool or an inline fetch.
func (c *TwitterPhone) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	authToken := token["authenticity_token"]
	if authToken == "" {
		return false, fmt.Errorf("twitter: empty token")
	}
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome)
	form := "authenticity_token=" + url.QueryEscape(authToken) +
		"&account_identifier=" + url.QueryEscape(internationalNumber(identifier))
	headers := map[string]string{
		"authority":       "twitter.com",
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "en-GB,en;q=0.9", "cache-control": "max-age=0",
		"content-type": "application/x-www-form-urlencoded", "origin": "https://twitter.com",
		"referer": twitterResetURL, "sec-fetch-dest": "document", "sec-fetch-mode": "navigate",
		"sec-fetch-site": "same-origin", "sec-fetch-user": "?1", "upgrade-insecure-requests": "1",
		"user-agent": randomUA(),
	}
	if cookie := token["cookie"]; cookie != "" {
		headers["cookie"] = cookie
	}
	st2, body2, _, err := doRequestFull(ctx, client, "POST", twitterResetURL, strings.NewReader(form), headers)
	if err != nil {
		return false, err
	}
	if st2 != 200 {
		return false, fmt.Errorf("twitter: no condition matched (status=%d)", st2)
	}
	text := string(body2)
	if strings.Contains(text, "We couldn't find your account with that information") {
		return false, nil
	}
	if strings.Contains(text, "JavaScript is not available.") {
		return false, fmt.Errorf("twitter: captcha")
	}
	if strings.Contains(text, "You've exceeded the number of attempts") {
		return false, fmt.Errorf("twitter: too many attempts")
	}
	userFound := []string{
		"Text a code to the phone number ending in",
		"We found more than one account with that telephone number",
		"How do you want to reset your password?",
		"I don't have access to this information",
		"Enter your username to continue",
	}
	for _, s := range userFound {
		if strings.Contains(text, s) {
			return true, nil
		}
	}
	return false, fmt.Errorf("twitter: no condition matched (status=%d)", st2)
}

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *TwitterPhone) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}

// --- APPLE (email) ---
// crawler/spiders/softwares/apple/apple.py + apple_email.py. Two-step inline
// (the background token pool is a latency optimization we skip, same as the
// other Tier-2 crawlers). Plain TLS — Python uses the default requests client,
// NOT curl_cffi.
//
// step 1: GET https://account.apple.com/account → from the response read the
// `aidsp` cookie (Set-Cookie) and the `scnt` header. Build the cookie string
// exactly as parse_cookie does; session_id = aidsp.
// step 2: POST https://appleid.apple.com/account/validation/appleid with body
// {"emailAddress": <email>} and headers X-Apple-ID-Session-Id=aidsp, scnt,
// Cookie, plus the hardcoded X-Apple-I-FD-Client-Info device blob (carried
// verbatim from Python — Apple's anti-bot fingerprint; a rot risk, not a bug).
//
// Verdict: 403 => captcha (error, not a false verdict); 200 + used==true =>
// exist; used==false OR appleOwnedDomain==true => not-exist; else NoConditionMatched.
// AppleEmail is a TokenCrawler: its step-1 token (aidsp session id + scnt + the
// built cookie string) is identifier-agnostic and poolable. tokens is the
// optional warm-token source (nil => always generate inline).
type AppleEmail struct {
	timeout time.Duration
	tokens  TokenSource
}

func NewAppleEmail(timeout time.Duration) *AppleEmail { return &AppleEmail{timeout: timeout} }

// WithTokenSource wires the crawler to consult a token pool before generating
// inline. Called once at composition; the base constructor stays pool-agnostic.
func (c *AppleEmail) WithTokenSource(src TokenSource) *AppleEmail { c.tokens = src; return c }

func (c *AppleEmail) Website() string { return "APPLE" }
func (c *AppleEmail) Kind() Kind      { return KindEmail }

const (
	appleAccountURL    = "https://account.apple.com/account"
	appleValidationURL = "https://appleid.apple.com/account/validation/appleid"
	// appleFDClientInfo is the X-Apple-I-FD-Client-Info device-fingerprint blob,
	// carried verbatim from apple.py's login headers.
	appleFDClientInfo = `{"U":"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36","L":"en-IN","Z":"GMT+05:30","V":"1.1","F":"7ta44j1e3NlY5BNlY5BSmHACVZXnNA9ZdcFHmWumeiQBpsOIcF69LarTcfx9MsFY5CCw1JgN3dN9ZsdI_Fe2iwjOyZ2wcGY5BNlYJNNlY5QB4bVNjMk.7HR"}`
	appleUA           = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var appleAidspRe = regexp.MustCompile(`aidsp=(.*?);`)

// GenerateToken performs step 1: GET the bootstrap page, read the aidsp cookie
// (Set-Cookie) and the scnt header, and build the cookie string exactly as
// parse_cookie does. All are identifier-agnostic, so the pool can hand this
// token to any lookup; the cookie string is carried in the token so a pooled
// token works on a fresh step-2 client.
func (c *AppleEmail) GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error) {
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault) // Python: default requests, no impersonation

	// step 1: bootstrap → aidsp cookie + scnt header.
	st1, _, hdr1, err := doRequestFull(ctx, client, "GET", appleAccountURL, nil, map[string]string{
		"Accept":          "application/json, text/plain, */*",
		"Accept-Language": "en-IN,en;q=0.9", "Content-Type": "application/json",
		"Cookie": "dslang=IN-EN; site=IND; geo=IN", "Host": "appleid.apple.com",
		"Origin": "https://account.apple.com", "Referer": "https://account.apple.com/",
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-site",
		"User-Agent": appleUA, "X-Apple-I-FD-Client-Info": appleFDClientInfo,
		"X-Apple-I-Request-Context": "ca", "X-Apple-I-TimeZone": "Asia/Calcutta",
		"sec-ch-ua-mobile": "?0",
	})
	if err != nil {
		return nil, fmt.Errorf("apple cookie: %w", err)
	}
	if st1 == 403 {
		return nil, fmt.Errorf("apple: captcha (cookie step 403)")
	}
	// aidsp comes from Set-Cookie; scnt from a response header.
	setCookie := strings.Join(hdr1.Values("Set-Cookie"), "; ")
	m := appleAidspRe.FindStringSubmatch(setCookie)
	if len(m) != 2 || m[1] == "" {
		return nil, fmt.Errorf("apple: aidsp cookie not found")
	}
	aidsp := m[1]
	scnt := hdr1.Get("scnt")
	if scnt == "" {
		return nil, fmt.Errorf("apple: scnt header not found")
	}
	// parse_cookie builds this exact string.
	cookie := "dslang=IN-EN; site=IND; geo=IN; idclient=web; aidsp=" + aidsp
	return map[string]string{
		"aidsp":  aidsp,
		"scnt":   scnt,
		"cookie": cookie,
	}, nil
}

// CheckWithToken performs step 2 (validation/appleid) for identifier using the
// given token (aidsp + scnt + cookie). The cookie is sent as an explicit header
// so the token works whether it came from the pool or an inline fetch.
func (c *AppleEmail) CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error) {
	aidsp, scnt, cookie := token["aidsp"], token["scnt"], token["cookie"]
	if aidsp == "" || scnt == "" || cookie == "" {
		return false, fmt.Errorf("apple: empty token")
	}
	client := newHTTPClientJar(proxyURL, c.timeout, TLSDefault)

	// step 2: existence check.
	payload, _ := json.Marshal(map[string]any{"emailAddress": identifier})
	st2, body2, _, err := doRequestFull(ctx, client, "POST", appleValidationURL, strings.NewReader(string(payload)), map[string]string{
		"Accept": "application/json, text/plain, */*", "Accept-Language": "en-IN,en;q=0.9",
		"Content-Type": "application/json", "Cookie": cookie, "Host": "appleid.apple.com",
		"Origin": "https://appleid.apple.com", "Referer": "https://appleid.apple.com/",
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
		"User-Agent": appleUA, "X-Apple-I-FD-Client-Info": appleFDClientInfo,
		"X-Apple-I-TimeZone": "Asia/Calcutta", "X-Apple-ID-Session-Id": aidsp,
		"X-Apple-Request-Context": "create", "scnt": scnt, "sec-ch-ua-mobile": "?0",
	})
	if err != nil {
		return false, err
	}
	if st2 == 403 {
		return false, fmt.Errorf("apple: captcha (validation 403)")
	}
	if st2 != 200 {
		return false, fmt.Errorf("apple: no condition matched (status=%d)", st2)
	}
	var parsed struct {
		Used             *bool `json:"used"`
		AppleOwnedDomain *bool `json:"appleOwnedDomain"`
	}
	if err := json.Unmarshal(body2, &parsed); err != nil {
		return false, fmt.Errorf("apple decode: %w", err)
	}
	switch {
	case parsed.Used != nil && *parsed.Used:
		return true, nil
	case parsed.Used != nil && !*parsed.Used:
		return false, nil
	case parsed.AppleOwnedDomain != nil && *parsed.AppleOwnedDomain:
		return false, nil
	default:
		return false, fmt.Errorf("apple: no condition matched")
	}
}

// Check is the standalone existence probe: get a token (pooled or inline) then
// run step 2 — the faithful get_or_generate_token flow.
func (c *AppleEmail) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.tokens, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, proxyURL)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, identifier, token, proxyURL)
}
