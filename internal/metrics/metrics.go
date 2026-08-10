// Package metrics exposes Prometheus metrics matching the Python (hey-you)
// names, labels, and histogram buckets, so the existing Grafana dashboards keep
// working for the Go service. All metrics self-register via promauto and
// surface on the /metrics endpoint mounted in cmd/server/main.go.
package metrics

import (
	"math"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Bucket tiers mirror crawler/common_utils/metric_constants.py:25-49 exactly.
// hey-you deliberately uses three tiers (rather than one wide set) to bound
// Prometheus active-series cardinality — a comment there notes a single
// 61-bucket set once caused a ~7M-series OOM. Keep these in sync with Python.
var (
	// DefaultBuckets — general internal timers (Python DEFAULT_BUCKETS /
	// Histogram_Buckets). Used by api_latency.
	DefaultBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 30, 60, math.Inf(1)}

	// ExternalBuckets — crawls / third-party HTTP (Python EXTERNAL_BUCKETS).
	// Used by website_latency.
	ExternalBuckets = []float64{0.25, 0.5, 1, 1.5, 2, 2.5, 3, 4, 5, 10, 15, 30, 60, math.Inf(1)}

	// DBBuckets — datastore / auth latency (Python DB_BUCKETS). Reserved for
	// DAO-boundary timing if/when the MySQL lanes are instrumented.
	DBBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, math.Inf(1)}
)

var (
	// APILatency mirrors the Python 'api_latency' histogram (labels: api,
	// tenant), observed once per request in the handler's deferred block. Uses
	// DEFAULT_BUCKETS, matching engine/resources/you.py:35.
	APILatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "api_latency",
		Help:    "You Service Request latency",
		Buckets: DefaultBuckets,
	}, []string{"api", "tenant"})

	// APIStatus mirrors the Python 'api_status' counter (labels: api, tenant,
	// status). engine/resources/you.py:38.
	APIStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "api_status",
		Help: "You Service Api Status",
	}, []string{"api", "tenant", "status"})

	// WebsiteLatency mirrors the Python 'website_latency' histogram — per-crawler
	// call latency. Labels match Python's semantics: website, type (phone|email),
	// status (ok|failed|timeout). Uses EXTERNAL_BUCKETS.
	// (crawler/common_utils/metric_constants.py; observed in crawler_service.py.)
	WebsiteLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "website_latency",
		Help:    "Per-website crawl latency",
		Buckets: ExternalBuckets,
	}, []string{"website", "type", "status"})

	// WebsiteStatus mirrors 'website_status' — per-crawler outcome count. Label
	// names follow Python (website_id, status, type). status ∈ {ok,failed,timeout}.
	WebsiteStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "website_status",
		Help: "Per-website crawl outcome count",
	}, []string{"website_id", "status", "type"})

	// UserExist mirrors 'user_exist' — how often a crawler returned a verdict and
	// whether the user was found. found ∈ {true,false}.
	UserExist = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "user_exist",
		Help: "Per-website user-exists verdict count",
	}, []string{"website_id", "found"})

	// SpiderError mirrors 'spider_error' — per-crawler failure count. msg is a
	// SMALL fixed error class (timeout|no_condition|http_error|other), NEVER the
	// raw error string, to bound label cardinality (Python learned this the hard
	// way). Map via ErrorClass.
	SpiderError = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "spider_error",
		Help: "Per-website crawl error count (bounded msg class)",
	}, []string{"website", "msg"})

	// MLServiceCounter mirrors 'ml_service_counter' — ml_service call outcome.
	// Labels: tenant, stage, status. service/utils/counter.py.
	MLServiceCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ml_service_counter",
		Help: "ML Service counter for status at all stages",
	}, []string{"tenant", "stage", "status"})

	// RealTimeCache mirrors 'real_time_cache' — the persona/meta cache counter.
	// hey-you carries hit/miss in the STATUS LABEL of one counter, not as
	// separate metrics. type ∈ {phone,email}; status ∈ {hit,miss}.
	// (Python's label set is wider — type,ctx,status,tenant,proxy_type,
	// usage_count — go-you keeps the two that matter for the cache-hit ratio.)
	RealTimeCache = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "real_time_cache",
		Help: "Real Time Cache hit/miss",
	}, []string{"type", "status"})

	// YouIntelligence mirrors 'you_intelligence' — per-intelligence-feature
	// outcome. Labels: tenant, feature_name, status (ok|error|skipped).
	YouIntelligence = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "you_intelligence",
		Help: "Status of intelligence developed in you",
	}, []string{"tenant", "feature_name", "status"})

	// TokenPool mirrors the Python 'token_pool' counter (base_api_spider.py:48).
	// Labels: website, status, msg. status ∈ {found (request served from a warm
	// pooled token), not_found (pool empty → generated inline), add_succ, add_fail}.
	// msg carries a bounded error class on add_fail (via metrics.ErrorClass),
	// never a raw error string.
	TokenPool = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "token_pool",
		Help: "token pool status",
	}, []string{"website", "status", "msg"})

	// TokenPoolSize mirrors the Python 'token_pool_gauge' — current warm-token
	// count per site+kind, set by the background refill loop after each cycle.
	TokenPoolSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "token_pool_size",
		Help: "Current number of warm tokens in the pool",
	}, []string{"website", "kind"})
)

// ErrorClass maps an arbitrary crawler error message to one of a small fixed
// set of classes, so spider_error's msg label can never explode. Callers pass
// the already-mapped status ("ok"/"timeout"/"failed") plus the raw error; only
// failures reach here.
//
// The match tokens are deliberately narrow — "status " and "http" (whole
// tokens with trailing context), never a bare substring like "code" that would
// misclassify "codecademy". When in doubt it returns "other".
func ErrorClass(status, rawErr string) string {
	if status == "timeout" {
		return "timeout"
	}
	if rawErr == "no condition matched" {
		return "no_condition"
	}
	if containsAny(rawErr, "status ", "status=", "http ", "HTTP ", "http_") {
		return "http_error"
	}
	return "other"
}

// containsAny reports whether s contains any of subs (tiny helper, avoids
// importing strings for one call site's readability).
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}
