package handler

// This file holds boundary reshaping shared by the ml-payload and client-response
// paths. Both need the same account_details transformation — the go-you list form
// ([{_website, user_exist, ...}]) becomes the website-keyed map form
// ({"FLIPKART": {user_exist, ...}}) that both ml_service and the Python client
// shape use. Keeping it in ONE place stops the two callers from drifting.

// accountDetailsListToMap converts the account_details slice form into the
// website-keyed map, stripping the per-entry "_website" hint. Entries that are
// not objects, or lack a "_website" name, are skipped. The returned map is a new
// map; the entry objects are the SAME maps (mutated in place: "_website" removed).
func accountDetailsListToMap(list []any) map[string]any {
	m := make(map[string]any, len(list))
	for _, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["_website"].(string)
		if name == "" {
			continue
		}
		delete(entry, "_website")
		m[name] = entry
	}
	return m
}

// keyAccountDetailsForML rewrites phone_data/email_data primary_data.account_details
// from the list form into the website-keyed map form ml_service reads. It mutates
// the stripped ml-payload map in place. The full crawl set is preserved (no
// client-side drops) — the ml model consumes every site. Unlike the client
// transform, no client_response/UPI filtering happens here.
func keyAccountDetailsForML(you map[string]any) {
	for _, sec := range []string{"phone_data", "email_data"} {
		s, ok := you[sec].(map[string]any)
		if !ok {
			continue
		}
		pd, ok := s["primary_data"].(map[string]any)
		if !ok {
			continue
		}
		list, ok := pd["account_details"].([]any)
		if !ok {
			continue // already a map or absent
		}
		pd["account_details"] = accountDetailsListToMap(list)
	}
}
