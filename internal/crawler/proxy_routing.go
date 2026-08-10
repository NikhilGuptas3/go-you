package crawler

import "net/url"

// NimbleSites are the websites hey-you pins to the NIMBLE proxy vendor
// (WEBSITE_TOKEN_POOL_CONFIG_DEFAULT[...].vendor_choices = ["NIMBLE"]) whose
// login endpoints 403 the default BrightData egress. Verified against the live
// endpoints: MICROSOFT and APPLE mint tokens through Nimble but not BrightData.
// Other Nimble-tagged sites in Python (e.g. token-pool-only ones go-you hasn't
// ported) are omitted until observed to need it.
var NimbleSites = map[string]struct{}{
	"MICROSOFT": {},
	"APPLE":     {},
}

// ProxyFor returns the proxy a given website should egress through: the Nimble
// proxy for NimbleSites (when a Nimble proxy is configured), else the default.
// A nil nimble falls back to def, so an unset NIMBLE_PROXY_URL leaves behavior
// exactly as before (everything on the default proxy).
func ProxyFor(website string, def, nimble *url.URL) *url.URL {
	if nimble != nil {
		if _, ok := NimbleSites[website]; ok {
			return nimble
		}
	}
	return def
}
