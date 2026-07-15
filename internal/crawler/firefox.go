package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Firefox ports crawler/spiders/softwares/firefox/firefox.py (email).
//
// POST account/status with {"email":<email>,"thirdPartyAuthStatus":true}.
// json.exists==true => exists; ==false => not-exists; else no-match error.
type Firefox struct{ timeout time.Duration }

func NewFirefox(timeout time.Duration) *Firefox { return &Firefox{timeout: timeout} }

func (c *Firefox) Website() string { return "FIREFOX" }
func (c *Firefox) Kind() Kind      { return KindEmail }

const firefoxURL = "https://api.accounts.firefox.com/v1/account/status"

func (c *Firefox) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"email": identifier, "thirdPartyAuthStatus": true})
	headers := map[string]string{
		"sec-ch-ua-platform": `"Android"`,
		"User-Agent":         "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
		"sec-ch-ua":          `"Google Chrome";v="129", "Not=A?Brand";v="8", "Chromium";v="129"`,
		"content-type":       "application/json", "sec-ch-ua-mobile": "?1", "Accept": "*/*",
		"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", firefoxURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("firefox status %d", status)
	}
	var parsed struct {
		Exists *bool `json:"exists"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("firefox decode: %w", err)
	}
	if parsed.Exists == nil {
		return false, fmt.Errorf("firefox: no condition matched")
	}
	return *parsed.Exists, nil
}
