package appconfig

// DisabledWebsitesDefault mirrors constants/config_constants.py:21
// disabled_websites_default — the fallback global disabled list when the
// `global_disabled_websites` config key is absent.
var DisabledWebsitesDefault = []string{
	"MMT", "AIRBNB", "YATRA", "EBAY", "AMAZON", "DISCORD", "SNAPCHAT",
	"ATLASSIAN", "ZEE5", "ZOMATO", "EVERNOTE", "ZERODHA",
}

// TokenPoolSites is the set of sites that require the token pool
// (WEBSITE_TOKEN_POOL_CONFIG_DEFAULT). These are OUT of scope for go-you (no
// token infrastructure) and are excluded from every crawl set as a safety net,
// independent of tenant config. Populated as crawlers are triaged in Phase 2;
// the exclusion is also enforced by simply never registering these crawlers,
// but keeping the set here makes the filter explicit and testable.
var TokenPoolSites = map[string]struct{}{
	"APPLE": {}, "EVENTBRITE": {}, "EVERNOTE": {}, "DIGILOCKER": {},
	"ZERODHA": {}, "BPCL_GAS": {}, "PAYU_UPI": {}, "SWIGGY": {},
	"NETFLIX": {}, "LINKEDIN": {}, "MICROSOFT": {}, "SNAPDEAL": {},
	"SAMSUNG": {}, "ZOHO": {}, "EASYGOSMS": {},
	// NOTE: TWITTER is token-pool for the PHONE flow but token-free for EMAIL
	// (email_available.json). It is therefore NOT listed here; the email crawler
	// registers TWITTER and the phone flow simply has no TWITTER crawler.
}

// CrawlSet returns the websites to crawl for kind ("phone"/"email") for a
// tenant, replicating real_time_data_service.get_persona_by_type's site
// selection (real_time_data_service.py:184-192) under the no-cloud constraint:
//
//	get_websites(kind)  ∩  enabled(tenant)  −  global_disabled  −  token_pool
//
// available is the set of crawler websites registered for this kind (go-you's
// "factory" — the registered Crawler implementations). Because go-you is
// stateless, websites_not_present() returns everything (no cache skip) and
// partial_data_present() involves only cache/token sites (LINKEDIN/SKYPE/UPI/
// WHATSAPP), so the crawl set is exactly the filtered enabled set.
func CrawlSet(kind string, available []string, tenant *YouConfiguration, globalDisabled []string) []string {
	disabled := make(map[string]struct{}, len(globalDisabled))
	for _, d := range globalDisabled {
		disabled[d] = struct{}{}
	}
	out := make([]string, 0, len(available))
	for _, site := range available {
		if !tenant.IsWebsiteEnabled(site, kind) {
			continue
		}
		if _, off := disabled[site]; off {
			continue
		}
		if _, tok := TokenPoolSites[site]; tok {
			continue
		}
		out = append(out, site)
	}
	return out
}

// GlobalDisabled resolves the global disabled list from the Fetcher, falling
// back to DisabledWebsitesDefault. It coerces the JSON array (which decodes as
// []any) into []string, dropping non-string entries defensively.
func GlobalDisabled(f *Fetcher) []string {
	v := f.Get("global_disabled_websites", nil)
	arr, ok := v.([]any)
	if !ok {
		return DisabledWebsitesDefault
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
