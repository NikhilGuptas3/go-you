package handler

import (
	"testing"

	"github.com/sign3labs/go-you/internal/crawler/upi"
)

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
