// Package commondata is the enrichdata.in "common_data" feature: it runs up to
// six enrichment checks (vintage, demat_check, mutual_fund_check,
// credit_card_check, fintech_count, phone_to_address) against the third-party
// enrichdata.in OLAP API and assembles the top-level `common_data` block of the
// /v1/persona response. It ports common_data_service + get_common_enriched_data
// (service/you_service_aggregator.py:776-840, intelligence/vintage.py:26-69).
//
// Each check is the SAME POST call, differing only by a per-check service id:
//
//	POST {base}/api/olap/{service_id}/
//	Authorization: <token>   (from config, verbatim — includes the "Token " prefix)
//	body: {"phone":..,"email":..,"name":..}  (present fields only)
//	200 -> response JSON (a map);  non-200 / error -> nil (caller records {"error":true})
//
// Gating is per-check (see Service.Fetch): each tenant gate ANDs the global
// ENRICH_DATA.enabled flag. Results are cached under md5(phone:email:name) in the
// OrganicData table (via personacache): a per-check cached value short-circuits
// the HTTP call, and the merged doc is written back fire-and-forget.
//
// Config source: cfg.Get("enrich_data_config", nil) — a map with:
//
//	{"enabled": true, "url": "https://services.enrichdata.in",
//	 "authorization": "Token ...",
//	 "service_ids": {"vintage":"...","demat_check":"...", ...},
//	 "timeout": 10}
//
// A nil *Service (config absent / disabled) is a safe no-op: Fetch returns nil
// and the response simply has no common_data block, exactly as today.
package commondata

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/logger"
	"github.com/sign3labs/go-you/internal/model"
	"github.com/sign3labs/go-you/internal/personacache"
)

// defaultBaseURL / defaultTimeout mirror the Python defaults when the config row
// omits them. The base URL is the enrichdata.in host; the OLAP path is appended
// per call.
const (
	defaultBaseURL = "https://services.enrichdata.in"
	defaultTimeout = 10 * time.Second
)

// cdLog is this package's component logger ("commondata:<func> - …").
var cdLog = logger.Component("commondata")

// debugCommonEnv is the legacy DEBUG_COMMON=1 opt-in.
var debugCommonEnv = os.Getenv("DEBUG_COMMON") == "1"

// debugCommon reports whether an enrich-check diagnostic should be logged:
// legacy DEBUG_COMMON=1 env, or LOG_LEVEL=debug.
func debugCommon() bool { return debugCommonEnv || logger.DebugEnabled() }

// checkOrder is the fixed set of checks, in the order Python submits them. Each
// carries its tenant gate; the global ENRICH_DATA.enabled flag is ANDed in Fetch.
var checkOrder = []struct {
	name string
	gate func(*appconfig.YouConfiguration) bool
}{
	{"vintage", func(yc *appconfig.YouConfiguration) bool { return yc.EnrichedDataAPIEnabled() }},
	{"demat_check", func(yc *appconfig.YouConfiguration) bool { return yc.DematCheckEnabled() }},
	{"mutual_fund_check", func(yc *appconfig.YouConfiguration) bool { return yc.MutualFundCheckEnabled() }},
	{"credit_card_check", func(yc *appconfig.YouConfiguration) bool { return yc.CreditCardCheckEnabled() }},
	{"fintech_count", func(yc *appconfig.YouConfiguration) bool { return yc.FintechCountEnabled() }},
	{"phone_to_address", func(yc *appconfig.YouConfiguration) bool { return yc.PhoneToAddressEnabled() }},
}

// ConfigGetter is the subset of appconfig.Fetcher this package needs (declared
// as an interface to allow a stub in tests), matching meta.ConfigGetter.
type ConfigGetter interface {
	Get(key string, def any) any
}

// enrichCache is the subset of *personacache.Repo the service uses for the
// enrich doc. An interface so tests can supply a fake (or nil to disable).
type enrichCache interface {
	GetDoc(ctx context.Context, key string, now int64) (map[string]any, bool, error)
	PutDoc(ctx context.Context, key string, doc map[string]any, now int64) error
}

// doer is the http.Client subset used, so tests can stub the transport.
type doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Service runs the enrich checks. A nil *Service is a safe no-op.
type Service struct {
	cfg   ConfigGetter
	cache enrichCache
	http  doer
}

// New builds a Service. cfg is required (nil cfg => nil service, disabled).
// cache may be nil (caching off — every check hits the network). The service is
// further disabled at request time if the enrich_data_config is absent or
// enabled:false, so New never needs the config to be present yet.
func New(cfg ConfigGetter, cache *personacache.Repo) *Service {
	if cfg == nil {
		return nil
	}
	s := &Service{cfg: cfg, http: &http.Client{Timeout: defaultTimeout}}
	// A typed-nil *personacache.Repo must be stored as a nil interface so the
	// nil-cache guards below work (its methods are already nil-safe, but keep
	// the interface nil for clarity).
	if cache != nil {
		s.cache = cache
	}
	return s
}

// enrichConfig is the parsed enrich_data_config row.
type enrichConfig struct {
	enabled    bool
	url        string
	auth       string
	serviceIDs map[string]string
	timeout    time.Duration
}

// config reads and parses enrich_data_config. ok is false when the row is
// absent or enabled is not true — in which case the whole feature is off.
func (s *Service) config() (enrichConfig, bool) {
	raw, _ := s.cfg.Get("enrich_data_config", nil).(map[string]any)
	if raw == nil {
		return enrichConfig{}, false
	}
	c := enrichConfig{
		url:        defaultBaseURL,
		timeout:    defaultTimeout,
		serviceIDs: map[string]string{},
	}
	if b, ok := raw["enabled"].(bool); ok {
		c.enabled = b
	}
	if !c.enabled {
		return enrichConfig{}, false
	}
	if u, ok := raw["url"].(string); ok && u != "" {
		c.url = u
	}
	if a, ok := raw["authorization"].(string); ok {
		c.auth = a
	}
	if t, ok := raw["timeout"].(float64); ok && t > 0 {
		c.timeout = time.Duration(t * float64(time.Second))
	}
	if ids, ok := raw["service_ids"].(map[string]any); ok {
		for k, v := range ids {
			if sv, ok := v.(string); ok {
				c.serviceIDs[k] = sv
			}
		}
	}
	return c, true
}

// Fetch runs the enabled enrich checks for req and returns the common_data map
// (check name -> result, or {"error":true} on failure). It returns nil when the
// service is disabled or no check is enabled, so the caller can omit the block.
//
// The phone value used for both the payload and the cache key is the bare
// national number (req.Phone.Number), matching the Python raw request phone.
func (s *Service) Fetch(ctx context.Context, req *model.PersonaRequest, yc *appconfig.YouConfiguration) map[string]any {
	if s == nil || yc == nil || req == nil {
		return nil
	}
	cfg, ok := s.config()
	if !ok {
		return nil // enrich_data_config absent or disabled
	}

	phone, email, name := reqFields(req)

	// Which checks are enabled for this tenant?
	enabled := make([]string, 0, len(checkOrder))
	for _, c := range checkOrder {
		if c.gate(yc) {
			enabled = append(enabled, c.name)
		}
	}
	if len(enabled) == 0 {
		return nil
	}

	// Cache read: the merged enrich doc (a per-check cached value short-circuits
	// the HTTP call). Gated on caching, like Python (only reads when caching on).
	now := time.Now().Unix()
	cached := map[string]any{}
	cacheKey := ""
	cacheOn := s.cache != nil && yc.IsCachingEnabled()
	if cacheOn {
		cacheKey = personacache.EnrichKey(phone, email, name)
		if doc, hit, err := s.cache.GetDoc(ctx, cacheKey, now); err == nil && hit && doc != nil {
			cached = doc
		}
	}

	// Fan out the enabled checks concurrently. A per-check cache hit is served
	// from `cached` (read-only in this loop) without a network call. The
	// goroutines write ONLY to `results` under mu — `cached` is never mutated
	// concurrently — and the write-back doc is assembled after wg.Wait().
	results := make(map[string]any, len(enabled))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, check := range enabled {
		if hitVal, ok := cached[check]; ok && hitVal != nil {
			results[check] = hitVal
			if debugCommon() {
				cdLog.Debug("[DEBUG_COMMON] cache hit (skip http)", "check", check)
			}
			continue
		}
		serviceID := cfg.serviceIDs[check]
		if serviceID == "" {
			// No id configured for this check: mirror Python's error result.
			results[check] = map[string]any{"error": true}
			if debugCommon() {
				cdLog.Debug("[DEBUG_COMMON] no service_id configured", "check", check)
			}
			continue
		}
		wg.Add(1)
		go func(check, id string) {
			defer wg.Done()
			val := s.callCheck(ctx, cfg, id, check, phone, email, name)
			mu.Lock()
			if val != nil {
				results[check] = val
			} else {
				results[check] = map[string]any{"error": true}
			}
			mu.Unlock()
		}(check, serviceID)
	}
	wg.Wait()

	// Write the merged doc back, fire-and-forget: the doc is the union of prior
	// cache hits and this request's fresh successes (errors are NOT cached, so a
	// failed check retries next time — matching Python, which only stores
	// response.json() on 200). Built here from results after the fan-out barrier,
	// so no goroutine mutates a shared map. Python stores unconditionally
	// (modified=True); we store whenever there is anything to store.
	if cacheOn {
		doc := make(map[string]any, len(results))
		for k, v := range cached {
			doc[k] = v // prior hits (retain checks not run this request)
		}
		for k, v := range results {
			if m, ok := v.(map[string]any); ok {
				if _, isErr := m["error"]; isErr && len(m) == 1 {
					continue // don't cache a bare {"error":true}
				}
			}
			doc[k] = v
		}
		if len(doc) > 0 {
			go func() { _ = s.cache.PutDoc(context.Background(), cacheKey, doc, now) }()
		}
	}

	return results
}

// callCheck performs one enrich POST and returns the parsed JSON (a map) on 200,
// or nil on any non-200 / error — matching get_common_enriched_data. The phone
// name-arg is intentionally the tenant name; the per-check `name` var is passed
// as reqName to avoid shadowing.
func (s *Service) callCheck(ctx context.Context, cfg enrichConfig, serviceID, check, phone, email, reqName string) map[string]any {
	url := cfg.url + "/api/olap/" + serviceID + "/"

	payload := map[string]any{}
	if phone != "" {
		payload["phone"] = phone
	}
	if email != "" {
		payload["email"] = email
	}
	if reqName != "" {
		payload["name"] = reqName
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json")
	if cfg.auth != "" {
		// verbatim (the config value already includes the "Token " prefix),
		// matching the Python header assignment.
		req.Header.Set("Authorization", cfg.auth)
	}

	// Per-check timeout: Python uses timeout-0.7 for the request. The client's
	// own Timeout is the ceiling; ctx cancellation still applies (leaf timeout).
	resp, err := s.http.Do(req)
	if err != nil {
		if debugCommon() {
			cdLog.Debug("[DEBUG_COMMON] http error", "check", check, "err", err.Error())
		}
		return nil
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		if debugCommon() {
			cdLog.Debug("[DEBUG_COMMON] non-200 (-> nil)", "check", check, "http_status", resp.StatusCode)
		}
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		if debugCommon() {
			cdLog.Debug("[DEBUG_COMMON] 200 but unparseable JSON", "check", check, "err", err.Error())
		}
		return nil
	}
	if debugCommon() {
		cdLog.Debug("[DEBUG_COMMON] 200 OK", "check", check, "keys", len(parsed))
	}
	return parsed
}

// reqFields extracts the phone (bare national number), email, and name strings
// used for the payload and cache key.
func reqFields(req *model.PersonaRequest) (phone, email, name string) {
	if req.Phone != nil {
		phone = req.Phone.Number
	}
	return phone, req.Email, req.Name
}
