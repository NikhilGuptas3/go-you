package handler

import (
	"encoding/json"

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/model"
)

// computeTopLevelStatus ports service/you_service_aggregator.py:231. Each
// present section with a non-2000 status code contributes a failure; 0 failures
// => 2000/SUCCESS, 1 => that failure, >=2 => 2500/MULTI_FIELD_ERROR. A missing
// section is not a failure. go-you's sections are 2000 unless a future
// invalid-id path sets 2100, so in practice this returns success today.
func computeTopLevelStatus(resp *model.PersonaResponse) (int, string) {
	type fail struct {
		code int
		name string
	}
	var failures []fail

	if resp.PhoneData != nil && resp.PhoneData.StatusCode != sectionStatusSuccess {
		if resp.PhoneData.StatusCode == sectionStatusInvalid {
			failures = append(failures, fail{statusCodeInvalidPhone, statusInvalidPhone})
		} else {
			failures = append(failures, fail{statusCodePhoneServerError, statusPhoneServerError})
		}
	}
	if resp.EmailData != nil && resp.EmailData.StatusCode != sectionStatusSuccess {
		if resp.EmailData.StatusCode == sectionStatusInvalid {
			failures = append(failures, fail{statusCodeInvalidEmail, statusInvalidEmail})
		} else {
			failures = append(failures, fail{statusCodeEmailServerError, statusEmailServerError})
		}
	}

	switch len(failures) {
	case 0:
		return statusCodeSuccess, statusOK
	case 1:
		return failures[0].code, failures[0].name
	default:
		return statusCodeMultiFieldError, statusMultiFieldError
	}
}

// transformResponse produces the final client JSON (as a map), porting the
// relevant rules of service/utils/response_mapper.py transform() for the
// token-free source set:
//   - account_details: slice -> map keyed by website
//   - drop websites with website_config[X].client_response == false
//   - recompute social_profile_count after drops
//   - cleanup_prediction: reshape prediction to {output_key_name: score} / {error:true}
//   - cleanup_meta: strip "meta"/"reverse_model_list" from intelligence_data.score
//     UNLESS the ?meta query param is present (the Python gotcha: any presence,
//     even ?meta=false, skips the strip; only a fully-absent param runs it)
//   - common_data is never produced here, so no explicit drop needed
//
// metaPresent is true when the request carried a ?meta query param at all.
func transformResponse(resp *model.PersonaResponse, yc *appconfig.YouConfiguration, metaPresent bool) map[string]any {
	// Marshal the typed response to a generic map so we can reshape freely, the
	// way the Python transform mutates a dict.
	b, _ := json.Marshal(resp)
	var out map[string]any
	_ = json.Unmarshal(b, &out)

	// Per-section reshape.
	if sec, ok := out["phone_data"].(map[string]any); ok {
		transformSection(sec, yc)
	}
	if sec, ok := out["email_data"].(map[string]any); ok {
		transformSection(sec, yc)
	}

	// cleanup_prediction (top level).
	cleanupPrediction(out, yc)

	// cleanup_meta unless the meta param is present at all.
	if !metaPresent {
		cleanupMeta(out)
	}

	return out
}

// transformSection reshapes one section's primary_data: account_details to a
// keyed map, dropping client_response:false sites and recomputing the count.
func transformSection(sec map[string]any, yc *appconfig.YouConfiguration) {
	pd, ok := sec["primary_data"].(map[string]any)
	if !ok {
		return
	}
	list, ok := pd["account_details"].([]any)
	if !ok {
		// Already a map or absent — nothing to reshape.
		return
	}
	m := make(map[string]any, len(list))
	profileCount := 0
	for _, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		// The marshaler drops "website" from the entry, so recover it from the
		// typed slice is not possible here — instead the entry retains no name.
		// We therefore key by a "website" field the transform-time struct still
		// carries: re-add it below via the parallel typed slice. To keep this
		// self-contained, transformSection is only reached with entries that
		// include a "_website" hint (set by the pre-transform step).
		name, _ := entry["_website"].(string)
		if name == "" {
			continue
		}
		delete(entry, "_website")
		if yc != nil && !yc.ClientResponse(name) {
			continue // drop client_response:false site
		}
		m[name] = entry
		if ue, ok := entry["user_exist"].(bool); ok && ue {
			profileCount++
		}
	}
	pd["account_details"] = m
	pd["social_profile_count"] = profileCount
}

// cleanupPrediction reshapes prediction to {output_key_name: score} or
// {error:true}, gated by the tenant prediction flag. output_key_name defaults
// to identity_fraud_score.
func cleanupPrediction(out map[string]any, yc *appconfig.YouConfiguration) {
	pred, ok := out["prediction"].(map[string]any)
	if !ok {
		return
	}
	if yc != nil && !yc.Prediction {
		delete(out, "prediction")
		return
	}
	keyName := "identity_fraud_score"
	if _, isErr := pred["error"]; isErr {
		out["prediction"] = map[string]any{"error": true}
		return
	}
	if score, ok := pred["predicted_score"]; ok {
		out["prediction"] = map[string]any{keyName: score}
	}
}

// cleanupMeta recursively removes "meta" and "reverse_model_list" keys from
// intelligence_data.score wherever they appear.
func cleanupMeta(out map[string]any) {
	strip := func(idAny any) {
		id, ok := idAny.(map[string]any)
		if !ok {
			return
		}
		score, ok := id["score"].(map[string]any)
		if !ok {
			return
		}
		removeKeyRecursive(score, "meta")
		delete(score, "reverse_model_list")
	}
	strip(out["intelligence_data"])
	if sec, ok := out["phone_data"].(map[string]any); ok {
		strip(sec["intelligence_data"])
	}
	if sec, ok := out["email_data"].(map[string]any); ok {
		strip(sec["intelligence_data"])
	}
}

// removeKeyRecursive deletes every occurrence of key from a nested map/slice.
func removeKeyRecursive(v any, key string) {
	switch t := v.(type) {
	case map[string]any:
		delete(t, key)
		for _, val := range t {
			removeKeyRecursive(val, key)
		}
	case []any:
		for _, e := range t {
			removeKeyRecursive(e, key)
		}
	}
}
