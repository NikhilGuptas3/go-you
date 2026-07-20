package upi

// This file ports the client-facing UPI response shaping from upi_mapper.py:
// clean_upi_profile_for_response and filter_verified_names. The handler's
// transform_upi_response calls CleanProfileForResponse on the raw UPI account
// entry produced by the crawler.

// VerifiedNamesConfig mirrors service.models.you_configuration.VerifiedNamesConfig
// (the intelligence.verified_names_config block). Absent flags default per the
// Python `in [True, None]` checks.
type VerifiedNamesConfig struct {
	Enabled        bool
	Name           *bool // name in [True, None] => include
	UpiIDs         *bool // upi_ids in [True] => include
	AlphanumericID *bool // alphanumeric_id in [None, False] => apply format filter
	Encoding       *bool // encoding in [True, None] => encode source
	Source         *bool
}

// CleanProfileForResponse ports clean_upi_profile_for_response: keep only
// {user_exist, suffix, vpa, name} on the top profile, a de-duplicated `profiles`
// list of the same keys, and the filtered verified_names. On error -> {"error":
// true}. emailVPAIDs are email-derived VPAs exempt from the format filter.
func CleanProfileForResponse(upiProfile map[string]any, vnCfg *VerifiedNamesConfig, emailVPAIDs map[string]struct{}) map[string]any {
	if _, isErr := upiProfile["error"]; isErr {
		return map[string]any{"error": true}
	}
	clean := map[string]any{}
	for _, k := range []string{"user_exist", "suffix", "vpa", "name"} {
		if v, ok := upiProfile[k]; ok {
			clean[k] = v
		}
	}
	// De-dup nested profiles by suffix, keeping only the whitelisted keys.
	var cleanProfiles []map[string]any
	seenSuffix := map[string]struct{}{}
	if raw, ok := upiProfile["profiles"].([]map[string]any); ok {
		for _, p := range raw {
			suffix, _ := p["suffix"].(string)
			if suffix == "" {
				continue
			}
			if _, dup := seenSuffix[suffix]; dup {
				continue
			}
			seenSuffix[suffix] = struct{}{}
			cp := map[string]any{}
			for _, k := range []string{"user_exist", "suffix", "vpa", "name"} {
				if v, ok := p[k]; ok {
					cp[k] = v
				}
			}
			if len(cp) > 0 {
				cleanProfiles = append(cleanProfiles, cp)
			}
		}
	}
	clean["profiles"] = cleanProfiles

	if vn, ok := upiProfile["verified_names"].([]map[string]any); ok {
		clean["verified_names"] = filterVerifiedNames(vn, vnCfg, emailVPAIDs)
	}
	return clean
}

// filterVerifiedNames ports filter_verified_names: per verified-name entry keep
// name (if configured/present), upi_ids (format-filtered unless alphanumeric_id),
// and source (encoded unless disabled). Drops entries that end up empty.
func filterVerifiedNames(vns []map[string]any, cfg *VerifiedNamesConfig, emailVPAIDs map[string]struct{}) []map[string]any {
	if vns == nil || cfg == nil {
		return nil
	}
	if emailVPAIDs == nil {
		emailVPAIDs = map[string]struct{}{}
	}
	out := make([]map[string]any, 0, len(vns))
	for _, vn := range vns {
		upd := map[string]any{}
		// name in [True, None] and name present
		if boolOrNilTrue(cfg.Name) {
			if name, ok := vn["name"].(string); ok && name != "" {
				upd["name"] = name
			}
		}
		// upi_ids in [True]
		if cfg.UpiIDs != nil && *cfg.UpiIDs {
			ids := toStringSlice(vn["upi_ids"])
			// alphanumeric_id in [None, False] => apply is_upi_format_correct
			// (drop non-conforming ids unless they are email-derived VPAs).
			if cfg.AlphanumericID == nil || !*cfg.AlphanumericID {
				filtered := ids[:0:0]
				for _, id := range ids {
					if isUPIFormatCorrect(id) {
						filtered = append(filtered, id)
						continue
					}
					if _, ok := emailVPAIDs[id]; ok {
						filtered = append(filtered, id)
					}
				}
				ids = filtered
			}
			if len(ids) > 0 {
				upd["upi_ids"] = ids
			}
		}
		// encoding in [True, None] => encode source name; else raw source
		src, _ := vn["source"].(string)
		if boolOrNilTrue(cfg.Encoding) {
			if enc, ok := appNameEncoding[src]; ok {
				upd["source"] = enc
			} else {
				upd["source"] = otherAppName
			}
		} else {
			upd["source"] = src
		}
		if len(upd) > 0 {
			out = append(out, upd)
		}
	}
	return out
}

// boolOrNilTrue reports whether a *bool is nil (absent -> treated as True per
// Python `in [True, None]`) or explicitly true.
func boolOrNilTrue(b *bool) bool { return b == nil || *b }

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// isUPIFormatCorrect ports upi_mapper.is_upi_format_correct: 10 digits, an
// optional "-<affix>" before '@', then a handle.
func isUPIFormatCorrect(upiID string) bool {
	return upiFormatRe.MatchString(upiID)
}
