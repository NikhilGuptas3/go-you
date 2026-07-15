package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"time"
)

// Discord ports crawler/spiders/social/discord/discord.py (email). POST the
// registration endpoint with a random username/password; the error response
// reveals existence. errors.email._errors[0].code == "EMAIL_ALREADY_REGISTERED"
// => exists; captcha or any other shape => not-exists; unparseable => error.
type Discord struct{ timeout time.Duration }

func NewDiscord(timeout time.Duration) *Discord { return &Discord{timeout: timeout} }
func (c *Discord) Website() string              { return "DISCORD" }
func (c *Discord) Kind() Kind                   { return KindEmail }

const discordURL = "https://discord.com/api/v8/auth/register"

func randomLower(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (c *Discord) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{
		"fingerprint": "", "email": identifier,
		"username": randomLower(20), "password": randomLower(20),
		"invite": nil, "consent": true, "date_of_birth": "",
		"gift_code_sku_id": nil, "captcha_key": nil,
	})
	headers := map[string]string{
		"User-Agent": randomUA(), "Accept": "*/*", "Accept-Language": "en-US",
		"Content-Type": "application/json", "Origin": "https://discord.com",
		"DNT": "1", "Connection": "keep-alive", "TE": "Trailers",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	_, respBody, err := doRequest(ctx, client, "POST", discordURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("discord: parse failed: %w", err)
	}
	if _, hasCode := parsed["code"]; hasCode {
		// Navigate errors.email._errors[0].code defensively.
		if code := discordEmailErrorCode(parsed); code == "EMAIL_ALREADY_REGISTERED" {
			return true, nil
		}
		return false, nil // code present but not the "already registered" path
	}
	// No "code": check captcha_key[0] == "captcha-required". If captcha_key is
	// absent, Python's access raises -> outer except -> error. Mirror that.
	ck, ok := parsed["captcha_key"]
	if !ok {
		return false, fmt.Errorf("discord: no condition matched (no code, no captcha_key)")
	}
	if arr, ok := ck.([]any); ok && len(arr) > 0 {
		if s, _ := arr[0].(string); s == "captcha-required" {
			return false, nil
		}
	}
	return false, nil
}

// discordEmailErrorCode walks parsed.errors.email._errors[0].code, returning ""
// if any step is missing (Python treats that as not-registered).
func discordEmailErrorCode(parsed map[string]any) string {
	errs, ok := parsed["errors"].(map[string]any)
	if !ok {
		return ""
	}
	email, ok := errs["email"].(map[string]any)
	if !ok {
		return ""
	}
	list, ok := email["_errors"].([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := first["code"].(string)
	return code
}
