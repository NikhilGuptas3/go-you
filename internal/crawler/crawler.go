// Package crawler defines the Crawler contract and the token-free spiders the
// POC ports from Python (crawler/base_api_spider.py ApiSpider + subclasses).
//
// Only token-free sources are included: Flipkart, Instagram (phone) and Spotify,
// Freelancer (email). Facebook and any token-pool source are deliberately out of
// scope.
package crawler

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Kind distinguishes phone crawlers from email crawlers, mirroring the
// PhoneFactory / EmailFactory split.
type Kind string

const (
	KindPhone Kind = "phone"
	KindEmail Kind = "email"
)

// Result is one crawler's verdict. Mirrors the Python {"user_exist": bool} plus
// an error channel. Rich crawlers (DetailCrawler) additionally populate Data
// with spider-specific fields (e.g. TELEGRAM username, GOOGLE reviews).
type Result struct {
	Website   string
	Kind      Kind
	UserExist *bool
	// Data holds rich per-site fields from a DetailCrawler; nil for simple
	// crawlers. Flattened alongside user_exist in the client account entry.
	Data map[string]any
	Err  error
	// Duration is how long this crawler's Check took — used for per-stage
	// latency reporting.
	Duration time.Duration
}

// Crawler is the Go equivalent of the ApiSpider template method: given an
// identifier (a phone or an email) it performs the login-probe request and
// parses existence. Each implementation owns its endpoint, headers and parse
// rules, exactly as each Python spider does.
type Crawler interface {
	Website() string
	Kind() Kind
	// Check probes whether identifier has an account. proxyURL may be nil
	// (crawl direct). It must respect ctx cancellation for the timeout model.
	Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error)
}

// DetailCrawler is the optional extension for spiders that return more than an
// existence bool (TELEGRAM, GOOGLE, GITHUB, INDANE_GAS, UAN_*, ...). The runner
// prefers CheckDetail when a crawler implements it, falling back to Check
// otherwise. userExist may be nil (no verdict / no-match); data may be nil even
// on a positive verdict.
type DetailCrawler interface {
	Crawler
	CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (userExist *bool, data map[string]any, err error)
}

// TLSMode selects the TLS ClientHello fingerprint a crawler's HTTP client
// presents. Many hey-you spiders use curl_cffi to impersonate Chrome; sites that
// fingerprint (JA3) will block Go's default net/http hello. Crawlers that need
// impersonation request TLSChrome; the rest use TLSDefault.
type TLSMode int

const (
	// TLSDefault uses Go's stock net/http TLS stack.
	TLSDefault TLSMode = iota
	// TLSChrome presents a Chrome-like ClientHello (uTLS). Used by the
	// curl_cffi-sensitive sites flagged in the plan (Amazon, JSSO family,
	// IRCTC, Freecharge, ...).
	TLSChrome
)

// newHTTPClient builds a client bound to a single proxy (or direct if nil) with
// the per-request timeout, using Go's stock TLS stack. A fresh client per call
// keeps proxy selection independent, matching Python's per-request proxy
// resolution. Crawlers that need Chrome impersonation call newHTTPClientTLS
// with TLSChrome instead.
func newHTTPClient(proxyURL *url.URL, timeout time.Duration) *http.Client {
	return newHTTPClientTLS(proxyURL, timeout, TLSDefault)
}

// newHTTPClientTLS is newHTTPClient with an explicit TLS fingerprint mode.
// TLSChrome swaps in the uTLS round-tripper (newChromeTransport) so
// curl_cffi-impersonating sites are not blocked; the crawlers stay unchanged
// and only declare which mode they need.
func newHTTPClientTLS(proxyURL *url.URL, timeout time.Duration, mode TLSMode) *http.Client {
	if mode == TLSChrome {
		return &http.Client{Transport: newChromeTransport(proxyURL), Timeout: timeout}
	}
	transport := &http.Transport{}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}
