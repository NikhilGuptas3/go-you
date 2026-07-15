package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Duolingo ports crawler/spiders/ed_tech/duolingo/duolingo.py (email).
//
// GET users?email=<email>. json.users present and non-empty => exists (rich:
// extra_data = first user object); present and empty => not-exists; no 'users'
// key => no-match error.
type Duolingo struct{ timeout time.Duration }

func NewDuolingo(timeout time.Duration) *Duolingo { return &Duolingo{timeout: timeout} }

func (c *Duolingo) Website() string { return "DUOLINGO" }
func (c *Duolingo) Kind() Kind      { return KindEmail }

func (c *Duolingo) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	u := "https://www.duolingo.com/2017-06-30/users?email=" + url.QueryEscape(identifier)
	headers := map[string]string{
		"authority":       "www.duolingo.com",
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9",
		"accept-language": "en-GB,en;q=0.9", "sec-ch-ua-mobile": "?0",
		"sec-fetch-dest": "document", "sec-fetch-mode": "navigate", "sec-fetch-site": "none",
		"sec-fetch-user": "?1", "upgrade-insecure-requests": "1", "user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return nil, nil, err
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("duolingo status %d", status)
	}
	var parsed struct {
		Users *[]map[string]any `json:"users"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, nil, fmt.Errorf("duolingo decode: %w", err)
	}
	if parsed.Users == nil {
		return nil, nil, fmt.Errorf("duolingo: no condition matched (no users key)")
	}
	users := *parsed.Users
	if len(users) == 0 {
		return boolPtr(false), nil, nil
	}
	// Note: the extra_data (first user object) is later stripped by the
	// transform step (remove_extra_data_from_duolingo) unless internal_request.
	return boolPtr(true), map[string]any{"extra_data": users[0]}, nil
}

func (c *Duolingo) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}
