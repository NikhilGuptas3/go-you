// Package model defines the request/response shapes for /v1/persona.
//
// These mirror the Python YouRequest (service/you_service_aggregator.py:610
// set_you_request) and the phone_data/email_data sections of YouResponse. Field
// JSON tags match the Python keys exactly so existing clients see no difference.
package model

// Phone mirrors the Python PhoneNumber payload. The Python side accepts a phone
// as an object with a country code and number.
type Phone struct {
	CountryCode string `json:"country_code"`
	Number      string `json:"number"`
}

// PersonaRequest is the subset of the Python request body the POC supports.
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
// entries the Python spiders return ({"user_exist": true/false}).
type AccountDetails struct {
	Website   string `json:"website"`
	UserExist *bool  `json:"user_exist,omitempty"`
	ErrorMsg  string `json:"error_msg,omitempty"`
}

// Section is the phone_data or email_data block. It matches the Python section
// shape at a POC level: the primary_data.account_details list plus a status.
type Section struct {
	Key         string           `json:"key,omitempty"`
	Type        string           `json:"type"` // "phone" or "email"
	PrimaryData *PrimaryData     `json:"primary_data,omitempty"`
	StatusCode  int              `json:"status_code"`
	Status      string           `json:"status"`
	ErrorMsg    string           `json:"error_msg,omitempty"`
}

type PrimaryData struct {
	AccountDetails      []AccountDetails `json:"account_details"`
	SocialProfileCount  int              `json:"social_profile_count"`
}

// PersonaResponse is the top-level /v1/persona response. Only the fields the POC
// populates are present; the Python clean_empty step drops nulls the same way.
type PersonaResponse struct {
	RequestID  string   `json:"request_id"`
	PhoneData  *Section `json:"phone_data,omitempty"`
	EmailData  *Section `json:"email_data,omitempty"`
	StatusCode int      `json:"status_code"`
	Status     string   `json:"status"`
}

// ErrorResponse matches the Python error_*_handler output.
type ErrorResponse struct {
	RequestID string `json:"request_id"`
	ErrorMsg  string `json:"error_msg,omitempty"`
}
