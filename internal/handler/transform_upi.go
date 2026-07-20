package handler

import (
	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/crawler/upi"
)

// upiTransform holds the per-request UPI config the transform needs, resolved
// from the registered UPI crawler (global upi_config) plus the tenant's
// intelligence.verified_names_config and website_config gates. nil when UPI is
// not registered (LOCAL_DEV) — the UPI transform steps then no-op.
type upiTransform struct {
	cfg   *upi.Config
	vnCfg *upi.VerifiedNamesConfig
	// phonepeEnabled mirrors website_config[PHONEPE].enabled.
	phonepeEnabled bool
	// bankVerifiedNameDisabled mirrors intelligence.is_disabled_bank_verified_name.
	bankVerifiedNameDisabled bool
	// verifiedNamesEnabled mirrors intelligence.verified_names_config.enabled.
	verifiedNamesEnabled bool
	// clientResponse mirrors the resolved UPI CLIENT_RESPONSE flag (tenant over
	// global). When false, the UPI entry is dropped from account_details after
	// intelligence derivation (remove_upi_responses / drop_all_upi_responses).
	clientResponse bool
}

// transformUPIResponse ports upi_mapper.transform_upi_response for one section's
// account_details map: reshape the raw UPI entry into the client form and derive
// the PHONEPE entry. Operates in-place on accountDetails (keyed by website).
// It runs BEFORE the UPI-derived intelligence extraction and BEFORE the UPI
// entry is (optionally) dropped.
func transformUPIResponse(accountDetails map[string]any, ut *upiTransform) {
	if ut == nil {
		return
	}
	raw, ok := accountDetails["UPI"].(map[string]any)
	if !ok {
		return
	}
	if _, isErr := raw["error"]; isErr {
		accountDetails["UPI"] = map[string]any{"error": true}
		if ut.phonepeEnabled {
			accountDetails["PHONEPE"] = map[string]any{"error": true}
		}
		return
	}
	// Email-derived VPAs (match_type == "email") are exempt from the format
	// filter; collect them from the raw profiles.
	emailVPAIDs := map[string]struct{}{}
	if profs, ok := raw["profiles"].([]map[string]any); ok {
		for _, p := range profs {
			if mt, _ := p["match_type"].(string); mt == "email" {
				if vpa, _ := p["vpa"].(string); vpa != "" {
					emailVPAIDs[vpa] = struct{}{}
				}
			}
		}
	}
	accountDetails["UPI"] = upi.CleanProfileForResponse(raw, ut.vnCfg, emailVPAIDs)

	if ut.phonepeEnabled {
		// PHONEPE existence is derived from the raw profiles whose app_name is
		// PHONEPE (aggregated). We approximate the Python get_agg_upi_profile over
		// the PHONEPE-app subset with a simple any-true rule; a full re-aggregation
		// is unnecessary for the boolean the client sees.
		pe := derivePhonePe(raw)
		accountDetails["PHONEPE"] = pe
	}
}

// derivePhonePe scans the raw UPI profiles for PHONEPE-app entries and returns
// {user_exist: bool} (or {error:true} when none resolve), mirroring the
// get_agg_upi_profile(phonepe_profiles) outcome the Python transform attaches.
func derivePhonePe(raw map[string]any) map[string]any {
	profs, ok := raw["profiles"].([]map[string]any)
	if !ok {
		return map[string]any{"error": true}
	}
	sawTrue, sawFalse := false, false
	for _, p := range profs {
		if p["app_name"] != "PHONEPE" {
			continue
		}
		if _, isErr := p["error"]; isErr {
			continue
		}
		if ue, ok := p["user_exist"].(bool); ok {
			if ue {
				sawTrue = true
			} else {
				sawFalse = true
			}
		}
	}
	if sawTrue {
		return map[string]any{"user_exist": true}
	}
	if sawFalse {
		return map[string]any{"user_exist": false}
	}
	return map[string]any{"error": true}
}

// deriveUPIIntelligence ports the UPI branch of remove_intelligence_data
// (response_mapper.py:29-50): from the (already client-cleaned) UPI entry,
// populate the section's intelligence_data with bank_verified_name,
// verified_names_status, and verified_names, gated by the intelligence config.
// It mutates intel in place and returns it (created if nil).
func deriveUPIIntelligence(accountDetails map[string]any, intel map[string]any, ut *upiTransform) map[string]any {
	if ut == nil {
		return intel
	}
	upiEntry, ok := accountDetails["UPI"].(map[string]any)
	if !ok {
		return intel
	}
	if intel == nil {
		intel = map[string]any{}
	}
	if !ut.bankVerifiedNameDisabled {
		if name, ok := upiEntry["name"].(string); ok && name != "" {
			intel["bank_verified_name"] = name
			intel["verified_names_status"] = "found"
		}
		ue, hasUE := upiEntry["user_exist"].(bool)
		_, hasName := upiEntry["name"]
		if (hasUE && !ue) || !hasName {
			intel["verified_names_status"] = "not-found"
		}
		if _, isErr := upiEntry["error"]; isErr {
			intel["verified_names_status"] = "error"
		}
	}
	if ut.verifiedNamesEnabled {
		if vn, ok := upiEntry["verified_names"]; ok && vn != nil {
			intel["verified_names"] = vn
		} else {
			intel["verified_names"] = []any{}
		}
	}
	return intel
}

// resolveUPITransform builds the upiTransform from the registered UPI crawler
// config and the tenant youConfig. Returns nil when UPI is unavailable.
func resolveUPITransform(cfg *upi.Config, yc *appconfig.YouConfiguration) *upiTransform {
	if cfg == nil {
		return nil
	}
	ut := &upiTransform{
		cfg:            cfg,
		clientResponse: cfg.ClientResponse,
	}
	if yc != nil {
		ut.phonepeEnabled = yc.WebsiteEnabledFlag("PHONEPE")
		ut.bankVerifiedNameDisabled = yc.IntelligenceBool("is_disabled_bank_verified_name")
		ut.verifiedNamesEnabled = yc.VerifiedNamesEnabled()
		ut.vnCfg = toUPIVerifiedNamesConfig(yc.VerifiedNamesConfig())
		if v, ok := yc.WebsiteConfigValue("UPI", "CLIENT_RESPONSE"); ok {
			if b, ok := v.(bool); ok {
				ut.clientResponse = b
			}
		}
	}
	return ut
}

// toUPIVerifiedNamesConfig maps the appconfig raw verified-names config into the
// upi package's shape (kept separate to avoid an appconfig->upi import cycle).
func toUPIVerifiedNamesConfig(r *appconfig.VerifiedNamesRaw) *upi.VerifiedNamesConfig {
	if r == nil {
		return nil
	}
	return &upi.VerifiedNamesConfig{
		Enabled:        r.Enabled,
		Name:           r.Name,
		UpiIDs:         r.UpiIDs,
		Encoding:       r.Encoding,
		AlphanumericID: r.AlphanumericID,
		Source:         r.Source,
	}
}
