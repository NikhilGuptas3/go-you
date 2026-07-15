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
type WebsiteEntry struct {
	Enabled      bool `json:"enabled"`
	PhoneEnabled bool `json:"phone_enabled"`
	EmailEnabled bool `json:"email_enabled"`
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
func (yc *YouConfiguration) IsWebsiteEnabled(website, kind string) bool {
	e, ok := yc.Websites[website]
	if !ok || !e.Enabled {
		return false
	}
	switch kind {
	case "phone":
		return e.PhoneEnabled
	case "email":
		return e.EmailEnabled
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

// isPhoneInfoEnabled mirrors config_service.is_phone_info_enabled: absent =>
// true; only an explicit enabled:false disables it.
func (yc *YouConfiguration) isPhoneInfoEnabled() bool {
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
	return yc.isPhoneInfoEnabled() && boolAt(yc.PhoneInfo, "postpaid")
}

// IsDndEnabled mirrors config_service.is_dnd_status_enabled. (dnd is always OUT
// in go-you — EasyGoSms is token-pool — but the gate is modeled for parity.)
func (yc *YouConfiguration) IsDndEnabled() bool {
	return yc.isPhoneInfoEnabled() && boolAt(yc.PhoneInfo, "dnd_status")
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

// boolAt reads m[key] as a bool, false when absent or non-bool.
func boolAt(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, ok := m[key].(bool)
	return ok && b
}
