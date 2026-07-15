package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Github ports crawler/spiders/softwares/github/github.py (email). GET
// search/users?q=<email>. total_count>0 => exists (rich: total_accounts);
// ==0 => not-exists. Note the GitHub search API is rate-limited without a PAT.
type Github struct{ timeout time.Duration }

func NewGithub(timeout time.Duration) *Github { return &Github{timeout: timeout} }
func (c *Github) Website() string             { return "GITHUB" }
func (c *Github) Kind() Kind                  { return KindEmail }

func (c *Github) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	u := "https://api.github.com/search/users?q=" + url.QueryEscape(identifier)
	headers := map[string]string{
		"authority":       "api.github.com",
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9",
		"accept-language": "en-IN,en;q=0.9", "sec-ch-ua-mobile": "?0", "sec-fetch-dest": "document",
		"sec-fetch-mode": "navigate", "sec-fetch-site": "none", "sec-fetch-user": "?1",
		"upgrade-insecure-requests": "1", "user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return nil, nil, err
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("github status %d", status)
	}
	var parsed struct {
		Items      *json.RawMessage `json:"items"`
		TotalCount *int             `json:"total_count"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, nil, fmt.Errorf("github decode: %w", err)
	}
	if parsed.Items == nil {
		return nil, nil, fmt.Errorf("github: no items key")
	}
	if parsed.TotalCount == nil {
		return nil, nil, fmt.Errorf("github: no total_count")
	}
	if *parsed.TotalCount > 0 {
		return boolPtr(true), map[string]any{"total_accounts": *parsed.TotalCount}, nil
	}
	return boolPtr(false), map[string]any{"total_accounts": 0}, nil
}

func (c *Github) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}
