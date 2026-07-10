package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Freelancer ports crawler/spiders/jobs/freelancer/freelancer.py (FreelancerBase).
//
// Python flow: POST JSON {"user":{"email":<id>}} to the users/check endpoint
// with query params compact/new_errors/new_pools. A 409 with message
// "This email already exists..." => user exists; a 200 with status "success"
// => user does NOT exist; anything else => NoConditionMatched.
//
// This is an EMAIL crawler (no phone variant exists).
type Freelancer struct {
	timeout time.Duration
}

func NewFreelancer(timeout time.Duration) *Freelancer { return &Freelancer{timeout: timeout} }

func (f *Freelancer) Website() string { return "FREELANCER" }
func (f *Freelancer) Kind() Kind      { return KindEmail }

const freelancerURL = "https://www.freelancer.com/api/users/0.1/users/check?compact=true&new_errors=true&new_pools=true"

const freelancerExistsMsg = "This email already exists, please choose another"

func (f *Freelancer) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	payload, _ := json.Marshal(map[string]any{
		"user": map[string]string{"email": identifier},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, freelancerURL, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("accept-language", "en-IN,en;q=0.9")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "https://www.freelancer.com")
	req.Header.Set("referer", "https://www.freelancer.com/post-project")
	req.Header.Set("user-agent", "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36")

	client := newHTTPClient(proxyURL, f.timeout)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	switch resp.StatusCode {
	case http.StatusConflict: // 409
		var parsed struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return false, fmt.Errorf("freelancer decode (409): %w", err)
		}
		if parsed.Message == freelancerExistsMsg {
			return true, nil
		}
		return false, fmt.Errorf("freelancer: unexpected 409 message %q", parsed.Message)
	case http.StatusOK:
		var parsed struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return false, fmt.Errorf("freelancer decode (200): %w", err)
		}
		if parsed.Status == "success" {
			return false, nil
		}
		return false, fmt.Errorf("freelancer: no condition matched (status %q)", parsed.Status)
	default:
		return false, fmt.Errorf("freelancer: no condition matched (http %d)", resp.StatusCode)
	}
}
