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

// Token is one identifier-agnostic session token — a shared CSRF / cookie /
// bearer fetched by a two-step crawler's step 1 that can serve many different
// identifier lookups. It mirrors the Python token dict: the site-specific values
// plus usage/creation bookkeeping the pool manages.
type Token struct {
	// Values are the site-specific token fields (e.g. {"csrftoken": "…"},
	// {"authenticity_token": "…", "cookie": "…"}). Opaque to the pool; the
	// crawler's CheckWithToken interprets them.
	Values map[string]string
	// Usages counts how many checks have used this token (the pool evicts at
	// USE_LIMIT). Created is when it was minted (the pool evicts at Created+TTL).
	Usages  int
	Created time.Time
}

// TokenSource supplies a warm pooled token for a (website, kind), or (nil,false)
// on a miss. The token pool's Manager implements it. A crawler holds one
// (possibly nil) and consults it before doing its own step 1 — a nil source, or
// a miss, means "generate inline". This is the seam that lets the background pool
// short-circuit step 1 without the crawler knowing about the pool package.
type TokenSource interface {
	GetToken(website string, kind Kind) (map[string]string, bool)
}

// tokenVia returns a token for the crawler: a warm one from src on a hit, else a
// freshly generated one via gen (the get_or_generate_token fallback). src may be
// nil (always generate). It is the shared helper every TokenCrawler's Check uses
// so the pool/inline decision lives in one place.
func tokenVia(ctx context.Context, src TokenSource, website string, kind Kind, gen func() (map[string]string, error)) (map[string]string, error) {
	if src != nil {
		if tok, ok := src.GetToken(website, kind); ok {
			return tok, nil
		}
	}
	return gen()
}

// TokenCrawler is the optional extension for the two-step token-gated crawlers
// (Twitter, Microsoft, Apple, Zoho, …). It splits the Python get_login_response
// into its two faithful halves so a background pool can pre-warm step 1:
//
//   - GenerateToken performs step 1 — fetch the identifier-AGNOSTIC session
//     token (cookie/CSRF/bearer). The pool calls this off the request path.
//   - CheckWithToken performs step 2 — the existence probe for one identifier,
//     using a token (pooled or freshly generated).
//
// A TokenCrawler's plain Check (from Crawler) still works standalone: it does
// GenerateToken then CheckWithToken inline (the stateless path), so a crawler is
// fully functional whether or not a pool is attached. The pool, when present,
// simply supplies a warm token to skip step 1.
type TokenCrawler interface {
	Crawler
	// GenerateToken fetches a fresh session token (step 1). It must NOT depend on
	// any identifier — the returned token is reusable across lookups.
	GenerateToken(ctx context.Context, proxyURL *url.URL) (map[string]string, error)
	// CheckWithToken runs the existence probe (step 2) for identifier using the
	// given token values. proxyURL may be nil.
	CheckWithToken(ctx context.Context, identifier string, token map[string]string, proxyURL *url.URL) (bool, error)
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
	// TLSSafari presents a Safari-like ClientHello (uTLS). Python special-cases
	// exactly one site: `impersonate = "safari" if self.ID == QUORA else "chrome"`
	// (base_api_spider.py) — Quora TLS-gates on the Safari fingerprint and 403s a
	// Chrome hello. Only QUORA uses this mode.
	TLSSafari
)

// newHTTPClient builds a client bound to a single proxy (or direct if nil) with
// the per-request timeout, using Go's stock TLS stack. Proxy selection stays
// per-call (matching Python's per-request proxy resolution); the underlying
// transport is SHARED per (proxy, mode) so the proxy connection pool is reused
// across crawls (see transport_pool.go). Crawlers that need Chrome impersonation
// call newHTTPClientTLS with TLSChrome instead.
func newHTTPClient(proxyURL *url.URL, timeout time.Duration) *http.Client {
	return newHTTPClientTLS(proxyURL, timeout, TLSDefault)
}

// newHTTPClientTLS is newHTTPClient with an explicit TLS fingerprint mode.
// TLSChrome / TLSSafari use the uTLS round-tripper so curl_cffi-impersonating
// sites are not blocked; the crawlers stay unchanged and only declare which mode
// they need. The returned *http.Client is cheap and per-call, but its transport
// is the long-lived shared one for (proxyURL, mode), so connections pool and the
// proxy CONNECT+TLS handshake is amortized instead of re-paid every request.
func newHTTPClientTLS(proxyURL *url.URL, timeout time.Duration, mode TLSMode) *http.Client {
	return &http.Client{Transport: sharedTransport(proxyURL, mode), Timeout: timeout}
}
