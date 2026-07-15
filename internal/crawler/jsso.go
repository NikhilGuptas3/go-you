package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// jssoURL is the shared Times Internet SSO endpoint used by GAANA, TOI, and
// TIMES_PRIME (crawler/spiders/**/gaana|toi|times_prime). They differ only in
// the "channel"/origin/referer headers and the expected data.status values.
// All use curl_cffi in Python => TLSChrome.
const jssoURL = "https://jsso.indiatimes.com/sso/crossapp/identity/web/checkUserExists"

// jssoCheck posts {"identifier":<id>} and reads data.status. existStatus and
// notExistStatus are the site-specific expected values (e.g. VERIFIED_MOBILE /
// UNREGISTERED_MOBILE, or the _EMAIL variants). extraNotExist is an optional
// second not-exist value (TOI treats UNVERIFIED_MOBILE as not-exist too).
func jssoCheck(
	ctx context.Context, identifier string, proxyURL *url.URL, timeout time.Duration,
	headers map[string]string, existStatus, notExistStatus, extraNotExist string,
) (bool, error) {
	body, _ := json.Marshal(map[string]any{"identifier": identifier})
	client := newHTTPClientTLS(proxyURL, timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "POST", jssoURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("jsso status %d", status)
	}
	var parsed struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("jsso decode: %w", err)
	}
	switch parsed.Data.Status {
	case existStatus:
		return true, nil
	case notExistStatus:
		return false, nil
	case extraNotExist:
		if extraNotExist != "" {
			return false, nil
		}
		return false, fmt.Errorf("jsso: no condition matched (status=%q)", parsed.Data.Status)
	default:
		return false, fmt.Errorf("jsso: no condition matched (status=%q)", parsed.Data.Status)
	}
}

// --- GAANA (phone + email) ---

func gaanaHeaders() map[string]string {
	return map[string]string{
		"accept": "*/*", "accept-language": "en-IN,en;q=0.9", "captchatoken": "",
		"channel": "gaana.com", "content-type": "application/json", "csrftoken": "",
		"csut": "", "gdpr": "", "isjssocrosswalk": "true", "origin": "https://gaana.com",
		"platform": "WEB", "priority": "u=1, i", "referer": "https://gaana.com/",
		"sdkversion": "0.7.3", "sec-ch-ua": `"Google Chrome";v="129", "Not=A?Brand";v="8", "Chromium";v="129"`,
		"sec-ch-ua-mobile": "?1", "sec-ch-ua-platform": `"Android"`, "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "cross-site", "ssec": "", "tksec": "",
		"user-agent": "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
	}
}

type GaanaPhone struct{ timeout time.Duration }

func NewGaanaPhone(timeout time.Duration) *GaanaPhone { return &GaanaPhone{timeout: timeout} }
func (c *GaanaPhone) Website() string                 { return "GAANA" }
func (c *GaanaPhone) Kind() Kind                      { return KindPhone }
func (c *GaanaPhone) Check(ctx context.Context, id string, p *url.URL) (bool, error) {
	return jssoCheck(ctx, nationalNumber(id), p, c.timeout, gaanaHeaders(), "VERIFIED_MOBILE", "UNREGISTERED_MOBILE", "")
}

type GaanaEmail struct{ timeout time.Duration }

func NewGaanaEmail(timeout time.Duration) *GaanaEmail { return &GaanaEmail{timeout: timeout} }
func (c *GaanaEmail) Website() string                 { return "GAANA" }
func (c *GaanaEmail) Kind() Kind                      { return KindEmail }
func (c *GaanaEmail) Check(ctx context.Context, id string, p *url.URL) (bool, error) {
	return jssoCheck(ctx, id, p, c.timeout, gaanaHeaders(), "VERIFIED_EMAIL", "UNREGISTERED_EMAIL", "")
}

// --- TOI (phone) --- international id; UNVERIFIED_MOBILE also => not-exist.

type ToiPhone struct{ timeout time.Duration }

func NewToiPhone(timeout time.Duration) *ToiPhone { return &ToiPhone{timeout: timeout} }
func (c *ToiPhone) Website() string               { return "TOI" }
func (c *ToiPhone) Kind() Kind                    { return KindPhone }
func (c *ToiPhone) Check(ctx context.Context, id string, p *url.URL) (bool, error) {
	headers := map[string]string{
		"authority": "jsso.indiatimes.com", "accept": "*/*", "accept-language": "en-GB,en;q=0.9",
		"channel": "toi", "content-type": "application/json", "csrftoken": "", "csut": "",
		"gdpr": "", "isjssocrosswalk": "true", "origin": "https://timesofindia.indiatimes.com",
		"platform": "web", "referer": "https://timesofindia.indiatimes.com/", "sdkversion": "0.6.22",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-site",
		"ssec": "", "tksec": "", "user-agent": randomUA(),
	}
	return jssoCheck(ctx, internationalNumber(id), p, c.timeout, headers, "VERIFIED_MOBILE", "UNREGISTERED_MOBILE", "UNVERIFIED_MOBILE")
}

// --- TIMES_PRIME (phone) --- national id.

type TimesPrimePhone struct{ timeout time.Duration }

func NewTimesPrimePhone(timeout time.Duration) *TimesPrimePhone {
	return &TimesPrimePhone{timeout: timeout}
}
func (c *TimesPrimePhone) Website() string { return "TIMES_PRIME" }
func (c *TimesPrimePhone) Kind() Kind      { return KindPhone }
func (c *TimesPrimePhone) Check(ctx context.Context, id string, p *url.URL) (bool, error) {
	headers := map[string]string{
		"authority": "jsso.indiatimes.com", "accept": "*/*",
		"accept-language": "en-IN,en-GB;q=0.9,en-US;q=0.8,en;q=0.7", "captchatoken": "",
		"channel": "timesprime", "content-type": "application/json", "csrftoken": "", "csut": "",
		"gdpr": "", "isjssocrosswalk": "true", "origin": "https://www.timesprime.com",
		"platform": "WEB", "referer": "https://www.timesprime.com/", "sdkversion": "0.7.2",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "cross-site",
		"ssec": "", "tksec": "", "user-agent": randomUA(),
	}
	return jssoCheck(ctx, nationalNumber(id), p, c.timeout, headers, "VERIFIED_MOBILE", "UNREGISTERED_MOBILE", "")
}
