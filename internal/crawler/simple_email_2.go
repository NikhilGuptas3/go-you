package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// This file holds the second batch of single-request, token-free email crawlers
// ported from the Python spiders: NAUKRI, BODYBUILDING, ATLASSIAN, FLICKR, and
// SHAADI (email). Each is one HTTP call whose status code or a JSON field decides
// existence — no token pool, no session, no captcha — so they follow the same
// shape as simple_email.go. A returned error mirrors Python's
// NoConditionMatchedException (an indeterminate response), NOT a "not-exist".

// --- NAUKRI (email) ---
// HEAD the accounts endpoint: 200 => exists, 404 => not. 429/406 (rate-limit /
// captcha) and anything else are indeterminate. Ports crawler/spiders/jobs/naukri.
type Naukri struct{ timeout time.Duration }

func NewNaukri(timeout time.Duration) *Naukri { return &Naukri{timeout: timeout} }
func (c *Naukri) Website() string             { return "NAUKRI" }
func (c *Naukri) Kind() Kind                  { return KindEmail }

func (c *Naukri) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	email := strings.TrimRight(strings.TrimSpace(identifier), "/.")
	u := "https://www.naukri.com/cloudgateway-mynaukri/resman-aggregator-services/v0/accounts/" + url.PathEscape(email)
	headers := map[string]string{
		"accept":        "application/json",
		"cache-control": "no-cache",
		"content-type":  "application/json",
		"appid":         "104",
		"systemid":      "js",
		"clientid":      "d3skt0p",
		"origin":        "https://www.naukri.com",
		"referer":       "https://www.naukri.com/registration/createAccount",
		"user-agent":    randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, _, err := doRequest(ctx, client, "HEAD", u, nil, headers)
	if err != nil {
		return false, err
	}
	switch status {
	case 200:
		return true, nil
	case 404:
		return false, nil
	default:
		return false, fmt.Errorf("naukri: no condition matched (status=%d)", status)
	}
}

// --- BODYBUILDING (email) ---
// HEAD api.bodybuilding.com/profile/email/{email}: 200 => exists, 404 => not.
// USA-zone site. Ports crawler/spiders/sports/bodybuilding.
type Bodybuilding struct{ timeout time.Duration }

func NewBodybuilding(timeout time.Duration) *Bodybuilding { return &Bodybuilding{timeout: timeout} }
func (c *Bodybuilding) Website() string                   { return "BODYBUILDING" }
func (c *Bodybuilding) Kind() Kind                        { return KindEmail }

func (c *Bodybuilding) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://api.bodybuilding.com/profile/email/" + url.PathEscape(identifier)
	headers := map[string]string{
		"accept":             "application/json, text/plain, */*",
		"accept-language":    "en-IN,en;q=0.9",
		"origin":             "https://www.bodybuilding.com",
		"referer":            "https://www.bodybuilding.com/",
		"sec-ch-ua":          `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		"sec-ch-ua-mobile":   "?1",
		"sec-ch-ua-platform": `"Android"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-site",
		"user-agent":         randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, _, err := doRequest(ctx, client, "HEAD", u, nil, headers)
	if err != nil {
		return false, err
	}
	switch status {
	case 200:
		return true, nil
	case 404:
		return false, nil
	default:
		return false, fmt.Errorf("bodybuilding: no condition matched (status=%d)", status)
	}
}

// --- ATLASSIAN (email) ---
// POST id.atlassian.com/rest/check-username with {"username": email}. The JSON
// "action" field decides: no_action / social_login => exists; signup / redirect
// => not exists. Ports crawler/spiders/softwares/attlassian.
type Atlassian struct{ timeout time.Duration }

func NewAtlassian(timeout time.Duration) *Atlassian { return &Atlassian{timeout: timeout} }
func (c *Atlassian) Website() string                { return "ATLASSIAN" }
func (c *Atlassian) Kind() Kind                     { return KindEmail }

func (c *Atlassian) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"username": identifier})
	// continue param copied verbatim from the Python spider (part of the endpoint).
	u := "https://id.atlassian.com/rest/check-username?continue=https%3A%2F%2Fwww.atlassian.com%2Fgateway%2Fapi%2Fstart%2Fauthredirect%3FatlOrigin%3DeyJpIjoiMTYwMjA3MzQ0NmIzNDJhMDljZmQ3ZmQ3ODQ4MjZhZDQiLCJwIjoid2FjLWdsb2JhbGRyb3Bkb3duIn0"
	headers := map[string]string{
		"authority":          "id.atlassian.com",
		"accept":             "*/*",
		"accept-language":    "en-US,en;q=0.9",
		"content-type":       "application/json",
		"origin":             "https://id.atlassian.com",
		"referer":            "https://id.atlassian.com/",
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"macOS"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-origin",
		"user-agent":         randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", u, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	var parsed struct {
		Action string `json:"action"`
	}
	if status != 200 || json.Unmarshal(respBody, &parsed) != nil || parsed.Action == "" {
		return false, fmt.Errorf("atlassian: no condition matched (status=%d)", status)
	}
	switch parsed.Action {
	case "no_action", "social_login":
		return true, nil
	case "signup", "redirect":
		return false, nil
	default:
		return false, fmt.Errorf("atlassian: no condition matched (action=%q)", parsed.Action)
	}
}

// --- FLICKR (email) ---
// GET identity-api.flickr.com/migration?email={email}. stat=="ok" => exists;
// stat=="fail" with code in {1,11} => not exists. Ports crawler/spiders/social/flickr.
type Flickr struct{ timeout time.Duration }

func NewFlickr(timeout time.Duration) *Flickr { return &Flickr{timeout: timeout} }
func (c *Flickr) Website() string             { return "FLICKR" }
func (c *Flickr) Kind() Kind                  { return KindEmail }

func (c *Flickr) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://identity-api.flickr.com/migration?email=" + url.QueryEscape(identifier)
	headers := map[string]string{
		"authority":          "identity-api.flickr.com",
		"accept":             "*/*",
		"accept-language":    "en-US,en;q=0.9",
		"origin":             "https://identity.flickr.com",
		"referer":            "https://identity.flickr.com/login?redir=https%3A%2F%2Fwww.flickr.com%2F",
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"macOS"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-site",
		"user-agent":         randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("flickr: no condition matched (status=%d)", status)
	}
	var parsed struct {
		Stat string `json:"stat"`
		Code int    `json:"code"`
	}
	if json.Unmarshal(respBody, &parsed) != nil {
		return false, fmt.Errorf("flickr decode failed")
	}
	if parsed.Stat == "ok" {
		return true, nil
	}
	if parsed.Stat == "fail" && (parsed.Code == 1 || parsed.Code == 11) {
		return false, nil
	}
	return false, fmt.Errorf("flickr: no condition matched (stat=%q code=%d)", parsed.Stat, parsed.Code)
}

// --- SHAADI (email) ---
// GET .../ajax/check-if-email-exist/email/{email}/duplicate/1/frompage/reg1.
// error==1 with msg "already registered" => exists; error==0 => not exists.
// Ports crawler/spiders/matrimonial/shaadi (email variant). The phone variant is
// intentionally NOT ported: its Python request path is inconsistent (parses
// login-submit text but inherits the email-check GET) and the signal is currently
// down.
type ShaadiEmail struct{ timeout time.Duration }

func NewShaadiEmail(timeout time.Duration) *ShaadiEmail { return &ShaadiEmail{timeout: timeout} }
func (c *ShaadiEmail) Website() string                  { return "SHAADI" }
func (c *ShaadiEmail) Kind() Kind                       { return KindEmail }

func (c *ShaadiEmail) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://www.shaadi.com/ajax/check-if-email-exist/email/" + url.PathEscape(identifier) + "/duplicate/1/frompage/reg1"
	headers := map[string]string{
		"accept":           "application/json, text/javascript, */*",
		"accept-language":  "en-GB,en-US;q=0.9,en;q=0.8",
		"referer":          "https://www.shaadi.com/registration/user?btn=2",
		"sec-ch-ua-mobile": "?1",
		"sec-fetch-dest":   "empty",
		"sec-fetch-mode":   "cors",
		"sec-fetch-site":   "same-origin",
		"user-agent":       "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
		"x-requested-with": "XMLHttpRequest",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("shaadi: no condition matched (status=%d)", status)
	}
	var parsed struct {
		Error int    `json:"error"`
		Msg   string `json:"msg"`
	}
	if json.Unmarshal(respBody, &parsed) != nil {
		return false, fmt.Errorf("shaadi decode failed")
	}
	if parsed.Error == 1 && strings.Contains(parsed.Msg, "This email address is already registered with us") {
		return true, nil
	}
	if parsed.Error == 0 {
		return false, nil
	}
	return false, fmt.Errorf("shaadi: no condition matched (error=%d)", parsed.Error)
}
