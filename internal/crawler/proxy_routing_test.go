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

	// Option B: only APPLE (nimble) and MICROSOFT (smartproxy) are routed — the
	// two live-verified to need a non-India exit. Everything else uses the India
	// default.
	if got := e.ProxyForSite("APPLE"); got != nimble {
		t.Errorf("APPLE should use nimble, got %v", got)
	}
	if got := e.ProxyForSite("MICROSOFT"); got != smart {
		t.Errorf("MICROSOFT should use smartproxy, got %v", got)
	}
	// India sites (Snapdeal/Gaana/Pinterest) must use the India DEFAULT, not a
	// US vendor — the POC proved they 403 on US and need brd-in.
	if got := e.ProxyForSite("SNAPDEAL"); got != def {
		t.Errorf("SNAPDEAL should use default (India), got %v", got)
	}
	if got := e.ProxyForSite("GAANA"); got != def {
		t.Errorf("GAANA should use default (India), got %v", got)
	}
	if got := e.ProxyForSite("PINTEREST"); got != def {
		t.Errorf("PINTEREST should use default (India), got %v", got)
	}
	// QUORA 403s every egress => stays on default (routing it is pointless).
	if got := e.ProxyForSite("QUORA"); got != def {
		t.Errorf("QUORA should use default, got %v", got)
	}
	if got := e.ProxyForSite("FLIPKART"); got != def {
		t.Errorf("FLIPKART should use default, got %v", got)
	}
}

func TestEgressFallbacks(t *testing.T) {
	def, _ := url.Parse("http://default-proxy:8080")

	// A routed site whose named egress is unset falls back to Default.
	e := &Egress{Default: def} // no Named map at all
	if got := e.ProxyForSite("MICROSOFT"); got != def {
		t.Errorf("MICROSOFT with no smartproxy configured should fall back to default, got %v", got)
	}
	if got := e.ProxyForSite("APPLE"); got != def {
		t.Errorf("APPLE with no nimble configured should fall back to default, got %v", got)
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
