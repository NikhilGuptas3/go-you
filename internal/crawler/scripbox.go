package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Scripbox ports crawler/spiders/investments/scripbox/scripbox.py (email).
//
// POST user/check. status 200 with a non-empty data => exists; status 404 with
// error.message=="user not found" => not-exists; else no-match error.
type Scripbox struct{ timeout time.Duration }

func NewScripbox(timeout time.Duration) *Scripbox { return &Scripbox{timeout: timeout} }

func (c *Scripbox) Website() string { return "SCRIPBOX" }
func (c *Scripbox) Kind() Kind      { return KindEmail }

const scripboxURL = "https://api.scripbox.com/auth/v2/user/check"

func (c *Scripbox) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{
		"api_version": "2.0",
		"data":        map[string]any{"kind": "user", "email": identifier},
	})
	headers := map[string]string{
		"authority": "api.scripbox.com", "accept": "application/json, text/plain, */*",
		"accept-language": "en-IN,en;q=0.9", "application-id": "ec3f64bd-5bfa-407c-9291-8cbda78c75a1",
		"content-type": "application/json", "origin": "https://scripbox.com",
		"referer": "https://scripbox.com/", "sec-ch-ua-mobile": "?0", "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "same-site", "user-agent": randomUA(),
		"x-app-name": "castor", "x-app-platform": "web",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", scripboxURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	switch status {
	case 200:
		var parsed struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return false, fmt.Errorf("scripbox decode: %w", err)
		}
		// Non-empty data object/array => exists. An empty/absent data => no-match.
		if len(parsed.Data) > 0 && string(parsed.Data) != "null" &&
			string(parsed.Data) != "{}" && string(parsed.Data) != "[]" {
			return true, nil
		}
		return false, fmt.Errorf("scripbox: no condition matched (empty data)")
	case 404:
		var parsed struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return false, fmt.Errorf("scripbox decode: %w", err)
		}
		if parsed.Error.Message == "user not found" {
			return false, nil
		}
		return false, fmt.Errorf("scripbox: no condition matched (404)")
	default:
		return false, fmt.Errorf("scripbox status %d", status)
	}
}
