package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// This file holds the remaining single-request email crawlers whose logic is
// short enough to group: PINTEREST, TWITTER, ADOBE, ENVATO, PATREON, BITMOJI.
// (DISCORD is in discord.go — it needs randomized fields.) All use stock TLS.

// --- PINTEREST ---
// GET the ApiResource proxy for /v3/register/exists/. The response contains the
// substring "source_field" in all cases; existence is resource_response.data
// being truthy (Python: `if data:` after confirming 'source_field' present).
type Pinterest struct{ timeout time.Duration }

func NewPinterest(timeout time.Duration) *Pinterest { return &Pinterest{timeout: timeout} }
func (c *Pinterest) Website() string                { return "PINTEREST" }
func (c *Pinterest) Kind() Kind                     { return KindEmail }

func (c *Pinterest) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	// data = {"options":{"url":"/v3/register/exists/","data":{"email":<id>}},"context":{}}
	dataObj := `{"options":{"url":"/v3/register/exists/","data":{"email":"` + identifier + `"}},"context":{}}`
	u := "https://in.pinterest.com/resource/ApiResource/get/?source_url=%2F&data=" + url.QueryEscape(dataObj)
	headers := map[string]string{"x-pinterest-pws-handler": "www/index.js", "user-agent": randomUA()}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 || !strings.Contains(string(respBody), "source_field") {
		return false, fmt.Errorf("pinterest: no condition matched (status=%d)", status)
	}
	var parsed struct {
		ResourceResponse struct {
			Data json.RawMessage `json:"data"`
		} `json:"resource_response"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("pinterest decode: %w", err)
	}
	d := strings.TrimSpace(string(parsed.ResourceResponse.Data))
	if d != "" && d != "null" && d != "{}" && d != "[]" {
		return true, nil
	}
	return false, nil
}

// --- TWITTER (email) ---
// GET email_available.json?email=<id>. json.taken => user_exist verbatim.
type TwitterEmail struct{ timeout time.Duration }

func NewTwitterEmail(timeout time.Duration) *TwitterEmail { return &TwitterEmail{timeout: timeout} }
func (c *TwitterEmail) Website() string                   { return "TWITTER" }
func (c *TwitterEmail) Kind() Kind                        { return KindEmail }

func (c *TwitterEmail) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://api.twitter.com/i/users/email_available.json?email=" + url.QueryEscape(identifier)
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, map[string]string{})
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("twitter status %d", status)
	}
	var parsed struct {
		Taken *bool `json:"taken"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.Taken == nil {
		return false, fmt.Errorf("twitter: no condition matched")
	}
	return *parsed.Taken, nil
}

// --- ADOBE ---
// POST accounts. Non-empty JSON array/object => exists; empty => not-exists.
type Adobe struct{ timeout time.Duration }

func NewAdobe(timeout time.Duration) *Adobe { return &Adobe{timeout: timeout} }
func (c *Adobe) Website() string            { return "ADOBE" }
func (c *Adobe) Kind() Kind                 { return KindEmail }

func (c *Adobe) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"username": identifier, "usernameType": "EMAIL"})
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-GB,en-US;q=0.9,en;q=0.8",
		"content-type": "application/json", "origin": "https://auth.services.adobe.com",
		"priority": "u=1, i", "referer": "https://auth.services.adobe.com/en_US/index.html",
		"user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", "https://auth.services.adobe.com/signin/v2/users/accounts", strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("adobe status %d", status)
	}
	// Response is a JSON array of accounts; non-empty => exists.
	var arr []json.RawMessage
	if err := json.Unmarshal(respBody, &arr); err != nil {
		// Some responses may be an object; fall back to truthiness of the raw body.
		t := strings.TrimSpace(string(respBody))
		if t != "" && t != "{}" && t != "[]" && t != "null" {
			return true, nil
		}
		return false, nil
	}
	return len(arr) > 0, nil
}

// --- ENVATO ---
// POST validate_email. 204 empty => not-exists; error_message "Email is already
// taken" => exists.
type Envato struct{ timeout time.Duration }

func NewEnvato(timeout time.Duration) *Envato { return &Envato{timeout: timeout} }
func (c *Envato) Website() string             { return "ENVATO" }
func (c *Envato) Kind() Kind                  { return KindEmail }

func (c *Envato) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"language_code": "en", "email": identifier})
	headers := map[string]string{
		"accept": "application/json", "accept-language": "en-GB,en;q=0.9", "content-type": "application/json",
		"origin": "https://elements.envato.com", "referer": "https://elements.envato.com/",
		"sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": `"macOS"`, "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "same-site", "user-agent": randomUA(),
		"x-client-version": "3.5.1",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", "https://account.envato.com/api/public/validate_email", strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status == 204 && len(strings.TrimSpace(string(respBody))) == 0 {
		return false, nil
	}
	var parsed struct {
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil && strings.HasPrefix(parsed.ErrorMessage, "Email is already") {
		return true, nil
	}
	return false, fmt.Errorf("envato: no condition matched (status=%d)", status)
}

// --- PATREON ---
// POST email/available. is_available==false => exists (taken); true => not-exists.
type Patreon struct{ timeout time.Duration }

func NewPatreon(timeout time.Duration) *Patreon { return &Patreon{timeout: timeout} }
func (c *Patreon) Website() string              { return "PATREON" }
func (c *Patreon) Kind() Kind                   { return KindEmail }

func (c *Patreon) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{"attributes": map[string]any{"email": identifier}, "relationships": map[string]any{}},
	})
	headers := map[string]string{
		"authority": "www.patreon.com", "accept": "*/*", "accept-language": "en-GB,en;q=0.9",
		"content-type": "application/vnd.api+json", "origin": "https://www.patreon.com",
		"referer": "https://www.patreon.com/signup", "sec-ch-ua-mobile": "?0", "sec-ch-ua-model": "",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", "https://www.patreon.com/api/email/available?json-api-version=1.0&include=[]", strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("patreon status %d", status)
	}
	var parsed struct {
		Data struct {
			Attributes struct {
				IsAvailable *bool `json:"is_available"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.Data.Attributes.IsAvailable == nil {
		return false, fmt.Errorf("patreon: no condition matched")
	}
	// available == taken? Python: is_available false => exists.
	return !*parsed.Data.Attributes.IsAvailable, nil
}

// --- BITMOJI ---
// POST user/find. 200 + account_type=="bitmoji" => exists; 204 => not-exists.
type Bitmoji struct{ timeout time.Duration }

func NewBitmoji(timeout time.Duration) *Bitmoji { return &Bitmoji{timeout: timeout} }
func (c *Bitmoji) Website() string              { return "BITMOJI" }
func (c *Bitmoji) Kind() Kind                   { return KindEmail }

func (c *Bitmoji) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"email": identifier})
	headers := map[string]string{
		"accept": "application/json, text/plain, */*", "accept-language": "en-IN,en;q=0.9",
		"content-type": "application/json;charset=UTF-8", "origin": "https://www.bitmoji.com",
		"priority": "u=1, i", "referer": "https://www.bitmoji.com/",
		"sec-ch-ua":        `"Google Chrome";v="129", "Not=A?Brand";v="8", "Chromium";v="129"`,
		"sec-ch-ua-mobile": "?1", "sec-ch-ua-platform": `"Android"`, "sec-fetch-dest": "empty",
		"user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", "https://us-east-1-bitmoji.api.snapchat.com/api/user/find", strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	switch status {
	case 204:
		return false, nil
	case 200:
		var parsed struct {
			AccountType string `json:"account_type"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return false, fmt.Errorf("bitmoji decode: %w", err)
		}
		if parsed.AccountType == "bitmoji" {
			return true, nil
		}
		return false, fmt.Errorf("bitmoji: no condition matched (account_type=%q)", parsed.AccountType)
	default:
		return false, fmt.Errorf("bitmoji status %d", status)
	}
}
