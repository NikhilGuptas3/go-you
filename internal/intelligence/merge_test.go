package intelligence

import (
	"reflect"
	"testing"
)

func TestParseBracketPath(t *testing.T) {
	cases := map[string][]string{
		"['score']['onboarding_fraud_detection']": {"score", "onboarding_fraud_detection"},
		"['age']":                     {"age"},
		"['education']['is_student']": {"education", "is_student"},
	}
	for in, want := range cases {
		if got := parseBracketPath(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseBracketPath(%q) = %v want %v", in, got, want)
		}
	}
}

func TestClassifyOutput(t *testing.T) {
	if classifyOutput("get_score_entity_from_ml_response(ml_response.get(feature_name))") != "scoreEntity" {
		t.Error("scoreEntity misclassified")
	}
	if classifyOutput("ml_response.get(feature_name, {}).get('data', {}).get('value', {}).get('data')") != "dataValueData" {
		t.Error("dataValueData misclassified")
	}
	if classifyOutput("ml_response.get(feature_name, {}).get('data', {}).get('value')") != "dataValue" {
		t.Error("dataValue misclassified")
	}
}

func TestEvalCondition(t *testing.T) {
	cases := []struct {
		cond               string
		hasPhone, hasEmail bool
		want               bool
	}{
		{"you_req_json.get('email') is not None", false, true, true},
		{"you_req_json.get('email') is not None", true, false, false},
		{"you_req_json.get('phone') is not None", true, false, true},
		{"you_req_json.get('phone') is not None and you_req_json.get('email') is not None", true, true, true},
		{"you_req_json.get('phone') is not None and you_req_json.get('email') is not None", true, false, false},
		{"some_unknown_condition", true, true, false}, // fail closed
	}
	for _, c := range cases {
		if got := evalCondition(c.cond, c.hasPhone, c.hasEmail); got != c.want {
			t.Errorf("evalCondition(%q, p=%v e=%v) = %v want %v", c.cond, c.hasPhone, c.hasEmail, got, c.want)
		}
	}
}

func TestExtractScoreEntityAndSetPath(t *testing.T) {
	fc := &featureConfig{
		OutputKind: "scoreEntity",
		Path:       []string{"score", "onboarding_fraud_detection"},
		OutputType: "dict",
		ParentKey:  "common_intelligence",
	}
	ml := map[string]any{
		"onboarding_fraud_detection__default_1": map[string]any{
			"data":  map[string]any{"value": 0.87, "label": "risky"},
			"meta":  map[string]any{"type": "NUMERICAL"},
			"error": false,
		},
	}
	val, ok := extractOutput(fc, ml, "onboarding_fraud_detection__default_1")
	if !ok {
		t.Fatal("expected ok")
	}
	target := map[string]any{}
	setAtPath(target, fc.Path, val, ok, fc.OutputType)

	score := target["score"].(map[string]any)
	ofd := score["onboarding_fraud_detection"].(map[string]any)
	if ofd["value"] != 0.87 {
		t.Errorf("value = %v want 0.87", ofd["value"])
	}
	// derivePrediction reads it back.
	ps, perr := derivePrediction(target)
	if perr || ps == nil || *ps != 0.87 {
		t.Errorf("derivePrediction = %v, err=%v want 0.87", ps, perr)
	}
}

func TestSetPathErrorDefaults(t *testing.T) {
	// dict feature, not ok => {error:true}
	target := map[string]any{}
	setAtPath(target, []string{"score", "x"}, nil, false, "dict")
	got := target["score"].(map[string]any)["x"].(map[string]any)
	if got["error"] != true {
		t.Errorf("dict error default = %v", got)
	}
	// list feature, not ok => []
	target2 := map[string]any{}
	setAtPath(target2, []string{"addresses"}, nil, false, "list")
	if arr, ok := target2["addresses"].([]any); !ok || len(arr) != 0 {
		t.Errorf("list error default = %v", target2["addresses"])
	}
}

func TestDataValueTypeGate(t *testing.T) {
	// bool feature with a string value => skipped (type mismatch).
	fc := &featureConfig{OutputKind: "dataValue", Path: []string{"education", "is_student"}, OutputType: "bool", ParentKey: "common_intelligence"}
	ml := map[string]any{"is_student": map[string]any{"data": map[string]any{"value": "yes"}, "error": false}}
	val, ok := extractOutput(fc, ml, "is_student")
	target := map[string]any{}
	setAtPath(target, fc.Path, val, ok, fc.OutputType)
	if _, exists := target["education"]; exists {
		t.Errorf("type-mismatched bool should be skipped, got %v", target)
	}
}

func TestBuildFeatureList(t *testing.T) {
	tenant := map[string]any{
		"onboarding_fraud_detection__default_1": map[string]any{"enabled": true},
		"bnb_model_email":                       map[string]any{"enabled": true},
		"disabled_feature":                      map[string]any{"enabled": false},
	}
	global := map[string]any{
		"onboarding_fraud_detection__default_1": map[string]any{"condition": "you_req_json.get('phone') is not None"},
		"bnb_model_email":                       map[string]any{"condition": "you_req_json.get('email') is not None"},
		"disabled_feature":                      map[string]any{},
	}
	// phone only => only the phone-conditioned feature.
	got := buildFeatureList(tenant, global, true, false)
	if len(got) != 1 || got[0] != "onboarding_fraud_detection__default_1" {
		t.Errorf("phone-only feature list = %v", got)
	}
	// both => both conditioned features.
	got2 := buildFeatureList(tenant, global, true, true)
	if len(got2) != 2 {
		t.Errorf("both feature list = %v want 2", got2)
	}
}
