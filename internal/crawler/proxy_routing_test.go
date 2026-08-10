package crawler

import (
	"net/url"
	"testing"
)

func TestProxyFor(t *testing.T) {
	def, _ := url.Parse("http://default-proxy:8080")
	nimble, _ := url.Parse("http://nimble:7000")

	// NimbleSites route to nimble when it's set.
	if got := ProxyFor("MICROSOFT", def, nimble); got != nimble {
		t.Errorf("MICROSOFT should use nimble, got %v", got)
	}
	if got := ProxyFor("APPLE", def, nimble); got != nimble {
		t.Errorf("APPLE should use nimble, got %v", got)
	}
	// Non-Nimble sites always use the default.
	if got := ProxyFor("TWITTER", def, nimble); got != def {
		t.Errorf("TWITTER should use default, got %v", got)
	}
	if got := ProxyFor("SNAPDEAL", def, nimble); got != def {
		t.Errorf("SNAPDEAL should use default, got %v", got)
	}
	// nil nimble => NimbleSites fall back to the default (no behavior change).
	if got := ProxyFor("MICROSOFT", def, nil); got != def {
		t.Errorf("MICROSOFT with nil nimble should fall back to default, got %v", got)
	}
	// nil default + nil nimble => nil (direct).
	if got := ProxyFor("TWITTER", nil, nil); got != nil {
		t.Errorf("expected nil (direct), got %v", got)
	}
}
