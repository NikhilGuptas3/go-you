// Package model defines the request/response shapes for /v1/persona.
//
// These mirror the Python YouRequest (service/you_service_aggregator.py:610
// set_you_request) and the phone_data/email_data sections of YouResponse. Field
// JSON tags match the Python keys exactly so existing clients see no difference.
//
// Phase 0 of the full-parity migration expands the POC shapes to carry rich
// per-site data, concrete phone/email meta, breach details, per-section and
// top-level intelligence_data, and prediction. Fields the current handler does
// not yet populate are still declared here so later phases can fill them without
// re-touching this file. The Python clean_empty step (service/utils/
// response_mapper.py:182) drops nulls/empties; omitempty here mirrors that.
package model

// Phone mirrors the Python PhoneNumber payload. The Python side accepts a phone
// as an object with a country code and number.
type Phone struct {
	CountryCode string `json:"country_code"`
	Number      string `json:"number"`
}

// PersonaRequest is the subset of the Python request body go-you supports.
// Everything else (pan_number, gst_number, device_id, face-match, ...) is out of
// scope and simply ignored if present.
type PersonaRequest struct {
	Phone *Phone `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	// Timeout is a per-request override in seconds (Python: request["timeout"]).
	Timeout int `json:"timeout,omitempty"`
}

// AccountDetails is one crawler's verdict for one website. Mirrors the per-site
// entries the Python spiders return. Simple crawlers emit {"user_exist": bool};
// rich crawlers (DetailCrawler) additionally emit an arbitrary object which is
// carried in Data and flattened into the account entry at serialization time.
type AccountDetails struct {
	Website   string `json:"website"`
	UserExist *bool  `json:"user_exist,omitempty"`
	ErrorMsg  string `json:"error_msg,omitempty"`
	// Data holds the rich per-site fields (e.g. TELEGRAM username/handle,
	// GOOGLE reviews, GITHUB personal_profiles). Merged alongside user_exist in
	// the final account_details map. Nil for simple crawlers.
	Data map[string]any `json:"-"`
}

// Section is the phone_data or email_data block, mirroring the Python
// YouResponseByType (service/models/you_response.py:32). primary_data carries
// the crawler verdicts + meta + breach; linked_data is the opposite-type persona
// (always empty under the no-cloud constraint — no linked-id resolution);
// intelligence_data is the per-section rebuilt allow-list.
type Section struct {
	Key              string            `json:"key,omitempty"`
	Type             string            `json:"type"` // "phone" or "email"
	PrimaryData      *PrimaryData      `json:"primary_data,omitempty"`
	LinkedData       *PrimaryData      `json:"linked_data,omitempty"`
	IntelligenceData *IntelligenceData `json:"intelligence_data,omitempty"`
	StatusCode       int               `json:"status_code"`
	Status           string            `json:"status"`
	ErrorMsg         string            `json:"error_msg,omitempty"`
}

type PrimaryData struct {
	AccountDetails     []AccountDetails `json:"account_details"`
	SocialProfileCount int              `json:"social_profile_count"`
	// PhoneMeta/EmailMeta: only one is set per section, per its Kind. Typed as
	// pointers to the concrete meta structs (Phase 3); nil when the meta lane is
	// disabled or errored.
	PhoneMeta *PhoneMeta `json:"phone_meta,omitempty"`
	EmailMeta *EmailMeta `json:"email_meta,omitempty"`
	// BreachDetails is the section's breach block (Phase 4). Phone breach is
	// always empty under the no-cloud constraint (no static data source).
	BreachDetails *BreachDetails `json:"breach_details,omitempty"`
}

// PhoneMeta mirrors the Python phone_meta dict (service/models/
// phone_intelligence_data.py). IPQS fields (valid/active/country/associated_*)
// are always null in go-you: IPQS is prod-disabled. operator/circle come from
// OperatorFreecharge, postpaid from the Airtel/Jio/VI lane, revocations from the
// Outris TPI lane. dnd_status is always null (EasyGoSms is token-pool → OUT).
// revocations_analytics is never produced (stripped from the client anyway).
type PhoneMeta struct {
	PhoneNumber              string `json:"phone_number,omitempty"`
	Valid                    *bool  `json:"valid"`
	Active                   *bool  `json:"active"`
	Country                  string `json:"country"`
	AssociatedEmailAddresses string `json:"associated_email_addresses"`
	AssociatedNames          string `json:"associated_names"`
	Operator                 string `json:"operator,omitempty"`
	Postpaid                 *bool  `json:"postpaid,omitempty"`
	Circle                   string `json:"circle,omitempty"`
	// Revocations is {total_revocations, year, month} when the Outris TPI lane is
	// enabled and returns data; empty object otherwise.
	Revocations map[string]any `json:"revocations,omitempty"`
	DndStatus   *bool          `json:"dnd_status"`
}

// EmailMeta mirrors the Python email_meta dict (service/models/
// email_intelligence_data.py). IPQS fields and OpenAI name_from_email are always
// null (prod-disabled / dead). is_disposable + domain_attributes come from the
// domain-intelligence V2 lane.
type EmailMeta struct {
	Email                  string            `json:"email"`
	IsValid                *bool             `json:"is_valid"`
	IsDisposable           *bool             `json:"is_disposable,omitempty"`
	AssociatedNames        string            `json:"associated_names"`
	AssociatedPhoneNumbers string            `json:"associated_phone_numbers"`
	NamePredictedFromEmail string            `json:"name_predicted_from_email"`
	NameFromEmail          string            `json:"name_from_email"`
	DomainAttributes       *DomainAttributes `json:"domain_attributes,omitempty"`
}

// DomainAttributes mirrors the Python domain_meta_v2 output. NOTE the key
// "valid _mx" contains a literal space — preserved verbatim to match prod. On a
// lane error the whole object collapses to {domain, error:true}, represented by
// setting Error and leaving the rest zero.
type DomainAttributes struct {
	Domain        string `json:"domain,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	TLD           string `json:"tld,omitempty"`
	Registered    string `json:"registered,omitempty"`
	RegistrarName string `json:"registrar_name,omitempty"`
	RegisteredTo  string `json:"registered_to,omitempty"`
	DmarcEnforced string `json:"dmarc_enforced,omitempty"`
	SpfStrict     string `json:"spf_strict,omitempty"`
	ValidMX       string `json:"valid _mx,omitempty"`
	WebsiteExists *bool  `json:"website_exists,omitempty"`
	Error         *bool  `json:"error,omitempty"`
}

// BreachDetails mirrors the Python breach block. Email uses HaveIBeenPwned live;
// phone is always {phone, breaches:[], breaches_status:"not-found"}.
type BreachDetails struct {
	Email            string   `json:"email,omitempty"`
	Phone            string   `json:"phone,omitempty"`
	NumberOfBreaches int      `json:"number_of_breaches,omitempty"`
	Breaches         []Breach `json:"breaches"`
	BreachesStatus   string   `json:"breaches_status"`
}

// Breach is one HIBP breach entry (data_breach/breach_detail.py:21).
type Breach struct {
	Name         string `json:"name"`
	Date         string `json:"date,omitempty"`
	Data         string `json:"data,omitempty"`
	PwnCount     int    `json:"PwnCount,omitempty"`
	IsVerified   bool   `json:"IsVerified"`
	IsFabricated bool   `json:"IsFabricated"`
	IsSensitive  bool   `json:"IsSensitive"`
	IsRetired    bool   `json:"IsRetired"`
	IsSpamList   bool   `json:"IsSpamList"`
	IsMalware    bool   `json:"IsMalware"`
}

// IntelligenceData is both the per-section rebuilt allow-list (remove_
// intelligence_data) and the common top-level block. Score is the flexible
// merged score map from ml_service; the named fields are the section allow-list
// keys. Typed loosely because the ml_service merge writes arbitrary shapes.
type IntelligenceData struct {
	Score               map[string]any `json:"score,omitempty"`
	BankVerifiedName    string         `json:"bank_verified_name,omitempty"`
	VerifiedNamesStatus string         `json:"verified_names_status,omitempty"`
	AssociatedNames     []string       `json:"associated_names,omitempty"`
	LinkedIDs           []string       `json:"linked_ids,omitempty"`
	NonVerifiedNames    []string       `json:"non_verified_names,omitempty"`
	DigitalAge          map[string]any `json:"digital_age,omitempty"`
}

// Prediction is the internal prediction carrier before cleanup_prediction
// reshapes it to {output_key_name: score} or {error:true}. Served entirely by
// the ml_service score merge; the local sklearn model is not ported.
type Prediction struct {
	PredictedScore *float64 `json:"predicted_score,omitempty"`
	Error          *bool    `json:"error,omitempty"`
}

// PersonaResponse is the top-level /v1/persona response. The Python clean_empty
// step drops nulls the same way omitempty does here. IntelligenceData/Prediction
// are the common-level blocks (Phase 5); Timings is go-you observability.
type PersonaResponse struct {
	RequestID        string            `json:"request_id"`
	PhoneData        *Section          `json:"phone_data,omitempty"`
	EmailData        *Section          `json:"email_data,omitempty"`
	IntelligenceData *IntelligenceData `json:"intelligence_data,omitempty"`
	// Prediction is the reshaped client form: {"identity_fraud_score": <score>}
	// or {"error": true}. Built by cleanup_prediction (Phase 6), so it is a raw
	// map rather than the internal Prediction struct.
	Prediction map[string]any `json:"prediction,omitempty"`
	StatusCode int            `json:"status_code"`
	Status     string         `json:"status"`
	// Timings holds per-stage latency in milliseconds (go-you observability; also
	// emitted as the Server-Timing header). Omitted when timing is off.
	Timings map[string]float64 `json:"_timings_ms,omitempty"`
}

// ErrorResponse matches the Python error_*_handler output.
type ErrorResponse struct {
	RequestID string `json:"request_id"`
	ErrorMsg  string `json:"error_msg,omitempty"`
}
