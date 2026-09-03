package upi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// source is one UPI VPA-validation source. Most sources probe one VPA per suffix
// (validate); the fan-out across the assigned suffix set is shared in
// runSource. Sources that don't follow the per-suffix shape (PHONEPE emulator)
// implement runSource-equivalent behavior via the SelfDriven marker.
type source interface {
	// name is the source constant (e.g. "CONFIRMTKT_UPI").
	name() string
	// validate probes one suffix and returns the parsed profile. suffix is a
	// bank handle (e.g. "ybl") or "COMMON". national is the 10-digit number.
	// A nil-return-with-nil-error is not used; return an error for no-match.
	validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error)
}

// selfDrivenSource bypasses the per-suffix fan-out and produces the full profile
// list itself (PHONEPE emulator: single request, international id, no suffix
// iteration). BRB also self-drives (session bootstrap + single COMMON probe).
type selfDrivenSource interface {
	source
	run(ctx context.Context, national, intl string, sm SourceMeta, proxyURL *url.URL) []*Profile
}

// runSource ports UPIBase.get_login_response: fan out across the assigned
// suffixes (intersected with supported suffixes), collecting per-suffix
// profiles. A source-level error for a suffix yields an error-marked profile so
// aggregation can see it, matching parse_login_response. When ReturnAll is
// false the caller may short-circuit on the first user_exist=true; we always
// collect all here (the aggregator applies return_all semantics).
func runSource(ctx context.Context, src source, national, intl string, sm SourceMeta, proxyURL *url.URL) []*Profile {
	if sd, ok := src.(selfDrivenSource); ok {
		return sd.run(ctx, national, intl, sm, proxyURL)
	}

	// Intersect requested suffixes with supported ones (upi_base does this).
	var suffixes []string
	for _, s := range sm.SuffixList {
		if s == commonSuffixConstant || isSupportedSuffix(s) {
			suffixes = append(suffixes, s)
		}
	}
	if len(suffixes) == 0 {
		return nil
	}

	type res struct {
		p *Profile
	}
	out := make([]*Profile, 0, len(suffixes))
	ch := make(chan res, len(suffixes))
	for _, suffix := range suffixes {
		go func(suffix string) {
			p, err := src.validate(ctx, national, suffix, sm, proxyURL)
			if err != nil || p == nil {
				ch <- res{p: &Profile{
					Source: src.name(),
					Flow:   flowForSuffix(suffix),
					Suffix: suffix,
					Err:    errString(err),
				}}
				return
			}
			ch <- res{p: p}
		}(suffix)
	}
	for range suffixes {
		r := <-ch
		out = append(out, r.p)
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return "error"
	}
	return err.Error()
}

// --- shared HTTP for UPI sources ---

// UPI transport reuse. Like the main crawler package (see
// crawler/transport_pool.go), UPI sources used to build a fresh *http.Transport
// per request, re-paying the proxy CONNECT + TLS handshake every time. Cache one
// long-lived transport per proxy so the tunnel is reused across VPA probes (a
// single persona fans out many UPI probes through the same proxy).
const (
	upiMaxIdleConns        = 128
	upiMaxIdleConnsPerHost = 64
	upiIdleConnTimeout     = 90 * time.Second
)

var (
	upiTransportMu    sync.Mutex
	upiTransportCache = map[string]*http.Transport{}
)

// httpClient builds a proxy-aware client with the given timeout (stock TLS; UPI
// sources are not JA3-fingerprinted in the sources we port). The *http.Client is
// per-call but its transport is shared per proxy, so connections pool.
func httpClient(proxyURL *url.URL, timeout time.Duration) *http.Client {
	return &http.Client{Transport: sharedUPITransport(proxyURL), Timeout: timeout}
}

// sharedUPITransport returns the long-lived transport for proxyURL, building it
// once. A nil proxy (direct) is keyed by the empty string.
func sharedUPITransport(proxyURL *url.URL) *http.Transport {
	key := ""
	if proxyURL != nil {
		key = proxyURL.String()
	}
	upiTransportMu.Lock()
	defer upiTransportMu.Unlock()
	if tr, ok := upiTransportCache[key]; ok {
		return tr
	}
	tr := &http.Transport{
		MaxIdleConns:        upiMaxIdleConns,
		MaxIdleConnsPerHost: upiMaxIdleConnsPerHost,
		IdleConnTimeout:     upiIdleConnTimeout,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	upiTransportCache[key] = tr
	return tr
}

// do sends a request and returns status + body. body may be nil.
func do(ctx context.Context, client *http.Client, method, rawURL string, body io.Reader, headers map[string]string) (int, []byte, error) {
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

// vpaFor builds "<national10>@<suffix>".
func vpaFor(national, suffix string) string { return national + "@" + suffix }

// trueP / falseP build user_exist profiles for a source+suffix.
func trueP(src, suffix, name, vpa string) *Profile {
	t := true
	appName := commonSuffixConstant
	if suffix != commonSuffixConstant {
		appName = appNameForSuffix(suffix)
	}
	return &Profile{Source: src, Flow: flowForSuffix(suffix), Suffix: suffix, AppName: appName, UserExist: &t, Name: name, VPA: vpa}
}

func falseP(src, suffix string) *Profile {
	f := false
	appName := commonSuffixConstant
	if suffix != commonSuffixConstant {
		appName = appNameForSuffix(suffix)
	}
	return &Profile{Source: src, Flow: flowForSuffix(suffix), Suffix: suffix, AppName: appName, UserExist: &f}
}

var _ = context.Background
