package crawler

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// A small rotating set of realistic desktop Chrome user agents, standing in for
// the Python latest_user_agents.get_random_user_agent(). Rotation is by request
// count (not crypto-random) — enough to avoid a single static UA without needing
// a randomness source (Math.random is unavailable in some contexts and a fixed
// pool is deterministic for tests).
var userAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
}

// uaCounter rotates the UA pool. Concurrent increments race harmlessly — we only
// need spread, not a precise sequence.
var uaCounter int

func randomUA() string {
	uaCounter++
	return userAgents[uaCounter%len(userAgents)]
}

// doRequest builds a request with ctx, applies headers, sends it via a
// per-call client (proxy-aware, given TLS mode), and returns the status code
// and body. It centralizes the boilerplate every spider repeats.
//
// body may be nil (e.g. GET). headers is applied verbatim (order-independent —
// Go's http.Header is a map). On transport error it returns (0, nil, err).
func doRequest(
	ctx context.Context,
	client *http.Client,
	method, rawURL string,
	body io.Reader,
	headers map[string]string,
) (status int, respBody []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

// formBody encodes a form body (application/x-www-form-urlencoded value string).
func formBody(values map[string]string) *strings.Reader {
	v := url.Values{}
	for k, val := range values {
		v.Set(k, val)
	}
	return strings.NewReader(v.Encode())
}

// boolPtr returns a pointer to b (crawlers return *bool for the optional
// user_exist verdict).
func boolPtr(b bool) *bool { return &b }

// truthy applies Python-style truthiness to an arbitrary decoded JSON value,
// for spiders whose verdict keys on `if value:` (bool, non-zero number,
// non-empty string, non-null).
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != "" && !strings.EqualFold(t, "false")
	default:
		return true
	}
}

// nationalNumber strips a leading "+" and the India country code "91" from an
// international identifier to yield the 10-digit national number several
// spiders send (e.g. "+919876543210" -> "9876543210"). Inputs already in
// national form pass through unchanged.
func nationalNumber(identifier string) string {
	s := strings.TrimPrefix(strings.TrimSpace(identifier), "+")
	// Only strip a leading "91" when what remains is a plausible 10-digit
	// national number, so a genuine number starting with 91 isn't mangled.
	if strings.HasPrefix(s, "91") && len(s) == 12 {
		return s[2:]
	}
	return s
}

// internationalNumber returns the E.164 form "+<cc><national>" some spiders
// send. The persona handler already normalizes the identifier to "+<cc><num>",
// so this is largely a pass-through that guarantees a leading "+".
func internationalNumber(identifier string) string {
	s := strings.TrimSpace(identifier)
	if strings.HasPrefix(s, "+") {
		return s
	}
	return "+" + s
}

// checkViaDetail adapts a DetailCrawler's CheckDetail to the plain Check
// signature, so a rich crawler can satisfy both without duplicating logic.
// A nil userExist (no-match) surfaces as an error, matching the Python
// NoConditionMatched behavior for the bool-only callers.
func checkViaDetail(dc DetailCrawler, ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	exist, _, err := dc.CheckDetail(ctx, identifier, proxyURL)
	if err != nil {
		return false, err
	}
	if exist == nil {
		return false, errNoConditionMatched
	}
	return *exist, nil
}
