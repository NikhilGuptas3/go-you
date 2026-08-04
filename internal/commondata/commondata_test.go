package commondata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/model"
	"github.com/sign3labs/go-you/internal/personacache"
)

// --- stubs ---

// stubConfig is a ConfigGetter returning a fixed enrich_data_config.
type stubConfig struct{ row any }

func (s stubConfig) Get(key string, def any) any {
	if key == "enrich_data_config" {
		return s.row
	}
	return def
}

// enabledRow is a fully-enabled enrich_data_config with all six service ids.
func enabledRow() map[string]any {
	return map[string]any{
		"enabled":       true,
		"url":           "https://enrich.test",
		"authorization": "Token abc123",
		"timeout":       float64(5),
		"service_ids": map[string]any{
			"vintage":           "sid-vintage",
			"demat_check":       "sid-demat",
			"mutual_fund_check": "sid-mf",
			"credit_card_check": "sid-cc",
			"fintech_count":     "sid-fintech",
			"phone_to_address":  "sid-p2a",
		},
	}
}

// capturingDoer records requests and returns a canned response per URL.
type capturingDoer struct {
	mu       sync.Mutex
	reqs     []*http.Request
	bodies   []string
	status   int
	respBody string
}

func (d *capturingDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	d.reqs = append(d.reqs, req)
	d.bodies = append(d.bodies, body)
	st := d.status
	if st == 0 {
		st = 200
	}
	rb := d.respBody
	if rb == "" {
		rb = `{"ok":true}`
	}
	return &http.Response{
		StatusCode: st,
		Body:       io.NopCloser(strings.NewReader(rb)),
		Header:     make(http.Header),
	}, nil
}

// fakeCache is an in-memory enrichCache. mu guards puts/docs because PutDoc runs
// in the service's fire-and-forget write-back goroutine.
type fakeCache struct {
	mu   sync.Mutex
	docs map[string]map[string]any
	puts int
}

func newFakeCache() *fakeCache { return &fakeCache{docs: map[string]map[string]any{}} }

func (c *fakeCache) GetDoc(_ context.Context, key string, _ int64) (map[string]any, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[key]
	return d, ok, nil
}

func (c *fakeCache) PutDoc(_ context.Context, key string, doc map[string]any, _ int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	c.docs[key] = doc
	return nil
}

// svcWith builds a Service with the given config row, http doer, and cache.
func svcWith(row any, d doer, c enrichCache) *Service {
	return &Service{cfg: stubConfig{row: row}, http: d, cache: c}
}

// ycFrom parses a youConfig from the given inner JSON object.
func ycFrom(t *testing.T, inner string) *appconfig.YouConfiguration {
	t.Helper()
	yc, err := appconfig.ParseYouConfig(`{"youConfig": ` + inner + `}`)
	if err != nil {
		t.Fatalf("ParseYouConfig: %v", err)
	}
	return yc
}

func reqPhoneEmailName() *model.PersonaRequest {
	return &model.PersonaRequest{
		Phone: &model.Phone{CountryCode: "91", Number: "7596845338"},
		Email: "a@b.com",
		Name:  "Nikhil",
	}
}

// --- tests ---

// A disabled config row => nil result (feature off).
func TestFetch_DisabledConfig(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(map[string]any{"enabled": false}, d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": true, "demat_check": true}}`)
	got := s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	if got != nil {
		t.Fatalf("want nil for disabled config, got %v", got)
	}
	if len(d.reqs) != 0 {
		t.Fatalf("want no HTTP calls, got %d", len(d.reqs))
	}
}

// No enabled check => nil result, no HTTP.
func TestFetch_NoCheckEnabled(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": false}, "intelligence": {"enabled": false}}`)
	got := s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	if got != nil {
		t.Fatalf("want nil when no check enabled, got %v", got)
	}
	if len(d.reqs) != 0 {
		t.Fatalf("want no HTTP calls, got %d", len(d.reqs))
	}
}

// Gating: only the enabled checks run. Here demat_check + fintech_count only.
func TestFetch_GatingSubset(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": true, "demat_check": true, "fintech_count": true}}`)
	got := s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d: %v", len(got), got)
	}
	if _, ok := got["demat_check"]; !ok {
		t.Errorf("missing demat_check")
	}
	if _, ok := got["fintech_count"]; !ok {
		t.Errorf("missing fintech_count")
	}
	if _, ok := got["vintage"]; ok {
		t.Errorf("vintage should NOT run (intelligence off)")
	}
	if len(d.reqs) != 2 {
		t.Fatalf("want 2 HTTP calls, got %d", len(d.reqs))
	}
}

// vintage is gated by intelligence.enabled && intelligence.enriched_data_api.
func TestFetch_VintageGate(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"intelligence": {"enabled": true, "enriched_data_api": true}}`)
	got := s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	if _, ok := got["vintage"]; !ok {
		t.Fatalf("want vintage to run, got %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("want only vintage, got %v", got)
	}
}

// phone_to_address also fires via phone_to_address_with_raw.enabled.
func TestFetch_PhoneToAddressWithRaw(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": true, "phone_to_address_with_raw": {"enabled": true}}}`)
	got := s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	if _, ok := got["phone_to_address"]; !ok {
		t.Fatalf("want phone_to_address via _with_raw, got %v", got)
	}
}

// Request shape: URL = base/api/olap/{id}/, auth header verbatim, body has only
// present fields with the bare national phone number.
func TestFetch_RequestShape(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": true, "demat_check": true}}`)
	_ = s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	if len(d.reqs) != 1 {
		t.Fatalf("want 1 call, got %d", len(d.reqs))
	}
	req := d.reqs[0]
	if got, want := req.URL.String(), "https://enrich.test/api/olap/sid-demat/"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	if got := req.Header.Get("Authorization"); got != "Token abc123" {
		t.Errorf("Authorization = %q, want verbatim token", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(d.bodies[0]), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["phone"] != "7596845338" {
		t.Errorf("phone = %v, want bare national number 7596845338", body["phone"])
	}
	if body["email"] != "a@b.com" {
		t.Errorf("email = %v", body["email"])
	}
	if body["name"] != "Nikhil" {
		t.Errorf("name = %v", body["name"])
	}
}

// Body omits absent fields (email/name).
func TestFetch_BodyPresentOnly(t *testing.T) {
	d := &capturingDoer{}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": true, "demat_check": true}}`)
	req := &model.PersonaRequest{Phone: &model.Phone{CountryCode: "91", Number: "999"}}
	_ = s.Fetch(context.Background(), req, yc)
	var body map[string]any
	_ = json.Unmarshal([]byte(d.bodies[0]), &body)
	if _, ok := body["email"]; ok {
		t.Errorf("email must be absent when empty")
	}
	if _, ok := body["name"]; ok {
		t.Errorf("name must be absent when empty")
	}
	if body["phone"] != "999" {
		t.Errorf("phone = %v", body["phone"])
	}
}

// Non-200 => {"error": true} for that check.
func TestFetch_Non200IsError(t *testing.T) {
	d := &capturingDoer{status: 500}
	s := svcWith(enabledRow(), d, nil)
	yc := ycFrom(t, `{"common_intelligence": {"enabled": true, "demat_check": true}}`)
	got := s.Fetch(context.Background(), reqPhoneEmailName(), yc)
	dc, ok := got["demat_check"].(map[string]any)
	if !ok || dc["error"] != true {
		t.Fatalf("want demat_check={error:true}, got %v", got["demat_check"])
	}
}

// Cache short-circuit: a cached per-check value skips the HTTP call.
func TestFetch_CacheShortCircuit(t *testing.T) {
	d := &capturingDoer{}
	c := newFakeCache()
	// caching must be on (youConfig.caching) for the read to happen.
	yc := ycFrom(t, `{"caching": true, "common_intelligence": {"enabled": true, "demat_check": true, "fintech_count": true}}`)
	// Pre-seed the cache doc for the request under its enrich key.
	// The key builder is EnrichKey(phone, email, name).
	req := reqPhoneEmailName()
	// compute the key the same way Fetch does (phone = bare number)
	key := enrichKeyForReq(req)
	c.docs[key] = map[string]any{"demat_check": map[string]any{"cached": true}}

	s := svcWith(enabledRow(), d, c)
	got := s.Fetch(context.Background(), req, yc)

	// demat_check served from cache (no HTTP), fintech_count from HTTP.
	if len(d.reqs) != 1 {
		t.Fatalf("want 1 HTTP call (fintech only), got %d", len(d.reqs))
	}
	dc, _ := got["demat_check"].(map[string]any)
	if dc["cached"] != true {
		t.Errorf("demat_check not served from cache: %v", got["demat_check"])
	}
	if _, ok := got["fintech_count"]; !ok {
		t.Errorf("fintech_count should be fetched")
	}
	// write-back is fire-and-forget (a bare goroutine); poll briefly for it.
	if !waitFor(func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.puts > 0 }) {
		t.Errorf("expected cache write-back")
	}
}

// waitFor polls cond for up to ~1s, returning true as soon as it holds.
func waitFor(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// enrichKeyForReq mirrors reqFields + EnrichKey for the test.
func enrichKeyForReq(req *model.PersonaRequest) string {
	phone, email, name := reqFields(req)
	return personacache.EnrichKey(phone, email, name)
}
