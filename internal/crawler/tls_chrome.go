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

// newChromeTransport returns an http.RoundTripper that presents a Chrome-like
// TLS ClientHello (JA3) via uTLS, for the curl_cffi-impersonating spiders
// (Amazon, the JSSO family, IRCTC, Freecharge, ...) that fingerprint and block
// Go's stock hello.
//
// It wires uTLS into an http.Transport's DialTLSContext: the TCP dial is
// standard, then the connection is wrapped in a uTLS UClient using the
// HelloChrome_Auto preset and handshaked with ALPN so HTTP/1.1 and h2 are both
// offered. proxyURL, when set, is honored for the underlying TCP dial via
// http.ProxyURL (CONNECT for https targets is handled by the transport).
func newChromeTransport(proxyURL *url.URL) http.RoundTripper {
	t := &http.Transport{
		// ForceAttemptHTTP2 is left false: the ALPN result from the uTLS
		// handshake decides the protocol, and we only wrap with net/http's
		// HTTP/1.1 path here for simplicity and broad compatibility.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialChromeTLS(ctx, network, addr)
		},
	}
	if proxyURL != nil {
		t.Proxy = http.ProxyURL(proxyURL)
		// When a proxy is set, DialTLSContext is bypassed for the CONNECT
		// tunnel; the transport still needs a TLSClientConfig for the inner
		// handshake. We fall back to stock TLS through the proxy in that case —
		// uTLS-over-proxy needs a custom CONNECT dialer, deferred until a proxied
		// uTLS site is actually observed to be blocked.
		t.DialTLSContext = nil
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return t
}

// dialChromeTLS performs a TCP dial then a uTLS Chrome handshake against addr
// (host:port). It splits the SNI host from the port for the handshake config.
//
// The paired http.Transport speaks only HTTP/1.1 over the returned conn, so we
// must force the ALPN offer to "http/1.1" — otherwise the server negotiates h2
// and the HTTP/1.1 transport misreads the h2 frames as a malformed response.
// (curl_cffi's default for these spiders is effectively HTTP/1.1 too.) We build
// a Chrome ClientHello spec and overwrite its ALPN extension to h1-only, which
// keeps the Chrome JA3 fingerprint while constraining the protocol.
func dialChromeTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("chrome-tls: split %q: %w", addr, err)
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	// Build a Chrome ClientHello spec and rewrite its ALPN to advertise only
	// "http/1.1", then apply it via HelloCustom so the override actually takes
	// effect (applying a preset onto a HelloChrome_Auto UConn does not stick).
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("chrome-tls: spec: %w", err)
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	uconn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := uconn.ApplyPreset(&spec); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("chrome-tls: apply preset %q: %w", host, err)
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("chrome-tls: handshake %q: %w", host, err)
	}
	return uconn, nil
}
