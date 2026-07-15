package meta

import (
	"context"
	"crypto/sha1"
	"hash"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"time"

	utls "github.com/refraction-networking/utls"
)

// sha1New is the PBKDF2 hash constructor (referenced by pbkdf2.Key).
func sha1New() hash.Hash { return sha1.New() }

// rand3digits returns a 3-char string of digits 1-9 (matches the Python
// random.choices("123456789", k=3) used in the Jio postpaid URL).
func rand3digits() string {
	const digits = "123456789"
	b := make([]byte, 3)
	for i := range b {
		b[i] = digits[rand.Intn(len(digits))]
	}
	return string(b)
}

// doHTTP sends a request with ctx, optional proxy, and optional Chrome uTLS
// fingerprint (chromeTLS=true). Returns (status, body, err). This mirrors the
// crawler package's client construction; kept separate to avoid a cross-package
// dependency between meta and crawler.
func doHTTP(ctx context.Context, proxyURL *url.URL, timeout time.Duration, chromeTLS bool,
	method, rawURL string, body io.Reader, headers map[string]string) (int, []byte, error) {

	var transport http.RoundTripper
	if chromeTLS && proxyURL == nil {
		transport = &http.Transport{DialTLSContext: dialChromeTLS}
	} else {
		t := &http.Transport{}
		if proxyURL != nil {
			t.Proxy = http.ProxyURL(proxyURL)
		}
		transport = t
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

// dialChromeTLS mirrors crawler.dialChromeTLS: TCP dial + uTLS Chrome handshake
// with ALPN forced to http/1.1.
func dialChromeTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	uconn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := uconn.ApplyPreset(&spec); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return uconn, nil
}
