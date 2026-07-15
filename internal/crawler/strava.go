package crawler

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"time"
)

// Strava ports crawler/spiders/sports/strava/strava.py (email).
//
// GET email_unique?email=<email>. The body is a bare JSON boolean with INVERTED
// semantics: true => email available (not registered) => not-exists; false =>
// taken => exists; anything else => no-match error.
type Strava struct{ timeout time.Duration }

func NewStrava(timeout time.Duration) *Strava { return &Strava{timeout: timeout} }

func (c *Strava) Website() string { return "STRAVA" }
func (c *Strava) Kind() Kind      { return KindEmail }

func (c *Strava) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://www.strava.com/frontend/athletes/email_unique?email=" + url.QueryEscape(identifier)
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-US",
		"priority": "u=1, i", "referer": "https://www.strava.com/register/free",
		"sec-ch-ua":        `"Chromium";v="130", "Google Chrome";v="130", "Not?A_Brand";v="99"`,
		"sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": `"macOS"`,
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"x-requested-with": "XMLHttpRequest", "user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("strava status %d", status)
	}
	switch {
	case bytes.Equal(bytes.TrimSpace(respBody), []byte("true")):
		return false, nil // unique/available => not registered
	case bytes.Equal(bytes.TrimSpace(respBody), []byte("false")):
		return true, nil // taken => registered
	default:
		return false, fmt.Errorf("strava: no condition matched")
	}
}
