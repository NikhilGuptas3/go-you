package crawler

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Transport reuse.
//
// Every crawl used to build a brand-new *http.Transport (see the old
// newHTTPClientTLS): a fresh transport has an empty connection pool, so each
// request re-paid the full proxy CONNECT tunnel + double TLS handshake
// (client<->proxy, then origin-through-tunnel) — a flat ~1–1.5s tax over the
// residential proxy that was thrown away the instant the request returned.
//
// The fix: keep ONE long-lived transport per (proxy, TLS-mode) and reuse it
// across requests, so the proxy tunnel stays warm and the handshake is amortized.
// The set of (proxy, mode) pairs is tiny and fixed (one default proxy + a couple
// named egresses × three TLS modes), so the cache never grows unbounded.
//
// Correctness notes:
//   - Sharing a transport shares its connection pool; it does NOT share cookies
//     or any per-request state — those live on *http.Client (Jar) / *http.Request.
//     Two-step crawlers therefore still get a fresh *http.Client with its own jar
//     per call (newHTTPClientJar), they just reuse the shared RoundTripper.
//   - proxyURL selection stays per-call: the cache key includes the proxy, so
//     different egresses (default / nimble / smartproxy) get their own pools and
//     never cross-contaminate. This preserves the old "proxy selection is
//     independent per call" behaviour while still pooling connections.
//   - uTLS (TLSChrome/TLSSafari) transports are cached the same way. The
//     impersonating RoundTripper is an *http.Transport under the hood, so its
//     idle connections pool and reuse just like the stock one.

// transportPoolConfig tunes the shared transports. The stock
// DefaultMaxIdleConnsPerHost is only 2, far too low for the runner's fan-out of
// ~40 crawlers concurrently through a single proxy host — with the default,
// idle proxy tunnels would be evicted immediately and every crawl would still
// re-handshake. Size the pool for the concurrent fan-out.
const (
	maxIdleConns        = 256
	maxIdleConnsPerHost = 64
	idleConnTimeout     = 90 * time.Second
)

// transportKey identifies a shared transport by its proxy target and TLS mode.
// A nil proxy (direct) is keyed by the empty string.
type transportKey struct {
	proxy string
	mode  TLSMode
}

var (
	transportMu    sync.Mutex
	transportCache = map[transportKey]http.RoundTripper{}
)

// sharedTransport returns the long-lived RoundTripper for (proxyURL, mode),
// building and caching it on first use. Safe for concurrent callers.
func sharedTransport(proxyURL *url.URL, mode TLSMode) http.RoundTripper {
	key := transportKey{mode: mode}
	if proxyURL != nil {
		key.proxy = proxyURL.String()
	}

	transportMu.Lock()
	defer transportMu.Unlock()
	if rt, ok := transportCache[key]; ok {
		return rt
	}
	rt := buildTransport(proxyURL, mode)
	transportCache[key] = rt
	return rt
}

// buildTransport constructs a fresh, pool-tuned RoundTripper for the given proxy
// and TLS mode. This is the ONLY place transports are created; sharedTransport
// memoizes the result so it is called at most once per (proxy, mode).
func buildTransport(proxyURL *url.URL, mode TLSMode) http.RoundTripper {
	switch mode {
	case TLSChrome:
		return newImpersonatingTransport(proxyURL, helloChrome)
	case TLSSafari:
		return newImpersonatingTransport(proxyURL, helloSafari)
	}
	t := &http.Transport{
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
	}
	if proxyURL != nil {
		t.Proxy = http.ProxyURL(proxyURL)
	}
	return t
}
