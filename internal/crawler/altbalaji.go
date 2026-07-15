package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Altbalaji ports crawler/spiders/social/altbalaji/altbalaji.py.
//
// POST user/lookup with a body keyed by the crawler type: {"phone":<national>}
// or {"email":<email>}. For phone: error=="Invalid Phone Number" => not-exists,
// result=="Valid Phone" => exists. For email: error=="Invalid Email ID" =>
// not-exists, result=="Valid Email" => exists. Else no-match error.
type Altbalaji struct {
	kind    Kind
	timeout time.Duration
}

func NewAltbalajiPhone(timeout time.Duration) *Altbalaji {
	return &Altbalaji{kind: KindPhone, timeout: timeout}
}
func NewAltbalajiEmail(timeout time.Duration) *Altbalaji {
	return &Altbalaji{kind: KindEmail, timeout: timeout}
}

func (c *Altbalaji) Website() string { return "ALTBALAJI" }
func (c *Altbalaji) Kind() Kind      { return c.kind }

const altbalajiURL = "https://api.altt.studio/automatorapi/v10/user/lookup"

func (c *Altbalaji) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	var body []byte
	var notExistErr, existResult string
	if c.kind == KindPhone {
		body, _ = json.Marshal(map[string]any{"phone": nationalNumber(identifier)})
		notExistErr, existResult = "Invalid Phone Number", "Valid Phone"
	} else {
		body, _ = json.Marshal(map[string]any{"email": identifier})
		notExistErr, existResult = "Invalid Email ID", "Valid Email"
	}
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-IN,en;q=0.9",
		"origin": "https://altt.co.in", "priority": "u=1, i", "referer": "https://altt.co.in/",
		"sec-ch-ua":        `"Google Chrome";v="129", "Not=A?Brand";v="8", "Chromium";v="129"`,
		"sec-ch-ua-mobile": "?1", "sec-ch-ua-platform": `"Android"`,
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "cross-site",
		"user-agent": "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", altbalajiURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("altbalaji status %d", status)
	}
	var parsed struct {
		Error  string `json:"error"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("altbalaji decode: %w", err)
	}
	// Order matches Python: not-exist (error) checked before exist (result).
	switch {
	case parsed.Error == notExistErr:
		return false, nil
	case parsed.Result == existResult:
		return true, nil
	default:
		return false, fmt.Errorf("altbalaji: no condition matched")
	}
}
