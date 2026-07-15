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

// Flipkart ports crawler/spiders/ecommerce/flipkart/flipkart.py (FlipkartBase).
//
// Python flow: POST {"loginId":[id],"supportAllStates":true} to the signup
// status endpoint, then read RESPONSE.userDetails[id]. A code in the "exists"
// set => user_exist true; a code in the "not exists" set => false; anything else
// is NoConditionMatched (an error, not a false).
type Flipkart struct {
	timeout time.Duration
}

func NewFlipkart(timeout time.Duration) *Flipkart { return &Flipkart{timeout: timeout} }

func (f *Flipkart) Website() string { return "FLIPKART" }
func (f *Flipkart) Kind() Kind      { return KindPhone }

const flipkartURL = "https://2.rome.api.flipkart.com/api/6/user/signup/status"

var (
	flipkartExistCodes    = map[string]bool{"VERIFIED": true, "NOT_VERIFIED": true, "BLOCKED": true, "SOCIAL_GOOGLE": true}
	flipkartNotExistCodes = map[string]bool{"NOT_FOUND": true, "GUEST": true, "CHURNED": true, "LOGIN_INACTIVE": true}
)

func (f *Flipkart) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	payload, _ := json.Marshal(map[string]any{
		"loginId":          []string{identifier},
		"supportAllStates": true,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, flipkartURL, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	// Headers ported verbatim from the Python login_headers.
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.flipkart.com")
	req.Header.Set("Referer", "https://www.flipkart.com/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("X-User-Agent", ua+" FKUA/website/42/website/Desktop")

	client := newHTTPClient(proxyURL, f.timeout)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("flipkart status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var parsed struct {
		Response struct {
			UserDetails map[string]string `json:"userDetails"`
		} `json:"RESPONSE"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("flipkart decode: %w", err)
	}

	code, ok := parsed.Response.UserDetails[identifier]
	if !ok {
		return false, fmt.Errorf("flipkart: no userDetails for identifier")
	}
	switch {
	case flipkartExistCodes[code]:
		return true, nil
	case flipkartNotExistCodes[code]:
		return false, nil
	default:
		// Python raises NoConditionMatchedException here.
		return false, fmt.Errorf("flipkart: no condition matched for code %q", code)
	}
}
