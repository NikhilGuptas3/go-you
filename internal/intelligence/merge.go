package intelligence

import (
	"regexp"
	"strings"
)

// featureConfig is the resolved per-feature output spec (global entry overlaid
// with any tenant overrides). It captures the three things the Python eval/exec
// derived: how to extract the value, where to place it, and its type.
type featureConfig struct {
	OutputKind string   // "scoreEntity" | "dataValue" | "dataValueData"
	Path       []string // parsed output_path, e.g. ["score","onboarding_fraud_detection"]
	OutputType string   // bool|int|str|dict|list
	ParentKey  string   // phone_intelligence | email_intelligence | common_intelligence
}

// mergedFeatureConfig builds the effective config for a feature: the global
// entry with tenant keys overlaid (matching the Python per-key override loop).
func mergedFeatureConfig(globalCfg, tenantCfg map[string]any, name string) *featureConfig {
	g, ok := globalCfg[name].(map[string]any)
	if !ok {
		return nil
	}
	merged := map[string]any{}
	for k, v := range g {
		merged[k] = v
	}
	if t, ok := tenantCfg[name].(map[string]any); ok {
		for k, v := range t {
			if v != nil {
				merged[k] = v
			}
		}
	}
	outputType, _ := merged["output_type"].(string)
	if outputType == "" {
		return nil
	}
	pathStr, _ := merged["output_path"].(string)
	parentKey, _ := merged["parent_key"].(string)
	if parentKey == "" {
		parentKey = "common_intelligence"
	}
	outputStr, _ := merged["output"].(string)
	return &featureConfig{
		OutputKind: classifyOutput(outputStr),
		Path:       parseBracketPath(pathStr),
		OutputType: outputType,
		ParentKey:  parentKey,
	}
}

// classifyOutput maps the config `output` string to one of the known kinds.
func classifyOutput(output string) string {
	switch {
	case strings.Contains(output, "get_score_entity_from_ml_response"):
		return "scoreEntity"
	case strings.Contains(output, ".get('value', {}).get('data')") ||
		strings.Contains(output, `.get("value", {}).get("data")`):
		return "dataValueData"
	default:
		// ml_response[feature].data.value
		return "dataValue"
	}
}

var bracketRe = regexp.MustCompile(`\['([^']*)'\]|\["([^"]*)"\]`)

// parseBracketPath turns "['score']['onboarding_fraud_detection']" into
// ["score","onboarding_fraud_detection"].
func parseBracketPath(s string) []string {
	matches := bracketRe.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if m[1] != "" {
			out = append(out, m[1])
		} else {
			out = append(out, m[2])
		}
	}
	return out
}

// extractOutput pulls the value for a feature from the ml_response per the
// feature's output kind. ok is false when the feature errored or is absent (the
// caller then writes the type-appropriate default).
func extractOutput(fc *featureConfig, mlResponse map[string]any, name string) (any, bool) {
	feat, ok := mlResponse[name].(map[string]any)
	if !ok {
		return nil, false
	}
	// error in (False, None) => usable; anything truthy => not usable.
	if e, present := feat["error"]; present {
		if b, isBool := e.(bool); isBool && b {
			return nil, false
		}
		if e != nil && !isFalsey(feat["error"]) {
			return nil, false
		}
	}
	switch fc.OutputKind {
	case "scoreEntity":
		// {...data, meta: cleaned, error}
		data, _ := feat["data"].(map[string]any)
		entity := map[string]any{}
		for k, v := range data {
			entity[k] = v
		}
		entity["meta"] = feat["meta"]
		entity["error"] = feat["error"]
		return entity, true
	case "dataValueData":
		data, _ := feat["data"].(map[string]any)
		val, _ := data["value"].(map[string]any)
		return val["data"], val["data"] != nil
	default: // dataValue
		data, _ := feat["data"].(map[string]any)
		v, present := data["value"]
		return v, present
	}
}

// setAtPath writes value into m at the bracket path, creating intermediate
// maps. When ok is false it writes the type-appropriate default (error obj for
// dict, [] for list, nil otherwise), matching get_merged_response's else branch.
func setAtPath(m map[string]any, path []string, value any, ok bool, outputType string) {
	if len(path) == 0 {
		return
	}
	if !ok {
		switch outputType {
		case "dict":
			value = map[string]any{"error": true}
		case "list":
			value = []any{}
		default:
			value = nil
		}
	} else if !typeMatches(value, outputType) {
		// Python only assigns when isinstance(value, output_type); a mismatch
		// leaves the key unset. Mirror that by skipping.
		return
	}
	cur := m
	for _, key := range path[:len(path)-1] {
		next, ok := cur[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
}

// typeMatches checks value against the config output_type, mirroring Python's
// isinstance gate. JSON numbers arrive as float64.
func typeMatches(v any, outputType string) bool {
	switch outputType {
	case "bool":
		_, ok := v.(bool)
		return ok
	case "int":
		_, ok := v.(float64) // JSON numbers
		return ok
	case "str":
		_, ok := v.(string)
		return ok
	case "dict":
		_, ok := v.(map[string]any)
		return ok
	case "list":
		_, ok := v.([]any)
		return ok
	default:
		return true
	}
}

func isFalsey(v any) bool {
	if v == nil {
		return true
	}
	if b, ok := v.(bool); ok {
		return !b
	}
	return false
}

// derivePrediction extracts the onboarding_fraud_detection score from the merged
// common intelligence, matching set_intelligence's prediction handling: the
// score's "value" is the predicted_score; a nil value (or error) => error.
func derivePrediction(commonIntel map[string]any) (*float64, bool) {
	score, ok := commonIntel["score"].(map[string]any)
	if !ok {
		return nil, true
	}
	ofd, ok := score["onboarding_fraud_detection"].(map[string]any)
	if !ok {
		return nil, true
	}
	if e, ok := ofd["error"].(bool); ok && e {
		return nil, true
	}
	v, ok := ofd["value"].(float64)
	if !ok {
		return nil, true
	}
	return &v, false
}
