package handler

import (
	"testing"

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/model"
)

func bp(b bool) *bool { return &b }

func sampleResp() *model.PersonaResponse {
	return &model.PersonaResponse{
		RequestID: "r1",
		EmailData: &model.Section{
			Key: "a@b.com", Type: "email", StatusCode: 2000, Status: "SUCCESS",
			PrimaryData: &model.PrimaryData{
				AccountDetails: []model.AccountDetails{
					{Website: "SPOTIFY", UserExist: bp(true)},
					{Website: "GITHUB", UserExist: bp(true), Data: map[string]any{"total_accounts": 2}},
					{Website: "ADOBE", ErrorMsg: "adobe status 400"},
					{Website: "HIDDEN", UserExist: bp(true)}, // client_response:false
				},
				SocialProfileCount: 3,
			},
		},
	}
}

// ycWith builds a YouConfiguration with HIDDEN marked client_response:false and
// prediction on. Uses ParseYouConfig for fidelity.
func ycWith(t *testing.T) *appconfig.YouConfiguration {
	t.Helper()
	cfg := `{"youConfig":{"prediction":true,"websites":{
		"HIDDEN":{"enabled":true,"email_enabled":true,"client_response":false}
	}}}`
	yc, err := appconfig.ParseYouConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return yc
}

func TestTransformAccountDetailsToMapAndDrop(t *testing.T) {
	resp := sampleResp()
	yc := ycWith(t)
	resp.StatusCode, resp.Status = computeTopLevelStatus(resp)

	out := transformResponse(resp, yc, false, nil)

	ed := out["email_data"].(map[string]any)
	pd := ed["primary_data"].(map[string]any)
	ad, ok := pd["account_details"].(map[string]any)
	if !ok {
		t.Fatalf("account_details not a map: %T", pd["account_details"])
	}
	// HIDDEN dropped (client_response:false); SPOTIFY/GITHUB/ADOBE kept.
	if _, present := ad["HIDDEN"]; present {
		t.Error("HIDDEN should be dropped (client_response:false)")
	}
	if len(ad) != 3 {
		t.Errorf("expected 3 sites, got %d: %v", len(ad), keysOf(ad))
	}
	// _website hint stripped.
	for name, e := range ad {
		if entry, ok := e.(map[string]any); ok {
			if _, leaked := entry["_website"]; leaked {
				t.Errorf("_website leaked in %s", name)
			}
		}
	}
	// GITHUB rich data preserved.
	gh := ad["GITHUB"].(map[string]any)
	if gh["total_accounts"] != float64(2) {
		t.Errorf("GITHUB total_accounts = %v", gh["total_accounts"])
	}
	// social_profile_count recomputed after drop: SPOTIFY + GITHUB = 2 (ADOBE
	// errored, HIDDEN dropped). transformSection sets it as a Go int.
	if asInt(pd["social_profile_count"]) != 2 {
		t.Errorf("social_profile_count = %v want 2", pd["social_profile_count"])
	}
}

func TestTransformTopLevelStatus(t *testing.T) {
	resp := sampleResp()
	code, name := computeTopLevelStatus(resp)
	if code != 2000 || name != "SUCCESS" {
		t.Errorf("status = %d/%s want 2000/SUCCESS", code, name)
	}
}

func TestCleanupPrediction(t *testing.T) {
	yc := ycWith(t)
	// score present => renamed to identity_fraud_score.
	out := map[string]any{"prediction": map[string]any{"predicted_score": 0.9}}
	cleanupPrediction(out, yc)
	pred := out["prediction"].(map[string]any)
	if pred["identity_fraud_score"] != 0.9 {
		t.Errorf("prediction = %v", pred)
	}
	// error => {error:true}.
	out2 := map[string]any{"prediction": map[string]any{"error": true}}
	cleanupPrediction(out2, yc)
	if out2["prediction"].(map[string]any)["error"] != true {
		t.Errorf("error prediction = %v", out2["prediction"])
	}
}

// TestCleanupPredictionRenamesScoreKey covers the real-config case: the tenant
// sets common_intelligence.score.onboarding_fraud_detection.output_key_name, and
// cleanup_prediction must rename intelligence_data.score.onboarding_fraud_detection
// to that key (response_mapper.py:264-269) AND use it in the prediction reshape.
func TestCleanupPredictionRenamesScoreKey(t *testing.T) {
	cfg := `{"youConfig":{"prediction":true,"common_intelligence":{
		"enabled":true,
		"score":{"enabled":true,"onboarding_fraud_detection":{"enabled":true,"output_key_name":"onboarding_phone_risk_score"}}
	}}}`
	yc, err := appconfig.ParseYouConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{
		"intelligence_data": map[string]any{
			"score": map[string]any{
				"onboarding_fraud_detection": map[string]any{"value": 0.488, "min_value": 0, "max_value": 1},
			},
		},
		"prediction": map[string]any{"predicted_score": 0.104},
	}
	cleanupPrediction(out, yc)

	score := out["intelligence_data"].(map[string]any)["score"].(map[string]any)
	if _, stale := score["onboarding_fraud_detection"]; stale {
		t.Error("onboarding_fraud_detection should be renamed away")
	}
	renamed, ok := score["onboarding_phone_risk_score"].(map[string]any)
	if !ok || renamed["value"] != 0.488 {
		t.Errorf("expected renamed score with value 0.488, got %v", score)
	}
	pred := out["prediction"].(map[string]any)
	if pred["onboarding_phone_risk_score"] != 0.104 {
		t.Errorf("prediction should use output_key_name: %v", pred)
	}
}

func TestCleanupMetaGotcha(t *testing.T) {
	mk := func() map[string]any {
		return map[string]any{
			"intelligence_data": map[string]any{
				"score": map[string]any{
					"onboarding_fraud_detection": map[string]any{"value": 0.5, "meta": map[string]any{"x": 1}},
					"reverse_model_list":         []any{"a"},
				},
			},
		}
	}
	// meta absent => strip runs.
	out := transformResponse(respFrom(mk()), nil, false, nil)
	score := out["intelligence_data"].(map[string]any)["score"].(map[string]any)
	if _, ok := score["reverse_model_list"]; ok {
		t.Error("reverse_model_list should be stripped when meta param absent")
	}
	ofd := score["onboarding_fraud_detection"].(map[string]any)
	if _, ok := ofd["meta"]; ok {
		t.Error("meta should be stripped when meta param absent")
	}

	// meta present => strip skipped.
	out2 := transformResponse(respFrom(mk()), nil, true, nil)
	score2 := out2["intelligence_data"].(map[string]any)["score"].(map[string]any)
	if _, ok := score2["reverse_model_list"]; !ok {
		t.Error("reverse_model_list should be kept when meta param present")
	}
}

// respFrom wraps a pre-built intelligence_data map into a response for the
// cleanup_meta test (bypasses section building).
func respFrom(intel map[string]any) *model.PersonaResponse {
	id := &model.IntelligenceData{Score: intel["intelligence_data"].(map[string]any)["score"].(map[string]any)}
	return &model.PersonaResponse{RequestID: "r", IntelligenceData: id, StatusCode: 2000, Status: "SUCCESS"}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	default:
		return -1
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
