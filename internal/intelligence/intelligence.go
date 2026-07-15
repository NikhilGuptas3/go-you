// Package intelligence ports the REMOTE ml_service path of the Python
// YouServiceAggregator (get_intelligence / set_intelligence) and the
// merge_you_ml_service_intelligence logic. It assembles the feature_list, POSTs
// to ml_service_config.url, and merges the response into per-section and common
// intelligence_data, then derives the prediction score.
//
// The Python code uses eval/exec over config strings; go-you reproduces the
// exact behaviors with an explicit output-kind dispatcher and a bracket-path
// walker — no dynamic evaluation. Only the observed `output`/`condition` shapes
// are supported; anything unrecognized is skipped (fail-closed) and logged.
//
// The local in-process sklearn PredictionService is NOT ported: prediction is
// served entirely by the ml_service onboarding_fraud_detection score.
package intelligence

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ConfigGetter is the subset of appconfig.Fetcher this package needs
// (ml_service_config).
type ConfigGetter interface {
	Get(key string, def any) any
}

// Service performs the ml_service call + merge. It is constructed per-process;
// per-request state is passed to Run.
type Service struct {
	cfg     ConfigGetter
	timeout time.Duration
}

func NewService(cfg ConfigGetter, timeout time.Duration) *Service {
	return &Service{cfg: cfg, timeout: timeout}
}

// Input carries the request/response context the merge needs. hasPhone/hasEmail
// gate the feature conditions; commonIntelligence is the tenant's
// common_intelligence config block (from youConfig). youResponse/youRequest are
// the assembled maps sent to ml_service (already null-stripped by the caller).
type Input struct {
	HasPhone           bool
	HasEmail           bool
	Tenant             string
	CommonIntelligence map[string]any
	YouRequest         map[string]any
	YouResponse        map[string]any
}

// Output holds the merged intelligence blocks the handler attaches, plus the
// derived prediction score. PhoneIntel/EmailIntel/CommonIntel are the
// intelligence_data maps for each scope; PredictionScore is the onboarding
// fraud score (nil when absent/errored).
type Output struct {
	PhoneIntel      map[string]any
	EmailIntel      map[string]any
	CommonIntel     map[string]any
	PredictionScore *float64
	PredictionError bool
}

// Run executes the ml_service call and merge. It is a no-op returning empty
// output when common_intelligence/ml_service is disabled or no features apply.
func (s *Service) Run(ctx context.Context, in Input) Output {
	out := Output{
		PhoneIntel:  map[string]any{},
		EmailIntel:  map[string]any{},
		CommonIntel: map[string]any{},
	}
	mlCfg := s.mlServiceConfig()
	if mlCfg == nil || !boolAt(mlCfg, "enabled") {
		return out
	}
	if !boolAt(in.CommonIntelligence, "enabled") {
		return out
	}
	ci, _ := in.CommonIntelligence["ml_service"].(map[string]any)
	if !boolAt(ci, "enabled") {
		return out
	}
	tenantFeatureCfg, _ := ci["feature_list_config"].(map[string]any)
	globalFeatureCfg, _ := mlCfg["feature_list_config"].(map[string]any)

	featureList := buildFeatureList(tenantFeatureCfg, globalFeatureCfg, in.HasPhone, in.HasEmail)
	if len(featureList) == 0 {
		return out
	}

	mlResponse := s.callMLService(ctx, mlCfg, in, featureList)
	// Merge each feature into the right scope, mirroring merge_you_ml_service_intelligence.
	for _, name := range featureList {
		fc := mergedFeatureConfig(globalFeatureCfg, tenantFeatureCfg, name)
		if fc == nil || fc.OutputType == "" {
			continue
		}
		value, ok := extractOutput(fc, mlResponse, name)
		var target map[string]any
		switch fc.ParentKey {
		case "phone_intelligence":
			if !in.HasPhone {
				continue
			}
			target = out.PhoneIntel
		case "email_intelligence":
			if !in.HasEmail {
				continue
			}
			target = out.EmailIntel
		default: // common_intelligence
			target = out.CommonIntel
		}
		setAtPath(target, fc.Path, value, ok, fc.OutputType)
	}

	out.PredictionScore, out.PredictionError = derivePrediction(out.CommonIntel)
	return out
}

func (s *Service) mlServiceConfig() map[string]any {
	if s.cfg == nil {
		return nil
	}
	m, _ := s.cfg.Get("ml_service_config", nil).(map[string]any)
	return m
}

// callMLService POSTs the payload and returns the parsed ml_response map. On any
// error it returns an empty map (never fatal), matching set_intelligence.
func (s *Service) callMLService(ctx context.Context, mlCfg map[string]any, in Input, featureList []string) map[string]any {
	url, _ := mlCfg["url"].(string)
	if url == "" {
		return map[string]any{}
	}
	payload := map[string]any{
		"feature_list": featureList,
		"YOU": map[string]any{
			"analytic_response": in.YouResponse,
			"request":           in.YouRequest,
		},
		"flow":    "prediction",
		"tenant":  in.Tenant,
		"get_all": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{}
	}
	timeout := s.timeout
	if t, ok := mlCfg["timeout"].(float64); ok && t > 0 {
		timeout = time.Duration(t * float64(time.Second))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return map[string]any{}
	}
	req.Header.Set("Content-Type", "application/json")
	if auth, ok := mlCfg["authorization"].(string); ok && auth != "" {
		// verbatim, no Basic/Bearer prefix (matches the Python header assignment)
		req.Header.Set("authorization", auth)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("intelligence: ml_service call failed: %v", err)
		return map[string]any{}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}

// --- feature list ---

// buildFeatureList ports get_feature_list_from_config: a feature is included
// when the tenant enables it, the global config knows it, and its condition
// (evaluated against phone/email presence) passes.
func buildFeatureList(tenantCfg, globalCfg map[string]any, hasPhone, hasEmail bool) []string {
	var out []string
	for key, v := range tenantCfg {
		entry, ok := v.(map[string]any)
		if !ok || !boolAt(entry, "enabled") {
			continue
		}
		gEntry, ok := globalCfg[key].(map[string]any)
		if !ok {
			continue
		}
		if cond, ok := gEntry["condition"].(string); ok && cond != "" {
			if !evalCondition(cond, hasPhone, hasEmail) {
				continue
			}
		}
		out = append(out, key)
	}
	return out
}

// evalCondition safely evaluates the only three condition strings the prod
// config uses. Unknown conditions fail closed (exclude the feature).
func evalCondition(cond string, hasPhone, hasEmail bool) bool {
	c := strings.TrimSpace(cond)
	switch c {
	case "you_req_json.get('email') is not None":
		return hasEmail
	case "you_req_json.get('phone') is not None":
		return hasPhone
	case "you_req_json.get('phone') is not None and you_req_json.get('email') is not None":
		return hasPhone && hasEmail
	default:
		log.Printf("intelligence: unrecognized feature condition %q — excluding", cond)
		return false
	}
}

func boolAt(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, ok := m[key].(bool)
	return ok && b
}
