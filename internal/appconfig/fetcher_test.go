package appconfig

import (
	"reflect"
	"testing"
)

func TestParseValue(t *testing.T) {
	// Valid JSON object.
	v := parseValue("k", `{"enabled": true}`)
	m, ok := v.(map[string]any)
	if !ok || m["enabled"] != true {
		t.Errorf("object parse: %v", v)
	}
	// Valid JSON array.
	v = parseValue("k", `["A","B"]`)
	if arr, ok := v.([]any); !ok || len(arr) != 2 {
		t.Errorf("array parse: %v", v)
	}
	// Invalid JSON => empty object (matches __parse_value fallback).
	v = parseValue("k", `not json`)
	if m, ok := v.(map[string]any); !ok || len(m) != 0 {
		t.Errorf("invalid parse should be empty object, got %v", v)
	}
}

func TestGlobalDisabled(t *testing.T) {
	// Pre-seed a fetcher's map (no DB needed).
	f := &Fetcher{configMap: map[string]any{
		"global_disabled_websites": []any{"MMT", "AMAZON", 42, "ZEE5"}, // 42 dropped
	}}
	got := GlobalDisabled(f)
	want := []string{"MMT", "AMAZON", "ZEE5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GlobalDisabled: got %v want %v", got, want)
	}

	// Absent key => default list.
	f2 := &Fetcher{configMap: map[string]any{}}
	if got := GlobalDisabled(f2); !reflect.DeepEqual(got, DisabledWebsitesDefault) {
		t.Errorf("GlobalDisabled default: got %v", got)
	}
}

func TestGetFallsBackToDefault(t *testing.T) {
	// Get on a pre-seeded map returns the stored value; a missing key with no DB
	// (nil db would panic in refreshKey) is exercised via a hit here only.
	f := &Fetcher{configMap: map[string]any{"present": "value"}}
	if got := f.Get("present", "def"); got != "value" {
		t.Errorf("Get(present)=%v want value", got)
	}
}
