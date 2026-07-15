package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Yatra ports crawler/spiders/travel/yatra/yatra.py (phone). POST mobileStatus
// (form) isdCode=91&mobileNumber=<national>. mobileStatus "missing_mobile_number"
// => not-exists; "multiple_users"/"single_unverified" => exists. curl_cffi => TLSChrome.
type Yatra struct{ timeout time.Duration }

func NewYatra(timeout time.Duration) *Yatra { return &Yatra{timeout: timeout} }
func (c *Yatra) Website() string            { return "YATRA" }
func (c *Yatra) Kind() Kind                 { return KindPhone }

const yatraURL = "https://www.yatra.com/social/common/yatra/mobileStatus"

func (c *Yatra) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body := formBody(map[string]string{"isdCode": "91", "mobileNumber": nationalNumber(identifier)})
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-IN,en;q=0.9",
		"content-type": "application/x-www-form-urlencoded; charset=UTF-8", "origin": "https://www.yatra.com",
		"priority": "u=1, i", "referer": "https://www.yatra.com/", "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
	}
	client := newHTTPClientTLS(proxyURL, c.timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "POST", yatraURL, body, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("yatra status %d", status)
	}
	var parsed struct {
		MobileStatus string `json:"mobileStatus"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("yatra decode: %w", err)
	}
	switch parsed.MobileStatus {
	case "missing_mobile_number":
		return false, nil
	case "multiple_users", "single_unverified":
		return true, nil
	default:
		return false, fmt.Errorf("yatra: no condition matched (%q)", parsed.MobileStatus)
	}
}
