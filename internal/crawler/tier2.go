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
type Microsoft struct {
	timeout time.Duration
	kind    Kind
}

func NewMicrosoftPhone(timeout time.Duration) *Microsoft {
	return &Microsoft{timeout: timeout, kind: KindPhone}
}
func NewMicrosoftEmail(timeout time.Duration) *Microsoft {
	return &Microsoft{timeout: timeout, kind: KindEmail}
}
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

func (c *Microsoft) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
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
		return false, fmt.Errorf("microsoft init: %w", err)
	}
	if st1 != 200 {
		return false, fmt.Errorf("microsoft: init status %d", st1)
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
		return false, fmt.Errorf("microsoft: token parsing failed")
	}

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
	st2, body2, _, err := doRequestFull(ctx, client, "POST", microsoftLoginURL, strings.NewReader(string(payload)), map[string]string{
		"Accept": "application/json", "Content-type": "application/json; charset=UTF-8",
		"Origin": "https://login.microsoftonline.com", "Referer": microsoftInitURL,
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
		"User-Agent": ua, "canary": apiCanary, "client-request-id": corrID,
		"hpgact": hpgact, "hpgid": hpgid, "hpgrequestid": sessionID,
	})
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
type TwitterPhone struct{ timeout time.Duration }

func NewTwitterPhone(timeout time.Duration) *TwitterPhone { return &TwitterPhone{timeout: timeout} }
func (c *TwitterPhone) Website() string                   { return "TWITTER" }
func (c *TwitterPhone) Kind() Kind                        { return KindPhone }

const twitterResetURL = "https://twitter.com/account/begin_password_reset"

var twitterAuthTokenRe = regexp.MustCompile(`name="authenticity_token" value="(.*?)"`)

func (c *TwitterPhone) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	ua := randomUA()
	client := newHTTPClientJar(proxyURL, c.timeout, TLSChrome) // Python: curl_cffi impersonate=chrome

	// step 1: GET the reset page → cookie lands in the jar; scrape the CSRF token.
	st1, body1, _, err := doRequestFull(ctx, client, "GET", twitterResetURL, nil, map[string]string{
		"authority":       "twitter.com",
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "en-GB,en;q=0.9", "sec-fetch-dest": "document",
		"sec-fetch-mode": "navigate", "sec-fetch-site": "none", "sec-fetch-user": "?1",
		"upgrade-insecure-requests": "1", "user-agent": ua,
	})
	if err != nil {
		return false, fmt.Errorf("twitter cookie: %w", err)
	}
	if st1 != 200 {
		return false, fmt.Errorf("twitter: cookie page status %d", st1)
	}
	m := twitterAuthTokenRe.FindSubmatch(body1)
	if len(m) != 2 || len(m[1]) == 0 {
		return false, fmt.Errorf("twitter: authenticity_token not found")
	}
	authToken := string(m[1])

	// step 2: POST with the CSRF + the international number. The jar replays the
	// step-1 cookies automatically (same origin), matching Python's cookie reuse.
	form := "authenticity_token=" + url.QueryEscape(authToken) +
		"&account_identifier=" + url.QueryEscape(internationalNumber(identifier))
	st2, body2, _, err := doRequestFull(ctx, client, "POST", twitterResetURL, strings.NewReader(form), map[string]string{
		"authority":       "twitter.com",
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "en-GB,en;q=0.9", "cache-control": "max-age=0",
		"content-type": "application/x-www-form-urlencoded", "origin": "https://twitter.com",
		"referer": twitterResetURL, "sec-fetch-dest": "document", "sec-fetch-mode": "navigate",
		"sec-fetch-site": "same-origin", "sec-fetch-user": "?1", "upgrade-insecure-requests": "1",
		"user-agent": ua,
	})
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
