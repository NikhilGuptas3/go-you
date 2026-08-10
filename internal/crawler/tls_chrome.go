package crawler

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"

	utls "github.com/refraction-networking/utls"
)

// helloID selects which browser ClientHello uTLS presents. Python's
// base_api_spider does `impersonate = "safari" if self.ID == QUORA else "chrome"`
// for curl_cffi; we mirror that with two presets.
type helloID int

const (
	helloChrome helloID = iota
	helloSafari
)

func (h helloID) utlsID() utls.ClientHelloID {
	if h == helloSafari {
		return utls.HelloSafari_Auto
	}
	return utls.HelloChrome_Auto
}

func (h helloID) name() string {
	if h == helloSafari {
		return "safari"
	}
	return "chrome"
}

// newImpersonatingTransport returns an http.RoundTripper that presents a
// browser-like TLS ClientHello (JA3) via uTLS, for the curl_cffi-impersonating
// spiders that fingerprint and block Go's stock hello. hello picks Chrome (most
// sites) or Safari (QUORA only).
//
// It wires uTLS into an http.Transport's DialTLSContext: the TCP dial is
// standard, then the connection is wrapped in a uTLS UClient using the chosen
// preset and handshaked with ALPN forced to HTTP/1.1. proxyURL, when set, is
// honored for the underlying TCP dial via http.ProxyURL (CONNECT for https
// targets is handled by the transport).
func newImpersonatingTransport(proxyURL *url.URL, hello helloID) http.RoundTripper {
	t := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialImpersonatingTLS(ctx, network, addr, hello)
		},
	}
	if proxyURL != nil {
		t.Proxy = http.ProxyURL(proxyURL)
		// When a proxy is set, DialTLSContext is bypassed for the CONNECT tunnel;
		// the transport still needs a TLSClientConfig for the inner handshake. We
		// fall back to stock TLS through the proxy in that case — uTLS-over-proxy
		// needs a custom CONNECT dialer, deferred until a proxied uTLS site is
		// actually observed to be blocked.
		t.DialTLSContext = nil
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return t
}

// dialImpersonatingTLS performs a TCP dial then a uTLS handshake with the chosen
// browser fingerprint against addr (host:port), splitting the SNI host from the
// port for the handshake config.
//
// The paired http.Transport speaks only HTTP/1.1 over the returned conn, so we
// force the ALPN offer to "http/1.1" — otherwise the server negotiates h2 and
// the HTTP/1.1 transport misreads the h2 frames. We build the preset's spec and
// overwrite its ALPN extension to h1-only, which keeps the browser JA3
// fingerprint while constraining the protocol.
func dialImpersonatingTLS(ctx context.Context, network, addr string, hello helloID) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%s-tls: split %q: %w", hello.name(), addr, err)
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	spec, err := utls.UTLSIdToSpec(hello.utlsID())
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("%s-tls: spec: %w", hello.name(), err)
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	uconn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := uconn.ApplyPreset(&spec); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("%s-tls: apply preset %q: %w", hello.name(), host, err)
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("%s-tls: handshake %q: %w", hello.name(), host, err)
	}
	return uconn, nil
}
