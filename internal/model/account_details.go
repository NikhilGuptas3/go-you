package model

import "encoding/json"

// MarshalJSON flattens the rich Data map into the account entry alongside
// user_exist, matching the Python spiders that return a single flat object per
// site (e.g. {"user_exist": true, "username": "...", "handle": "..."}).
//
// A FAILED crawl marshals to exactly {"error": true} (plus the internal
// "_website" hint), matching hey-you's real_time_data_service.py:262 — no
// message, no user_exist, no rich data. The raw failure text lives only in the
// logs + the spider_error metric, never the response.
//
// Precedence on the success path: user_exist is written first, then Data keys
// are layered on top — a Data key never silently shadows user_exist because
// rich crawlers set user_exist via the typed field, not Data. On a key
// collision Data wins (Python builds one dict, last-write-wins), which is the
// intended behavior for spider-specific overrides.
func (a AccountDetails) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(a.Data)+2)
	if a.Error {
		// Bare error entry — nothing else. hey-you parity.
		out["error"] = true
	} else {
		if a.UserExist != nil {
			out["user_exist"] = *a.UserExist
		}
		for k, v := range a.Data {
			out[k] = v
		}
	}
	// The website name is carried as a transform-only hint "_website" so the
	// handler's transform step can build the final account_details map keyed by
	// website. transform deletes "_website" before the client sees it, so the
	// client-visible error entry is exactly {"error": true}. (The per-site
	// object itself does not include the website name in prod.)
	if a.Website != "" {
		out["_website"] = a.Website
	}
	return json.Marshal(out)
}

// UnmarshalJSON is the inverse of MarshalJSON: it reads a flattened account entry
// back into the typed fields (user_exist, error, _website) and collects every
// other key into Data. This makes AccountDetails round-trip through JSON, which
// the persona cache relies on (it stores the marshalled response and decodes it
// back). Keys consumed by typed fields are NOT duplicated into Data.
func (a *AccountDetails) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if v, ok := raw["_website"]; ok {
		_ = json.Unmarshal(v, &a.Website)
		delete(raw, "_website")
	}
	if v, ok := raw["user_exist"]; ok {
		var ue bool
		if json.Unmarshal(v, &ue) == nil {
			a.UserExist = &ue
		}
		delete(raw, "user_exist")
	}
	if v, ok := raw["error"]; ok {
		var e bool
		if json.Unmarshal(v, &e) == nil {
			a.Error = e
		}
		delete(raw, "error")
	}
	// Everything remaining is rich per-site data.
	if len(raw) > 0 {
		a.Data = make(map[string]any, len(raw))
		for k, v := range raw {
			var val any
			if err := json.Unmarshal(v, &val); err != nil {
				return err
			}
			a.Data[k] = val
		}
	}
	return nil
}
