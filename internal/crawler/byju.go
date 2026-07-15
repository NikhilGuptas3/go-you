package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Byju ports crawler/spiders/ed_tech/byju/byju.py.
//
// POST passcode/policy with {"identifier":"phone","value":"+91-<national>"}.
// status 200 with isPasscodeSet => exists (+partial_email:json.email);
// status 400 with the "not registered" message => not-exists; a
// "Please enable cookies." body is a captcha => error; else no-match error.
type Byju struct{ timeout time.Duration }

func NewByju(timeout time.Duration) *Byju { return &Byju{timeout: timeout} }

func (c *Byju) Website() string { return "BYJU" }
func (c *Byju) Kind() Kind      { return KindPhone }

const byjuURL = "https://identity.tllms.com/api/v2/passcode/policy"

func (c *Byju) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	national := nationalNumber(identifier)
	body, _ := json.Marshal(map[string]any{"identifier": "phone", "value": "+91-" + national})
	headers := map[string]string{
		"authority": "identity.tllms.com", "accept": "application/json, text/plain, */*",
		"accept-language": "en-GB,en;q=0.9", "content-type": "application/json",
		"origin": "https://byjus.com", "referer": "https://byjus.com/", "sec-ch-ua-mobile": "?0",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "cross-site",
		"user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", byjuURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return nil, nil, err
	}
	if strings.Contains(string(respBody), "Please enable cookies.") {
		return nil, nil, fmt.Errorf("byju: captcha")
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, nil, fmt.Errorf("byju decode: %w", err)
	}
	if status == 400 {
		if msg, _ := parsed["message"].(string); msg == "entered mobile number is not registered with us. Please check and retry" {
			return boolPtr(false), nil, nil
		}
		return nil, nil, fmt.Errorf("byju: no condition matched (400)")
	}
	if status == 200 {
		if _, ok := parsed["isPasscodeSet"]; ok {
			data := map[string]any{}
			if email, ok := parsed["email"].(string); ok && email != "" {
				data["partial_email"] = email
			}
			return boolPtr(true), data, nil
		}
	}
	return nil, nil, fmt.Errorf("byju: no condition matched (status=%d)", status)
}

func (c *Byju) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}
