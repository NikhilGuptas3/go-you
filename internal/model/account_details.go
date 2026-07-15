package model

import "encoding/json"

// MarshalJSON flattens the rich Data map into the account entry alongside
// user_exist / error_msg, matching the Python spiders that return a single flat
// object per site (e.g. {"user_exist": true, "username": "...", "handle": "..."}).
//
// Precedence: the typed fields (user_exist, error_msg) are written first, then
// Data keys are layered on top — a Data key never silently shadows user_exist
// because rich crawlers set user_exist via the typed field, not Data. On a key
// collision Data wins (Python builds one dict, last-write-wins), which is the
// intended behavior for spider-specific overrides.
func (a AccountDetails) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(a.Data)+2)
	if a.UserExist != nil {
		out["user_exist"] = *a.UserExist
	}
	if a.ErrorMsg != "" {
		out["error_msg"] = a.ErrorMsg
	}
	for k, v := range a.Data {
		out[k] = v
	}
	// The website name is carried as a transform-only hint "_website" so the
	// handler's transform step can build the final account_details map keyed by
	// website. transform deletes "_website" before the client sees it. (The
	// per-site object itself does not include the website name in prod.)
	if a.Website != "" {
		out["_website"] = a.Website
	}
	return json.Marshal(out)
}
