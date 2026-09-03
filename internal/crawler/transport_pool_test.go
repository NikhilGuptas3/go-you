package crawler

import (
	"net/url"
	"testing"
	"time"
)

// TestSharedTransportReuse is the core guarantee of the latency fix: the same
// (proxy, mode) returns the SAME transport instance, so the proxy connection
// pool is reused across crawls instead of a fresh CONNECT+TLS handshake per
// request. Different proxies or modes must NOT share a transport (egresses must
// not cross-contaminate; uTLS must stay separate from stock).
func TestSharedTransportReuse(t *testing.T) {
	p1, _ := url.Parse("http://user:pass@proxy-a:8080")
	p2, _ := url.Parse("http://user:pass@proxy-b:8080")

	// same proxy + same mode => identical pointer (reused pool)
	a1 := sharedTransport(p1, TLSDefault)
	a2 := sharedTransport(p1, TLSDefault)
	if a1 != a2 {
		t.Error("same (proxy,mode) must return the SAME transport (pool reuse)")
	}

	// different proxy => different transport (no cross-egress reuse)
	if b := sharedTransport(p2, TLSDefault); b == a1 {
		t.Error("different proxy must NOT share a transport")
	}

	// different TLS mode => different transport (stock vs uTLS)
	if c := sharedTransport(p1, TLSChrome); c == a1 {
		t.Error("different TLS mode must NOT share a transport")
	}
	// same chrome mode reuses
	if c1, c2 := sharedTransport(p1, TLSChrome), sharedTransport(p1, TLSChrome); c1 != c2 {
		t.Error("same (proxy,chrome) must return the SAME transport")
	}

	// nil proxy (direct) is stable and distinct from proxied
	d1 := sharedTransport(nil, TLSDefault)
	d2 := sharedTransport(nil, TLSDefault)
	if d1 != d2 {
		t.Error("nil-proxy (direct) must return the SAME transport")
	}
	if d1 == a1 {
		t.Error("direct and proxied must NOT share a transport")
	}
}

// TestNewHTTPClientJarSharesTransportNotJar confirms two-step crawlers reuse the
// pooled transport but still get an isolated cookie jar per call — so connection
// reuse never leaks cookie state between requests.
func TestNewHTTPClientJarSharesTransportNotJar(t *testing.T) {
	p, _ := url.Parse("http://user:pass@proxy-jar:8080")

	c1 := newHTTPClientJar(p, 5*time.Second, TLSDefault)
	c2 := newHTTPClientJar(p, 5*time.Second, TLSDefault)

	// transport (connection pool) is shared
	if c1.Transport != c2.Transport {
		t.Error("jar clients must share the pooled transport")
	}
	// but jars are distinct instances (no shared cookie state)
	if c1.Jar == nil || c2.Jar == nil {
		t.Fatal("jar clients must each have a cookie jar")
	}
	if c1.Jar == c2.Jar {
		t.Error("each jar client must have its OWN jar (no shared cookies)")
	}
}
