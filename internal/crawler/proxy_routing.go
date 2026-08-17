package crawler

import "net/url"

// Egress names the set of upstream proxies go-you can crawl through and the
// per-site routing between them. It replaces the earlier two-field
// (default+nimble) model so a third, fourth, … vendor can be added without
// changing any signature: register a proxy under a name, then point sites at
// that name in SiteEgress.
//
// The POC still uses static proxies (no Redis rotating pool). Default may be nil
// => crawl direct. Named proxies are optional; a site routed to a name that is
// unset falls back to Default, so an unconfigured vendor never breaks crawling —
// it just degrades to the default egress.
type Egress struct {
	// Default is the plain egress every site uses unless SiteEgress routes it
	// elsewhere. Typically the BrightData/Decodo residential proxy. nil => direct.
	Default *url.URL
	// Named holds vendor egresses keyed by the names used in SiteEgress
	// ("nimble", "smartproxy", …). A nil/absent entry falls back to Default.
	Named map[string]*url.URL
}

// SiteEgress routes specific websites to a named egress in Egress.Named. A site
// absent here uses Egress.Default. This is go-you's port of hey-you's per-site
// proxy pinning — vendor_choices=["NIMBLE"] and "proxy_zone": ProxyZone.USA —
// collapsed onto go-you's named-egress model:
//
//   - MICROSOFT, APPLE: vendor-pinned to NIMBLE (their login endpoints 403 the
//     default egress). Verified they mint tokens through Nimble.
//   - QUORA: zone-pinned to USA (social/quora/quora.py "proxy_zone":
//     ProxyZone.USA). Quora 403s a non-US egress on the home fetch regardless of
//     TLS fingerprint; routed to the smartproxy US exit.
//
// The name a site maps to is a logical vendor slot, not a hardcoded provider —
// which concrete proxy fills "nimble" or "smartproxy" is set at wiring time from
// the environment. Sites not listed (token-pool-only Python entries go-you hasn't
// ported) are added here when observed to need a non-default egress.
var SiteEgress = map[string]string{
	"MICROSOFT": "nimble",
	"APPLE":     "nimble",
	"QUORA":     "smartproxy",
}

// ProxyForSite returns the proxy a website should egress through: the named
// egress SiteEgress routes it to (when that name is configured in e.Named), else
// e.Default. Nil-safe: a nil *Egress or an unset named proxy both fall back to
// Default (nil => direct), so behavior degrades gracefully when a vendor is not
// configured.
func (e *Egress) ProxyForSite(website string) *url.URL {
	if e == nil {
		return nil
	}
	if name, ok := SiteEgress[website]; ok {
		if p := e.Named[name]; p != nil {
			return p
		}
	}
	return e.Default
}
