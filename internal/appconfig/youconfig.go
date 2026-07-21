package appconfig

import (
	"encoding/json"
	"fmt"
)

// YouConfiguration is the parsed per-tenant `youConfig` object (the tenant's
// tenantapp.config column, key "youConfig"). It mirrors the gate readers in
// service/config_service.py. Only the subset the /v1/persona path consults is
// modeled; unknown keys are ignored.
//
// Under the no-cloud constraint several Python flags are forced off regardless
// of tenant config (caching, static, linked_data) — see the Is* accessors,
// which hardcode those to match the stateless HTTP-only behavior.
type YouConfiguration struct {
	// raw retains the decoded object so nested/optional lookups (postpaid,
	// domain_intelligence, etc.) can be read without modeling every branch.
	raw map[string]any

	Websites map[string]WebsiteEntry `json:"websites"`

	Breach     bool `json:"breach"`
	PhoneMeta  bool `json:"phone_meta"`
	EmailMeta  bool `json:"email_meta"`
	Prediction bool `json:"prediction"`

	RequestTimeout []float64 `json:"request_timeout"`

	// CommonIntelligence / Intelligence / EmailInfo / PhoneInfo are read as raw
	// maps because their shapes are large and only partially consumed; typed
	// accessors below pull the specific gates.
	CommonIntelligence map[string]any `json:"common_intelligence"`
	Intelligence       map[string]any `json:"intelligence"`
	EmailInfo          map[string]any `json:"email_info"`
	PhoneInfo          map[string]any `json:"phone_info"`
}

// WebsiteEntry is one entry of youConfig.websites (config_service.py:46-50).
// A site is enabled for a type iff Enabled && the per-type flag is true.
//
// The per-type flags (phone_enabled/email_enabled) and client_response are
// POINTERS so we can distinguish "absent" from "explicitly false". Python reads
// the fully-merged tenant config (data/dao/config_utils.py:merge_and_delta
// overlays the tenant youConfig onto the init_root_config defaults at onboard
// time and stores the whole thing), so in the DB every site carries these keys.
// A tenant that sends only {"enabled": true} — the exposed "delta" view — relies
// on the root defaults being merged in. go-you replicates those defaults here so
// it behaves correctly whether it reads the merged config OR a bare delta: an
// absent per-type flag defaults to the root value (phone_enabled=true,
// email_enabled=true, client_response=true for every crawler-backed site; the
// deviating sites LINKEDIN/WHATSAPP/UPI are all token-pool and never crawled).
type WebsiteEntry struct {
	Enabled      bool  `json:"enabled"`
	PhoneEnabled *bool `json:"phone_enabled"`
	EmailEnabled *bool `json:"email_enabled"`
	// ClientResponse controls whether the site appears in the client response
	// (transform: remove_client_response_disabled_websites). Defaults to true
	// when the key is absent; use ClientResponse(site) to read with that default.
	ClientResponse *bool `json:"client_response"`
	// Cache is honored in Python but forced off here (no cache); retained so the
	// struct round-trips the tenant config faithfully.
	Cache *bool `json:"cache"`
}

// ParseYouConfig decodes the tenant config JSON string and extracts youConfig.
// Mirrors config_service.get_you_config (config_service.py:15-20): the config
// column is a JSON object that must contain a "youConfig" key.
func ParseYouConfig(tenantConfigJSON string) (*YouConfiguration, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tenantConfigJSON), &top); err != nil {
		return nil, fmt.Errorf("tenant config not valid JSON: %w", err)
	}
	rawYou, ok := top["youConfig"]
	if !ok {
		return nil, fmt.Errorf("youConfig not present for tenant")
	}
	var yc YouConfiguration
	if err := json.Unmarshal(rawYou, &yc); err != nil {
		return nil, fmt.Errorf("youConfig not valid: %w", err)
	}
	// Keep the raw map too, for nested optional lookups.
	if err := json.Unmarshal(rawYou, &yc.raw); err != nil {
		return nil, fmt.Errorf("youConfig not an object: %w", err)
	}
	return &yc, nil
}

// IsWebsiteEnabled mirrors config_service.is_website_enabled: the site exists in
// websites, Enabled is true, and the per-type flag is true. kind is "phone" or
// "email".
//
// Python reads a fully-merged config where phone_enabled/email_enabled always
// exist (root default = true for crawler-backed sites). go-you replicates that:
// an ABSENT per-type flag defaults to true, so a tenant delta of just
// {"enabled": true} enables the site for both types — exactly as the merged
// config would. Only an explicit "phone_enabled": false / "email_enabled": false
// disables that type.
func (yc *YouConfiguration) IsWebsiteEnabled(website, kind string) bool {
	e, ok := yc.Websites[website]
	if !ok || !e.Enabled {
		return false
	}
	switch kind {
	case "phone":
		return e.PhoneEnabled == nil || *e.PhoneEnabled
	case "email":
		return e.EmailEnabled == nil || *e.EmailEnabled
	default:
		return false
	}
}

// ClientResponse reports whether a site is included in the client response.
// Absent key => true (Python treats missing client_response as visible).
func (yc *YouConfiguration) ClientResponse(website string) bool {
	e, ok := yc.Websites[website]
	if !ok || e.ClientResponse == nil {
		return true
	}
	return *e.ClientResponse
}

// IsPhoneInfoEnabled mirrors config_service.is_phone_info_enabled: absent =>
// true; only an explicit enabled:false disables it. When false the whole
// phone-info lane (operator/circle/postpaid/revocations) is suppressed and
// phone_meta is omitted from the response.
func (yc *YouConfiguration) IsPhoneInfoEnabled() bool {
	pi, ok := yc.PhoneInfo["enabled"]
	if !ok {
		return true
	}
	b, ok := pi.(bool)
	return !ok || b
}

// isEmailInfoEnabled mirrors config_service.is_email_info_enabled (absent => true).
func (yc *YouConfiguration) isEmailInfoEnabled() bool {
	ei, ok := yc.EmailInfo["enabled"]
	if !ok {
		return true
	}
	b, ok := ei.(bool)
	return !ok || b
}

// IsPostpaidEnabled mirrors config_service.is_postpaid_enabled: phone_info
// enabled AND phone_info.postpaid == true.
func (yc *YouConfiguration) IsPostpaidEnabled() bool {
	return yc.IsPhoneInfoEnabled() && boolAt(yc.PhoneInfo, "postpaid")
}

// IsDndEnabled mirrors config_service.is_dnd_status_enabled. (dnd is always OUT
// in go-you — EasyGoSms is token-pool — but the gate is modeled for parity.)
func (yc *YouConfiguration) IsDndEnabled() bool {
	return yc.IsPhoneInfoEnabled() && boolAt(yc.PhoneInfo, "dnd_status")
}

// IsDomainIntelligenceEnabled mirrors config_service.is_domain_intelligence_enabled:
// email_info enabled AND email_info.domain_intelligence.enabled == true.
func (yc *YouConfiguration) IsDomainIntelligenceEnabled() bool {
	if !yc.isEmailInfoEnabled() {
		return false
	}
	di, ok := yc.EmailInfo["domain_intelligence"].(map[string]any)
	if !ok {
		return false
	}
	return boolAt(di, "enabled")
}

// IsCommonIntelligenceEnabled reports common_intelligence.enabled == true.
func (yc *YouConfiguration) IsCommonIntelligenceEnabled() bool {
	return boolAt(yc.CommonIntelligence, "enabled")
}

// OnboardingFraudOutputKey returns the tenant's configured output_key_name for
// the onboarding_fraud_detection score, and whether the score-rename should run.
// Mirrors response_mapper.cleanup_prediction (response_mapper.py:259-275):
// rename runs only when common_intelligence.enabled AND score.enabled AND
// score.onboarding_fraud_detection.enabled are all true. The returned key
// applies both to the intelligence_data.score rename and to the prediction
// reshape; it defaults to "identity_fraud_score" (and "identity_fraud" is
// normalized to "identity_fraud_score").
func (yc *YouConfiguration) OnboardingFraudOutputKey() (key string, renameEnabled bool) {
	key = "identity_fraud_score"
	ci := yc.CommonIntelligence
	if !boolAt(ci, "enabled") {
		return key, false
	}
	score, ok := ci["score"].(map[string]any)
	if !ok || !boolAt(score, "enabled") {
		return key, false
	}
	ofd, ok := score["onboarding_fraud_detection"].(map[string]any)
	if !ok || !boolAt(ofd, "enabled") {
		return key, false
	}
	if name, ok := ofd["output_key_name"].(string); ok && name != "" {
		if name == "identity_fraud" {
			name = "identity_fraud_score"
		}
		key = name
	}
	return key, true
}

// RequestTimeoutFor mirrors config_service.get_request_timeout: pick the
// timeout for the (1-based) request count, clamped to the list length. Returns
// (0, false) when no list is configured, so the caller uses its default.
func (yc *YouConfiguration) RequestTimeoutFor(requestCount int) (float64, bool) {
	n := len(yc.RequestTimeout)
	if n == 0 {
		return 0, false
	}
	idx := requestCount
	if idx > n {
		idx = n
	}
	if idx < 1 {
		idx = 1
	}
	return yc.RequestTimeout[idx-1], true
}

// --- UPI / verified-names accessors (used by the transform) ---

// WebsiteEnabledFlag reports websites[site].enabled == true (the site-level
// enable, independent of the per-type flags). Used for PHONEPE gating in the UPI
// transform.
func (yc *YouConfiguration) WebsiteEnabledFlag(site string) bool {
	e, ok := yc.Websites[site]
	return ok && e.Enabled
}

// WebsiteConfigValue returns youConfig.websites[site][key] and whether present.
// website_config == youConfig["websites"] (config_service.get_websites_config).
func (yc *YouConfiguration) WebsiteConfigValue(site, key string) (any, bool) {
	ws, ok := yc.raw["websites"].(map[string]any)
	if !ok {
		return nil, false
	}
	entry, ok := ws[site].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := entry[key]
	return v, ok
}

// IntelligenceBool reads youConfig.intelligence[key] as a bool (false absent).
func (yc *YouConfiguration) IntelligenceBool(key string) bool {
	return boolAt(yc.Intelligence, key)
}

// VerifiedNamesEnabled reports intelligence.verified_names_config.enabled == true.
func (yc *YouConfiguration) VerifiedNamesEnabled() bool {
	vn, ok := yc.Intelligence["verified_names_config"].(map[string]any)
	if !ok {
		return false
	}
	return boolAt(vn, "enabled")
}

// VerifiedNamesConfig resolves intelligence.verified_names_config into the
// upi.VerifiedNamesConfig shape (with the Python defaults for absent keys). It
// returns a value the UPI transform consumes; the *bool fields carry
// present/absent so upi's `in [True, None]` semantics are preserved. It lives
// here (not in upi) via a lightweight anonymous carrier to avoid an import
// cycle — the handler adapts it to upi.VerifiedNamesConfig.
func (yc *YouConfiguration) VerifiedNamesConfig() *VerifiedNamesRaw {
	vn, ok := yc.Intelligence["verified_names_config"].(map[string]any)
	if !ok {
		// Python default VerifiedNamesConfig() when absent.
		return &VerifiedNamesRaw{Enabled: false}
	}
	r := &VerifiedNamesRaw{Enabled: boolAt(vn, "enabled")}
	r.Name = boolPtrFrom(vn, "name")
	r.UpiIDs = boolPtrFrom(vn, "upi_ids")
	r.Encoding = boolPtrFrom(vn, "encoding")
	r.AlphanumericID = boolPtrFrom(vn, "alphanumeric_id")
	r.Source = boolPtrFrom(vn, "source")
	return r
}

// VerifiedNamesRaw is the tenant verified_names_config as read from youConfig,
// with present/absent preserved via pointers. The handler maps it to
// upi.VerifiedNamesConfig (kept separate to avoid an appconfig->upi import).
type VerifiedNamesRaw struct {
	Enabled        bool
	Name           *bool
	UpiIDs         *bool
	Encoding       *bool
	AlphanumericID *bool
	Source         *bool
}

func boolPtrFrom(m map[string]any, key string) *bool {
	v, ok := m[key]
	if !ok {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

// boolAt reads m[key] as a bool, false when absent or non-bool.
func boolAt(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, ok := m[key].(bool)
	return ok && b
}
