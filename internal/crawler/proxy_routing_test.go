package crawler

import (
	"net/url"
	"testing"
)

func TestEgressProxyForSite(t *testing.T) {
	def, _ := url.Parse("http://default-proxy:8080")
	nimble, _ := url.Parse("http://nimble:7000")
	smart, _ := url.Parse("http://smartproxy:10000")

	e := &Egress{
		Default: def,
		Named:   map[string]*url.URL{"nimble": nimble, "smartproxy": smart},
	}

	// Nimble-routed sites use the nimble egress.
	if got := e.ProxyForSite("MICROSOFT"); got != nimble {
		t.Errorf("MICROSOFT should use nimble, got %v", got)
	}
	if got := e.ProxyForSite("APPLE"); got != nimble {
		t.Errorf("APPLE should use nimble, got %v", got)
	}
	// QUORA is pinned to the US egress (hey-you ProxyZone.USA) via smartproxy.
	if got := e.ProxyForSite("QUORA"); got != smart {
		t.Errorf("QUORA should use smartproxy (US egress), got %v", got)
	}
	// Unrouted sites use the default.
	if got := e.ProxyForSite("TWITTER"); got != def {
		t.Errorf("TWITTER should use default, got %v", got)
	}
	if got := e.ProxyForSite("SNAPDEAL"); got != def {
		t.Errorf("SNAPDEAL should use default, got %v", got)
	}
}

func TestEgressFallbacks(t *testing.T) {
	def, _ := url.Parse("http://default-proxy:8080")

	// A routed site whose named egress is unset falls back to Default.
	e := &Egress{Default: def} // no Named map at all
	if got := e.ProxyForSite("MICROSOFT"); got != def {
		t.Errorf("MICROSOFT with no nimble configured should fall back to default, got %v", got)
	}
	if got := e.ProxyForSite("QUORA"); got != def {
		t.Errorf("QUORA with no smartproxy configured should fall back to default, got %v", got)
	}

	// Named present but the specific slot missing => Default.
	e2 := &Egress{Default: def, Named: map[string]*url.URL{}}
	if got := e2.ProxyForSite("APPLE"); got != def {
		t.Errorf("APPLE with empty Named should fall back to default, got %v", got)
	}

	// nil default + unrouted => nil (direct).
	e3 := &Egress{}
	if got := e3.ProxyForSite("TWITTER"); got != nil {
		t.Errorf("expected nil (direct), got %v", got)
	}

	// nil *Egress is safe => nil (direct).
	var e4 *Egress
	if got := e4.ProxyForSite("MICROSOFT"); got != nil {
		t.Errorf("nil Egress should return nil, got %v", got)
	}
}
