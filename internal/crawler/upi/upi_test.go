package upi

import "testing"

func bp(b bool) *bool { return &b }

// TestAggProfilePriorityPick: among true profiles, the highest-priority
// configured suffix wins (ibl priority 3 beats ybl priority 1).
func TestAggProfilePriorityPick(t *testing.T) {
	meta := &Meta{SuffixList: []SuffixEntry{{"ybl", 1}, {"ibl", 3}, {"paytm", 1}}}
	profiles := []*Profile{
		trueP("CONFIRMTKT_UPI", "ybl", "Ravi YBL", "9876543210@ybl"),
		trueP("MMT_UPI", "ibl", "Ravi IBL", "9876543210@ibl"),
	}
	agg := aggProfile(profiles, meta)
	if agg.UserExist == nil || !*agg.UserExist {
		t.Fatalf("expected user_exist true, got %+v", agg)
	}
	if agg.Suffix != "ibl" {
		t.Errorf("expected winning suffix ibl (priority 3), got %q", agg.Suffix)
	}
	if agg.Name != "Ravi IBL" {
		t.Errorf("expected name from ibl profile, got %q", agg.Name)
	}
}

// TestAggProfileFalseWhenNoTrue: no true profiles, some false -> user_exist false.
func TestAggProfileFalseWhenNoTrue(t *testing.T) {
	meta := &Meta{SuffixList: []SuffixEntry{{"ybl", 1}}}
	agg := aggProfile([]*Profile{falseP("MMT_UPI", "ybl")}, meta)
	if agg.UserExist == nil || *agg.UserExist {
		t.Errorf("expected user_exist false, got %+v", agg)
	}
}

// TestAggProfileErrorWhenAllError: only error profiles -> error.
func TestAggProfileErrorWhenAllError(t *testing.T) {
	meta := &Meta{SuffixList: []SuffixEntry{{"ybl", 1}}}
	agg := aggProfile([]*Profile{{Source: "MMT_UPI", Suffix: "ybl", Err: "boom"}}, meta)
	if !agg.hasError() {
		t.Errorf("expected error aggregate, got %+v", agg)
	}
}

// TestConstructVPA: prefers profile.vpa, else builds national@suffix, else "".
func TestConstructVPA(t *testing.T) {
	if got := constructVPAForProfile("9876543210", &Profile{VPA: "x@ybl"}); got != "x@ybl" {
		t.Errorf("prefer existing vpa: %q", got)
	}
	if got := constructVPAForProfile("9876543210", &Profile{Suffix: "ybl"}); got != "9876543210@ybl" {
		t.Errorf("build from suffix: %q", got)
	}
	if got := constructVPAForProfile("9876543210", &Profile{Suffix: commonSuffixConstant}); got != "" {
		t.Errorf("COMMON suffix -> empty vpa, got %q", got)
	}
}

// TestVerifiedNames: one entry per distinct app_name with collected ids.
func TestVerifiedNames(t *testing.T) {
	profiles := []*Profile{
		trueP("CONFIRMTKT_UPI", "ybl", "Ravi", "9876543210@ybl"),    // app PHONEPE
		trueP("MMT_UPI", "axl", "Ravi", "9876543210@axl"),           // app PHONEPE (same app)
		trueP("EMT_UPI", "paytm", "Ravi Kumar", "9876543210@paytm"), // app PAYTM
	}
	vns := verifiedNamesFromProfiles("9876543210", profiles)
	if len(vns) != 2 {
		t.Fatalf("expected 2 verified-name entries (PHONEPE, PAYTM), got %d: %+v", len(vns), vns)
	}
	// PHONEPE entry should have both ybl and axl ids.
	for _, vn := range vns {
		if vn["source"] == "PHONEPE" {
			ids, _ := vn["upi_ids"].([]string)
			if len(ids) != 2 {
				t.Errorf("PHONEPE should collect 2 ids, got %v", ids)
			}
		}
	}
}

// TestSuffixMaps: known suffix -> app; unknown -> UNKNOWN; COMMON identity.
func TestSuffixMaps(t *testing.T) {
	if appNameForSuffix("ybl") != "PHONEPE" {
		t.Errorf("ybl -> PHONEPE, got %q", appNameForSuffix("ybl"))
	}
	if appNameForSuffix("paytm") != "PAYTM" {
		t.Errorf("paytm -> PAYTM, got %q", appNameForSuffix("paytm"))
	}
	if appNameForSuffix("zzznope") != otherAppName {
		t.Errorf("unknown -> UNKNOWN, got %q", appNameForSuffix("zzznope"))
	}
	if appNameForSuffix(commonSuffixConstant) != commonSuffixConstant {
		t.Errorf("COMMON identity broken")
	}
}

// TestSplitList: numpy array_split contiguous chunks.
func TestSplitList(t *testing.T) {
	got := splitList([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 2 || len(got[0]) != 3 || len(got[1]) != 2 {
		t.Errorf("split [a..e]/2 -> [3][2], got %v", got)
	}
	got = splitList([]string{"a", "b"}, 3)
	if len(got) != 3 || len(got[0]) != 1 || len(got[1]) != 1 || len(got[2]) != 0 {
		t.Errorf("split [a,b]/3 -> [1][1][0], got %v", got)
	}
}

// TestCleanProfileForResponse: keeps whitelist keys, filters verified_names.
func TestCleanProfileForResponse(t *testing.T) {
	raw := map[string]any{
		"user_exist": true, "suffix": "ybl", "vpa": "9876543210@ybl", "name": "Ravi",
		"source": "CONFIRMTKT_UPI", // should be stripped
		"profiles": []map[string]any{
			{"user_exist": true, "suffix": "ybl", "vpa": "9876543210@ybl", "name": "Ravi", "app_name": "PHONEPE"},
		},
		"verified_names": []map[string]any{
			{"source": "PHONEPE", "name": "Ravi", "upi_ids": []string{"9876543210@ybl"}},
		},
	}
	vn := &VerifiedNamesConfig{Enabled: true} // name default true, encoding default true
	out := CleanProfileForResponse(raw, vn, nil)
	if _, leaked := out["source"]; leaked {
		t.Error("source should be stripped from top profile")
	}
	if out["name"] != "Ravi" || out["suffix"] != "ybl" {
		t.Errorf("whitelist keys lost: %+v", out)
	}
	vns, _ := out["verified_names"].([]map[string]any)
	if len(vns) != 1 {
		t.Fatalf("expected 1 verified name, got %+v", out["verified_names"])
	}
	// encoding default true -> source encoded to app_name_encoding[PHONEPE] = a1.
	if vns[0]["source"] != "a1" {
		t.Errorf("expected encoded source a1, got %v", vns[0]["source"])
	}
}

// TestCleanProfileError: error entry collapses to {error:true}.
func TestCleanProfileError(t *testing.T) {
	out := CleanProfileForResponse(map[string]any{"error": true}, &VerifiedNamesConfig{}, nil)
	if out["error"] != true || len(out) != 1 {
		t.Errorf("error profile should be {error:true}, got %+v", out)
	}
}

// TestBuildMetaSamplesEnriched: default config yields the enabled ENRICHED
// sources sampled to the count, and TPI includes Cashfree.
func TestBuildMetaSamplesEnriched(t *testing.T) {
	meta := buildMeta(DefaultConfig(), "9876543210")
	if len(meta.TPISources) != 1 || meta.TPISources[0].Name != "CASH_FREE_UPI" {
		t.Errorf("expected CASH_FREE_UPI TPI, got %+v", meta.TPISources)
	}
	// enriched_sources_sample_count = 4; 6 enabled ENRICHED (EMT, MMT, PAYU,
	// GIVE, EASEBUZZ, CONFIRMTKT) -> sampled to 4 unique.
	if len(meta.EnrichedSources) != 4 {
		t.Errorf("expected 4 sampled enriched sources, got %d: %+v", len(meta.EnrichedSources), meta.EnrichedSources)
	}
	seen := map[string]bool{}
	for _, s := range meta.EnrichedSources {
		if seen[s.Name] {
			t.Errorf("duplicate sampled source %s", s.Name)
		}
		seen[s.Name] = true
	}
}

// TestUPIFormatCorrect: 10-digit VPA format matcher.
func TestUPIFormatCorrect(t *testing.T) {
	cases := map[string]bool{
		"9876543210@ybl":     true,
		"9876543210-2@axl":   true,
		"9876543210-foo@axl": true,
		"short@ybl":          false,
		"ravi.kumar@okicici": false, // not 10 leading digits
	}
	for in, want := range cases {
		if got := isUPIFormatCorrect(in); got != want {
			t.Errorf("isUPIFormatCorrect(%q) = %v want %v", in, got, want)
		}
	}
}
