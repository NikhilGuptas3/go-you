package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// Irctc ports crawler/spiders/travel/irctc/irctc.py + irctc_email.py. GET
// checkUserAvail. The "{mobile,email}Available" flag is INVERTED: "FALSE" =>
// the id is taken => exists; "TRUE" => available => not-exists. The email flow
// queries ?email=<urlencoded> (no &isd) and reads emailAvailable; the phone
// flow queries ?mobile=<national>&isd=91 and reads mobileAvailable. curl_cffi
// => TLSChrome. A fresh greq (GQ:<uuid4>) is generated per request.
type Irctc struct {
	timeout time.Duration
	kind    Kind
}

func NewIrctc(timeout time.Duration) *Irctc { return &Irctc{timeout: timeout, kind: KindPhone} }

// NewIrctcEmail registers IRCTC on the email flow.
func NewIrctcEmail(timeout time.Duration) *Irctc { return &Irctc{timeout: timeout, kind: KindEmail} }

func (c *Irctc) Website() string { return "IRCTC" }
func (c *Irctc) Kind() Kind      { return c.kind }

func (c *Irctc) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const base = "https://www.irctc.co.in/eticketing/protected/mapps1/checkUserAvail?"
	var u, field string
	if c.kind == KindEmail {
		u = base + "email=" + url.QueryEscape(identifier)
		field = "emailAvailable"
	} else {
		u = base + "mobile=" + url.QueryEscape(nationalNumber(identifier)) + "&isd=91"
		field = "mobileAvailable"
	}
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
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("irctc decode: %w", err)
	}
	switch parsed[field] {
	case "FALSE":
		return true, nil // not available => registered
	case "TRUE":
		return false, nil // available => not registered
	default:
		return false, fmt.Errorf("irctc: no condition matched (%v)", parsed[field])
	}
}
