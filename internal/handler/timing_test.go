package handler

import "testing"

// TestIsStageMetricName pins which timing names feed stage_latency: the bounded
// internal stages YES, per-crawler crawl_* NO (they're on website_latency), and
// the whole-request roll-ups NO (total is api_latency, fanout_total is a sum).
func TestIsStageMetricName(t *testing.T) {
	stage := []string{
		"decode", "static_phone", "static_email", "meta_phone", "meta_email",
		"meta_cache_phone", "meta_cache_email", "breach_email", "intelligence",
		"common_data",
	}
	notStage := []string{
		"crawl_SNAPDEAL", "crawl_FLIPKART", "crawl_", "total", "fanout_total",
	}
	for _, n := range stage {
		if !isStageMetricName(n) {
			t.Errorf("%q should feed stage_latency", n)
		}
	}
	for _, n := range notStage {
		if isStageMetricName(n) {
			t.Errorf("%q should NOT feed stage_latency", n)
		}
	}
}

// TestRecordEmitsStageOnce is a smoke test that record() on a stage name does not
// panic and stores the ms value (the Prometheus side is covered in metrics pkg).
func TestRecordEmitsStageOnce(t *testing.T) {
	tm := newTimings()
	tm.record("intelligence", 0)
	tm.record("crawl_X", 0)
	m := tm.asMap()
	if _, ok := m["intelligence"]; !ok {
		t.Error("intelligence timing not stored")
	}
	if _, ok := m["crawl_X"]; !ok {
		t.Error("crawl_X timing not stored")
	}
}
