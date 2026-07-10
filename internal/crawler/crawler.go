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
// an error channel.
type Result struct {
	Website   string
	UserExist *bool
	Err       error
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

// newHTTPClient builds a client bound to a single proxy (or direct if nil) with
// the per-request timeout. A fresh client per call keeps proxy selection
// independent, matching Python's per-request proxy resolution.
//
// NOTE: Go's net/http does not replicate curl_cffi's TLS fingerprint. If a
// target blocks default Go TLS, that is a POC finding — swap in a utls-based
// client here without touching the crawlers.
func newHTTPClient(proxyURL *url.URL, timeout time.Duration) *http.Client {
	transport := &http.Transport{}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}
