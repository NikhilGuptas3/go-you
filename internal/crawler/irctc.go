package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// Irctc ports crawler/spiders/travel/irctc/irctc.py (phone). GET checkUserAvail.
// The "mobileAvailable" flag is INVERTED: "FALSE" => the number is taken =>
// exists; "TRUE" => available => not-exists. curl_cffi => TLSChrome.
type Irctc struct{ timeout time.Duration }

func NewIrctc(timeout time.Duration) *Irctc { return &Irctc{timeout: timeout} }
func (c *Irctc) Website() string            { return "IRCTC" }
func (c *Irctc) Kind() Kind                 { return KindPhone }

func (c *Irctc) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	national := nationalNumber(identifier)
	u := "https://www.irctc.co.in/eticketing/protected/mapps1/checkUserAvail?mobile=" +
		url.QueryEscape(national) + "&isd=91"
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-US,en;q=0.10",
		"bmirak": "webbm", "content-language": "en", "content-type": "application/x-www-form-urlencoded",
		"greq": "GQ:" + uuid.NewString(), "priority": "u=1, i",
		"referer": "https://www.irctc.co.in/nget/profile/user-signup", "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "same-origin", "user-agent": randomUA(),
	}
	client := newHTTPClientTLS(proxyURL, c.timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("irctc status %d", status)
	}
	var parsed struct {
		MobileAvailable string `json:"mobileAvailable"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("irctc decode: %w", err)
	}
	switch parsed.MobileAvailable {
	case "FALSE":
		return true, nil // not available => registered
	case "TRUE":
		return false, nil // available => not registered
	default:
		return false, fmt.Errorf("irctc: no condition matched (%q)", parsed.MobileAvailable)
	}
}
