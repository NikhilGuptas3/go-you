package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestErrorClass pins the cardinality guard: spider_error's msg label must
// collapse to a SMALL fixed set, never carry the raw error string.
func TestErrorClass(t *testing.T) {
	cases := []struct {
		status, raw, want string
	}{
		{"timeout", "context deadline exceeded", "timeout"},
		{"failed", "no condition matched", "no_condition"},
		{"failed", "quora: home status 403", "http_error"},
		{"failed", "tumblr: token page status 500", "http_error"},
		{"failed", "codecademy: csrf-token meta not found", "other"},
		{"failed", "some totally novel error text with a phone +919999999999", "other"},
	}
	allowed := map[string]bool{"timeout": true, "no_condition": true, "http_error": true, "other": true}
	for _, tc := range cases {
		got := ErrorClass(tc.status, tc.raw)
		if got != tc.want {
			t.Errorf("ErrorClass(%q,%q) = %q, want %q", tc.status, tc.raw, got, tc.want)
		}
		if !allowed[got] {
			t.Errorf("ErrorClass produced unbounded label %q for %q", got, tc.raw)
		}
	}
}

// TestMetricNamesMatchHeyYou guards the metric names against accidental
// drift — the whole point of the rename is that hey-you's Grafana panels line
// up, so the series names must stay exactly these.
func TestMetricNamesMatchHeyYou(t *testing.T) {
	want := map[string]bool{
		"api_latency": true, "api_status": true,
		"website_latency": true, "website_status": true, "user_exist": true,
		"spider_error": true, "ml_service_counter": true,
		"real_time_cache": true, "you_intelligence": true,
	}
	// Exercise each vector so it registers a series, then scrape the default
	// registry and confirm the names are present. Histograms must be Observed
	// (not just referenced) to emit a child.
	APILatency.WithLabelValues("/v1/persona", "t").Observe(0.1)
	WebsiteLatency.WithLabelValues("X", "phone", "ok").Observe(0.1)
	WebsiteStatus.WithLabelValues("X", "ok", "phone").Inc()
	UserExist.WithLabelValues("X", "true").Inc()
	SpiderError.WithLabelValues("X", "other").Inc()
	MLServiceCounter.WithLabelValues("t", "call", "ok").Inc()
	RealTimeCache.WithLabelValues("phone", "hit").Inc()
	YouIntelligence.WithLabelValues("t", "score", "ok").Inc()
	APIStatus.WithLabelValues("/v1/persona", "t", "200").Inc()

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	got := map[string]bool{}
	for _, mf := range mfs {
		got[mf.GetName()] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("metric %q not registered/exposed", name)
		}
	}
	// Old name must be gone so a stale dashboard fails loudly rather than
	// silently reading a metric that no longer updates.
	if got["crawler_latency_seconds"] {
		t.Errorf("old metric crawler_latency_seconds is still registered; rename incomplete")
	}
}

// TestRealTimeCacheHitMissLabel confirms hit/miss is carried as the status
// label of ONE counter (hey-you's convention), not two metrics.
func TestRealTimeCacheHitMissLabel(t *testing.T) {
	RealTimeCache.WithLabelValues("email", "hit").Inc()
	RealTimeCache.WithLabelValues("email", "miss").Inc()

	mfs, _ := prometheus.DefaultGatherer.Gather()
	var found *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == "real_time_cache" {
			found = mf
			break
		}
	}
	if found == nil {
		t.Fatal("real_time_cache not found")
	}
	labels := map[string]bool{}
	for _, m := range found.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "status" {
				labels[l.GetValue()] = true
			}
		}
	}
	if !labels["hit"] || !labels["miss"] {
		t.Errorf("real_time_cache status label missing hit/miss: %v", labels)
	}
}

// TestBucketTiersMatchPython pins the three tiers to the Python values so a
// refactor can't silently widen them (the OOM risk).
func TestBucketTiersMatchPython(t *testing.T) {
	if len(DefaultBuckets) != 12 { // 11 finite + Inf
		t.Errorf("DefaultBuckets len = %d, want 12", len(DefaultBuckets))
	}
	if len(ExternalBuckets) != 14 { // 13 finite + Inf
		t.Errorf("ExternalBuckets len = %d, want 14", len(ExternalBuckets))
	}
	if len(DBBuckets) != 13 { // 12 finite + Inf
		t.Errorf("DBBuckets len = %d, want 13", len(DBBuckets))
	}
	// DefaultBuckets must start at 0.05 (Python DEFAULT_BUCKETS).
	if DefaultBuckets[0] != 0.05 {
		t.Errorf("DefaultBuckets[0] = %v, want 0.05", DefaultBuckets[0])
	}
	// ExternalBuckets must start at 0.25 (Python EXTERNAL_BUCKETS).
	if ExternalBuckets[0] != 0.25 {
		t.Errorf("ExternalBuckets[0] = %v, want 0.25", ExternalBuckets[0])
	}
}

// TestCodeClass pins external_dep_status.code to a bounded 5-value set so the
// label can never carry a raw HTTP status code.
func TestCodeClass(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{0, "err"}, {-1, "err"},
		{200, "2xx"}, {204, "2xx"}, {299, "2xx"},
		{301, "3xx"}, {304, "3xx"},
		{400, "4xx"}, {403, "4xx"}, {429, "4xx"},
		{500, "5xx"}, {503, "5xx"},
	}
	allowed := map[string]bool{"err": true, "2xx": true, "3xx": true, "4xx": true, "5xx": true}
	for _, tc := range cases {
		got := CodeClass(tc.code)
		if got != tc.want {
			t.Errorf("CodeClass(%d) = %q, want %q", tc.code, got, tc.want)
		}
		if !allowed[got] {
			t.Errorf("CodeClass produced unbounded label %q", got)
		}
	}
}

// TestObserveExternal confirms the third-party helper derives the right status
// (ok/error/timeout) and registers both external_dep series.
func TestObserveExternal(t *testing.T) {
	ObserveExternal("hibp", 0.2, 200, nil)                          // ok
	ObserveExternal("whoisxml", 0.3, 500, nil)                      // error (5xx)
	ObserveExternal("enrichdata", 0.1, 0, context.DeadlineExceeded) // timeout
	ObserveExternal("ml_service", 0.4, 0, errors.New("dial tcp"))   // error (transport)

	mfs, _ := prometheus.DefaultGatherer.Gather()
	names := map[string]bool{}
	statusVals := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
		if mf.GetName() == "external_dep_status" {
			for _, m := range mf.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "status" {
						statusVals[l.GetValue()] = true
					}
				}
			}
		}
	}
	if !names["external_dep_latency"] || !names["external_dep_status"] {
		t.Errorf("external_dep metrics not registered: %v", names)
	}
	for _, want := range []string{"ok", "error", "timeout"} {
		if !statusVals[want] {
			t.Errorf("external_dep_status missing status=%q (got %v)", want, statusVals)
		}
	}
}

// TestStageLatencyRegistered confirms StageObserve emits the stage_latency
// histogram with the stage label.
func TestStageLatencyRegistered(t *testing.T) {
	StageObserve("intelligence", 0.5)
	StageObserve("meta_phone", 0.3)
	StageStatus.WithLabelValues("static_phone", "error").Inc()

	mfs, _ := prometheus.DefaultGatherer.Gather()
	got := map[string]bool{}
	for _, mf := range mfs {
		got[mf.GetName()] = true
	}
	if !got["stage_latency"] || !got["stage_status"] {
		t.Errorf("stage metrics not registered: stage_latency=%v stage_status=%v",
			got["stage_latency"], got["stage_status"])
	}
}

// TestContainsAny exercises the tiny helper used by ErrorClass.
func TestContainsAny(t *testing.T) {
	if !containsAny("quora home status 403", "status") {
		t.Error("should match status")
	}
	if containsAny("plain message", "status", "http") {
		t.Error("should not match")
	}
	// sanity on the strings-free indexOf
	if indexOf("abcdef", "cd") != 2 || indexOf("abc", "xyz") != -1 || indexOf("abc", "") != 0 {
		t.Error("indexOf broken")
	}
}
