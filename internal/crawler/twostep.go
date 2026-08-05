package crawler

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

// This file holds the shared machinery for TWO-STEP (token-gated) crawlers: the
// sites whose existence check needs a prior request to obtain a cookie and/or a
// CSRF/XSRF/bearer token. Ports of the Python token-pool spiders (Zoho, Trivago,
// Vimeo, Tumblr, Shopclues, Eventbrite, Codecademy, Quora, Snapdeal, Oyorooms).
//
// Design (see the Phase B design doc): each such crawler runs BOTH steps inside
// its own Check, using ONE cookie-jar client so the cookie set in step 1 is
// replayed automatically in step 2 (same host). This is the STATELESS model —
// a token fetch per request, faithful in result to Python without the background
// token-pool infrastructure (which the POC deliberately omits). A pool can later
// be slotted in behind these crawlers without changing them.
//
// Two helpers back this:
//   - newHTTPClientJar: a client with an in-memory cookie jar (+ TLS mode).
//   - doRequestFull: like doRequest but also returns the response http.Header,
//     so a crawler can read Set-Cookie / a specific cookie value when it must
//     inject the token as an explicit header or body field.

// newHTTPClientJar is newHTTPClientTLS plus an in-memory cookie jar. The jar
// carries cookies between the two requests of a token-gated crawler (step 1's
// Set-Cookie is replayed on step 2 automatically for the same host), matching
// the Python spiders that thread the cookie string through by hand. The jar is
// per-call (fresh client per request), so requests never share cookie state.
func newHTTPClientJar(proxyURL *url.URL, timeout time.Duration, mode TLSMode) *http.Client {
	client := newHTTPClientTLS(proxyURL, timeout, mode)
	// cookiejar.New(nil) never returns an error; ignore it defensively.
	if jar, err := cookiejar.New(nil); err == nil {
		client.Jar = jar
	}
	return client
}

// doRequestFull is doRequest that additionally returns the response headers, so a
// two-step crawler can read Set-Cookie (or a specific cookie) and forward a token
// into step 2. Behavior is otherwise identical to doRequest: it applies headers
// verbatim, sends via the given client, and returns (status, body, header). On a
// transport error it returns (0, nil, nil, err).
func doRequestFull(
	ctx context.Context,
	client *http.Client,
	method, rawURL string,
	body io.Reader,
	headers map[string]string,
) (status int, respBody []byte, respHeader http.Header, err error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	return resp.StatusCode, b, resp.Header, nil
}

// cookieStringFor returns the "k=v; k=v" Cookie-header string the client's jar
// holds for rawURL — the go-you equivalent of Python's cookie_parser(response),
// which joins every cookie the site set. Crawlers that must send the cookie as an
// explicit header (rather than relying on the jar's automatic replay) use this;
// e.g. when step 2 needs BOTH the cookie header AND a token derived from it.
// Keys are sorted for a stable, testable string. Returns "" when the jar is nil
// or holds no cookies for the URL.
func cookieStringFor(client *http.Client, rawURL string) string {
	if client == nil || client.Jar == nil {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	cookies := client.Jar.Cookies(u)
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// cookieValue returns the value of the named cookie the jar holds for rawURL, or
// "" if absent. Used by crawlers that lift a specific token out of the cookie
// (e.g. Trivago/Oyorooms read XSRF-TOKEN and echo it in a header).
func cookieValue(client *http.Client, rawURL, name string) string {
	if client == nil || client.Jar == nil {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
