// Package metrics exposes Prometheus metrics matching the Python names so the
// existing Grafana dashboards keep working for the Go service.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Buckets approximate the Python Histogram_Buckets (seconds).
var buckets = []float64{0.05, 0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 13, 20}

var (
	// APILatency mirrors the Python 'api_latency' histogram (labels: api, tenant).
	APILatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "api_latency",
		Help:    "You Service Request latency",
		Buckets: buckets,
	}, []string{"api", "tenant"})

	// APIStatus mirrors the Python 'api_status' counter (labels: api, tenant, status).
	APIStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "api_status",
		Help: "You Service Api Status",
	}, []string{"api", "tenant", "status"})

	// CrawlerLatency is a go-you-only per-crawler histogram. There is no Python
	// equivalent (the Python side has no per-spider Prometheus metric), so this
	// is for understanding go-you's internal latency profile — which crawler is
	// the tail — NOT for Go-vs-Python comparison. Labels:
	//   crawler = flipkart|instagram|spotify|freelancer
	//   kind    = phone|email
	//   status  = ok|failed|timeout
	CrawlerLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "crawler_latency_seconds",
		Help:    "Per-crawler call latency (go-you only)",
		Buckets: buckets,
	}, []string{"crawler", "kind", "status"})
)
