package crawler

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

// Pin the helloID → uTLS preset mapping so the QUORA-Safari special case (and
// the Chrome default) can't silently regress. Python: impersonate = "safari" if
// self.ID == QUORA else "chrome".
func TestHelloIDPresets(t *testing.T) {
	if helloChrome.utlsID() != utls.HelloChrome_Auto {
		t.Errorf("helloChrome should map to HelloChrome_Auto")
	}
	if helloSafari.utlsID() != utls.HelloSafari_Auto {
		t.Errorf("helloSafari should map to HelloSafari_Auto")
	}
	if helloChrome.name() != "chrome" || helloSafari.name() != "safari" {
		t.Errorf("hello names wrong: %q/%q", helloChrome.name(), helloSafari.name())
	}
}

// QUORA is the one site that must use Safari; everything else uses Chrome (or
// stock). Guard the mode each client-builder path selects by constructing the
// clients (no network) and asserting they build without panic — the concrete
// fingerprint is exercised by the live token probe, not a unit test.
func TestNewHTTPClientTLSModes(t *testing.T) {
	if c := newHTTPClientTLS(nil, 0, TLSSafari); c == nil || c.Transport == nil {
		t.Fatal("TLSSafari client not built")
	}
	if c := newHTTPClientTLS(nil, 0, TLSChrome); c == nil || c.Transport == nil {
		t.Fatal("TLSChrome client not built")
	}
	if c := newHTTPClientTLS(nil, 0, TLSDefault); c == nil {
		t.Fatal("TLSDefault client not built")
	}
}
