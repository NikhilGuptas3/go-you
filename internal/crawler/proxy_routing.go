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
// absent here uses Egress.Default (BrightData, pinned to India — see
// deploy/secret.yaml PROXY_URL). The default India egress is what most India
// e-commerce/OTT sites (Flipkart, Snapdeal, Gaana, Pinterest, …) require to
// return a session — so those sites are deliberately NOT listed and use it.
//
// Only the two sites we have LIVE-VERIFIED need a non-India vendor egress appear
// here (Option B — minimal, evidence-based, not the full vendor_choices map):
//
//   - APPLE -> "nimble" : Apple's login 403s BrightData; mints through Nimble.
//     Verified (Apple 3/3 tokens via Nimble).
//   - MICROSOFT -> "smartproxy" : Microsoft's authorize page 403s BrightData;
//     works through the US Smartproxy exit. Verified (8/8 Check runs via the US
//     egress after the token-retry fix).
//
// NOT routed here on purpose, despite the vendor_choices map suggesting a US
// vendor, because a POC proved routing them to a US/other egress does NOT help:
//   - SNAPDEAL/GAANA/PINTEREST: need the INDIA default (Snapdeal 200+cookies on
//     brd-in, 403 on US/Smartproxy). Leave them on the India default.
//   - QUORA/ADOBE: 403 on EVERY egress tested (BrightData any country, Nimble,
//     Smartproxy). No proxy route fixes them — they need residential rotation, a
//     separate problem. Routing them anywhere is pointless, so they stay default.
//
// The name a site maps to is a logical vendor slot; which concrete proxy fills
// "nimble"/"smartproxy" is set at wiring time from NIMBLE_PROXY_URL /
// SMARTPROXY_URL. An unset slot falls back to Default, so a site here still
// crawls (via the India BrightData) if its vendor proxy isn't configured.
var SiteEgress = map[string]string{
	"APPLE":     "nimble",
	"MICROSOFT": "smartproxy",
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
