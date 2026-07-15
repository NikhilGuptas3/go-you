package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Spicejet ports crawler/spiders/travel/spicejet/spicejet.py.
//
// POST userValidate with {"mobile": "+91<num>"} (international form). Message
// containing "Member account exists with the given mobile number" => exists;
// "No member account exists with the given mobile number" => not-exists; else error.
type Spicejet struct{ timeout time.Duration }

func NewSpicejet(timeout time.Duration) *Spicejet { return &Spicejet{timeout: timeout} }

func (c *Spicejet) Website() string { return "SPICEJET" }
func (c *Spicejet) Kind() Kind      { return KindPhone }

const spicejetURL = "https://spiceclub.spicejet.com/api/v1/token/userValidate"

func (c *Spicejet) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"mobile": internationalNumber(identifier)})
	headers := map[string]string{
		"Accept": "application/json, text/plain, */*", "Referer": "https://spiceclub.spicejet.com/signup",
		"Content-Type": "application/json;charset=UTF-8", "User-Agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", spicejetURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("spicejet status %d", status)
	}
	var parsed struct {
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("spicejet decode: %w", err)
	}
	switch {
	case strings.Contains(parsed.Message, "Member account exists with the given mobile number"):
		return true, nil
	case strings.Contains(parsed.Message, "No member account exists with the given mobile number"):
		return false, nil
	default:
		return false, fmt.Errorf("spicejet: no condition matched")
	}
}
