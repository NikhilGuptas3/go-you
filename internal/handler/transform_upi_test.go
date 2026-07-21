package handler

import (
	"encoding/json"
	"testing"

	"github.com/sign3labs/go-you/internal/crawler/upi"
)

// roundTrip marshals then unmarshals v, reproducing what transformResponse does
// to the whole response: []map[string]any becomes []any and []string becomes
// []any. Used to guard against the "verified_names empty after round-trip" bug.
func roundTrip(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestTransformUPIVerifiedNamesSurvivesRoundTrip reproduces the reported prod
// divergence: verified_names came back empty in go-you though bank_verified_name
// was populated. Root cause: transformResponse JSON-round-trips the response, so
// the UPI entry's profiles/verified_names arrive as []any (not []map[string]any)
// and the type assertions in CleanProfileForResponse silently dropped them.
// After CoerceMaps, verified_names must survive with the encoded source (ptsbi ->
// PAYTM -> "a2") and the upi_ids, matching prod.
func TestTransformUPIVerifiedNamesSurvivesRoundTrip(t *testing.T) {
	upiTrue := true
	yes := true
	ut := &upiTransform{
		cfg: &upi.Config{ClientResponse: true},
		vnCfg: &upi.VerifiedNamesConfig{
			Enabled: true, UpiIDs: &yes, // upi_ids enabled -> ids kept
		},
		phonepeEnabled:       false,
		verifiedNamesEnabled: true,
		clientResponse:       true,
	}
	_ = upiTrue

	rawUPI := map[string]any{
		"user_exist": true, "suffix": "ptsbi", "vpa": "7667701982@ptsbi", "name": "NIKHIL KUMAR  GUPTA",
		"source": "CASH_FREE_UPI", "app_name": "PAYTM",
		"profiles": []map[string]any{
			{"user_exist": true, "suffix": "ptsbi", "vpa": "7667701982@ptsbi", "name": "NIKHIL KUMAR  GUPTA", "app_name": "PAYTM"},
		},
		"verified_names": []map[string]any{
			{"source": "PAYTM", "name": "NIKHIL KUMAR  GUPTA", "upi_ids": []string{"7667701982@ptsbi"}},
		},
	}
	// Round-trip the account entry the way transformResponse does.
	accountDetails := roundTrip(t, map[string]any{"UPI": rawUPI})

	transformUPIResponse(accountDetails, ut)

	upiEntry := accountDetails["UPI"].(map[string]any)
	vnsAny, present := upiEntry["verified_names"]
	if !present {
		t.Fatalf("verified_names missing from cleaned UPI entry: %+v", upiEntry)
	}
	vns, ok := vnsAny.([]map[string]any)
	if !ok || len(vns) != 1 {
		t.Fatalf("expected 1 verified_name, got %#v", vnsAny)
	}
	if vns[0]["source"] != "a2" {
		t.Errorf("source should encode PAYTM->a2, got %v", vns[0]["source"])
	}
	if vns[0]["name"] != "NIKHIL KUMAR  GUPTA" {
		t.Errorf("name lost: %v", vns[0]["name"])
	}
	ids, _ := vns[0]["upi_ids"].([]string)
	if len(ids) != 1 || ids[0] != "7667701982@ptsbi" {
		t.Errorf("upi_ids lost after round-trip: %#v", vns[0]["upi_ids"])
	}

	// And it flows into the section intelligence_data.
	intel := deriveUPIIntelligence(accountDetails, map[string]any{}, ut)
	if intel["bank_verified_name"] != "NIKHIL KUMAR  GUPTA" {
		t.Errorf("bank_verified_name = %v", intel["bank_verified_name"])
	}
	got, _ := intel["verified_names"].([]map[string]any)
	if len(got) != 1 {
		t.Errorf("intelligence verified_names should carry the entry, got %#v", intel["verified_names"])
	}
}

// TestUPIIDsFormatFilterAfterRoundTrip covers the upi_ids branch specifically
// after the JSON round-trip (ids arrive as []any): a conforming 10-digit VPA is
// kept, a malformed id is dropped by is_upi_format_correct, and an email-derived
// VPA is kept via the emailVPAIDs exemption even though it fails the format
// filter. Mirrors filter_verified_names with alphanumeric_id absent (default).
func TestUPIIDsFormatFilterAfterRoundTrip(t *testing.T) {
	yes := true
	ut := &upiTransform{
		cfg:                  &upi.Config{ClientResponse: true},
		vnCfg:                &upi.VerifiedNamesConfig{Enabled: true, UpiIDs: &yes},
		verifiedNamesEnabled: true,
		clientResponse:       true,
	}
	rawUPI := map[string]any{
		"user_exist": true, "suffix": "okicici", "vpa": "user.name@okicici", "name": "Some Name",
		// One email-derived profile so its VPA is exempt from the format filter.
		"profiles": []map[string]any{
			{"user_exist": true, "suffix": "okicici", "vpa": "user.name@okicici", "name": "Some Name",
				"app_name": "GPAY", "match_type": "email"},
		},
		"verified_names": []map[string]any{
			{"source": "PAYTM", "name": "Some Name", "upi_ids": []string{
				"9876543210@paytm",  // conforming -> kept
				"garbage-not-a-vpa", // malformed -> dropped
				"user.name@okicici", // email-derived -> kept via exemption
			}},
		},
	}
	accountDetails := roundTrip(t, map[string]any{"UPI": rawUPI})
	transformUPIResponse(accountDetails, ut)

	vns := accountDetails["UPI"].(map[string]any)["verified_names"].([]map[string]any)
	if len(vns) != 1 {
		t.Fatalf("expected 1 verified_name, got %#v", vns)
	}
	ids, _ := vns[0]["upi_ids"].([]string)
	want := map[string]bool{"9876543210@paytm": true, "user.name@okicici": true}
	if len(ids) != 2 {
		t.Fatalf("expected 2 kept upi_ids (conforming + email-exempt), got %#v", ids)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected upi_id kept: %q", id)
		}
	}
}

// TestTransformUPIResponseAndIntelligence exercises the UPI transform end to
// end: a raw UPI account entry is reshaped to the client form, the section
// intelligence_data gets bank_verified_name/verified_names_status, and (with
// CLIENT_RESPONSE off) the UPI entry is dropped from account_details while
// PHONEPE (enabled) is derived.
func TestTransformUPIResponseAndIntelligence(t *testing.T) {
	ut := &upiTransform{
		cfg:                  &upi.Config{ClientResponse: false},
		vnCfg:                &upi.VerifiedNamesConfig{Enabled: true},
		phonepeEnabled:       true,
		verifiedNamesEnabled: true,
		clientResponse:       false,
	}
	accountDetails := map[string]any{
		"FLIPKART": map[string]any{"user_exist": true},
		"UPI": map[string]any{
			"user_exist": true, "suffix": "ybl", "vpa": "9876543210@ybl", "name": "Ravi Kumar",
			"source": "CONFIRMTKT_UPI",
			"profiles": []map[string]any{
				{"user_exist": true, "suffix": "ybl", "vpa": "9876543210@ybl", "name": "Ravi Kumar", "app_name": "PHONEPE"},
			},
			"verified_names": []map[string]any{
				{"source": "PHONEPE", "name": "Ravi Kumar", "upi_ids": []string{"9876543210@ybl"}},
			},
		},
	}

	transformUPIResponse(accountDetails, ut)

	// PHONEPE derived from the PHONEPE-app profile (user_exist true).
	pe, ok := accountDetails["PHONEPE"].(map[string]any)
	if !ok || pe["user_exist"] != true {
		t.Errorf("expected PHONEPE {user_exist:true}, got %+v", accountDetails["PHONEPE"])
	}
	// UPI entry reshaped (source stripped).
	upiEntry := accountDetails["UPI"].(map[string]any)
	if _, leaked := upiEntry["source"]; leaked {
		t.Error("UPI source should be stripped by CleanProfileForResponse")
	}

	// Intelligence derivation.
	intel := deriveUPIIntelligence(accountDetails, map[string]any{}, ut)
	if intel["bank_verified_name"] != "Ravi Kumar" {
		t.Errorf("bank_verified_name = %v", intel["bank_verified_name"])
	}
	if intel["verified_names_status"] != "found" {
		t.Errorf("verified_names_status = %v", intel["verified_names_status"])
	}
	if _, ok := intel["verified_names"]; !ok {
		t.Error("verified_names should be set when verifiedNamesEnabled")
	}
}

// TestTransformUPIError: an errored UPI entry becomes {error:true} and, with
// PHONEPE enabled, PHONEPE mirrors the error.
func TestTransformUPIError(t *testing.T) {
	ut := &upiTransform{cfg: &upi.Config{}, phonepeEnabled: true}
	ad := map[string]any{"UPI": map[string]any{"error": true}}
	transformUPIResponse(ad, ut)
	if u, _ := ad["UPI"].(map[string]any); u["error"] != true {
		t.Errorf("UPI should be {error:true}, got %+v", ad["UPI"])
	}
	if p, _ := ad["PHONEPE"].(map[string]any); p["error"] != true {
		t.Errorf("PHONEPE should mirror error, got %+v", ad["PHONEPE"])
	}
}

// TestTransformSectionDropsUPI: with CLIENT_RESPONSE off, the UPI entry is
// dropped from the final account_details but its intelligence survives.
func TestTransformSectionDropsUPI(t *testing.T) {
	ut := &upiTransform{cfg: &upi.Config{}, clientResponse: false, verifiedNamesEnabled: false}
	sec := map[string]any{
		"primary_data": map[string]any{
			"account_details": []any{
				map[string]any{"_website": "FLIPKART", "user_exist": true},
				map[string]any{"_website": "UPI", "user_exist": true, "name": "Ravi", "suffix": "ybl",
					"profiles": []map[string]any{}},
			},
		},
	}
	transformSection(sec, nil, ut)
	pd := sec["primary_data"].(map[string]any)
	ad := pd["account_details"].(map[string]any)
	if _, present := ad["UPI"]; present {
		t.Error("UPI should be dropped when CLIENT_RESPONSE is false")
	}
	if _, present := ad["FLIPKART"]; !present {
		t.Error("FLIPKART should remain")
	}
	// bank_verified_name derived even though UPI dropped from account_details.
	intel, _ := sec["intelligence_data"].(map[string]any)
	if intel == nil || intel["bank_verified_name"] != "Ravi" {
		t.Errorf("expected bank_verified_name derived, got %+v", sec["intelligence_data"])
	}
}
