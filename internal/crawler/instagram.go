package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Instagram ports crawler/spiders/social/instagram/instagram.py (InstagramBase).
//
// Python flow: POST the login ajax form with the target as `username` and a
// junk enc_password, then read error_type. "UserInvalidCredentials" (on 200 or
// 400) => the user exists (right username, wrong password); a 200 with no
// error_type => user does not exist; 429 or anything else => NoConditionMatched.
//
// IMPORTANT: the Python spider uses curl_cffi (Chrome TLS impersonation). Go's
// net/http presents a Go TLS fingerprint and Instagram is the crawler most
// likely to block or 429 as a result. If this crawler fails in the POC where
// Flipkart succeeds, that isolates the TLS-fingerprint gap — swap newHTTPClient
// for a utls-based client here.
type Instagram struct {
	timeout time.Duration
}

func NewInstagram(timeout time.Duration) *Instagram { return &Instagram{timeout: timeout} }

func (i *Instagram) Website() string { return "INSTAGRAM" }
func (i *Instagram) Kind() Kind      { return KindPhone }

const instagramLoginURL = "https://www.instagram.com/api/v1/web/accounts/login/ajax/"

// Hardcoded browser session values, ported verbatim from the Python config.
// These are baked into the Python spider too (not a rotating token pool).
var instagramHeaders = map[string]string{
	"accept":           "*/*",
	"accept-language":  "en-GB,en-US;q=0.9,en;q=0.8",
	"content-type":     "application/x-www-form-urlencoded",
	"origin":           "https://www.instagram.com",
	"referer":          "https://www.instagram.com/",
	"user-agent":       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
	"x-asbd-id":        "359341",
	"x-csrftoken":      "tjYUFuOt250XOOvYnmvIHvvlQyHkJW4W",
	"x-ig-app-id":      "936619743392459",
	"x-ig-www-claim":   "0",
	"x-instagram-ajax": "1032036386",
	"x-requested-with": "XMLHttpRequest",
	"x-web-session-id": "avk48o:g8gecw:cexgoe",
}

func (i *Instagram) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	form := url.Values{}
	form.Set("enc_password", "#PWD_INSTAGRAM_BROWSER:0:0:garbage")
	form.Set("username", identifier)
	form.Set("optIntoOneTap", "false")
	form.Set("loginAttemptSubmissionCount", "1")
	form.Set("queryParams", "{}")
	form.Set("trustedDeviceRecords", "{}")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instagramLoginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	for k, v := range instagramHeaders {
		req.Header.Set(k, v)
	}

	client := newHTTPClient(proxyURL, i.timeout)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return false, fmt.Errorf("instagram 429 (likely TLS-fingerprint block)")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("instagram decode (status %d): %w", resp.StatusCode, err)
	}
	errType, hasErrType := parsed["error_type"].(string)

	switch {
	case (resp.StatusCode == 200 || resp.StatusCode == 400) && hasErrType && errType == "UserInvalidCredentials":
		return true, nil
	case resp.StatusCode == 200 && !hasErrType:
		return false, nil
	default:
		return false, fmt.Errorf("instagram: no condition matched (status %d)", resp.StatusCode)
	}
}
