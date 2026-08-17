package handler

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sign3labs/go-you/internal/metrics"
)

// timings collects named stage durations for one request. Concurrency-safe
// because phone/email branches (and their crawlers) record from goroutines.
//
// Two outputs:
//   - Server-Timing HTTP header (visible in Postman/curl -v, no tooling needed)
//   - a _timings map in the JSON body (easy to log / eyeball / diff vs Python)
//
// The point of per-stage timing: separate the service's OWN overhead (decode,
// auth, serialize, fan-out coordination) from the external crawler/meta HTTP
// calls, which are network-bound and roughly language-independent. That split
// is what actually tells you whether Go beats Python and where.
type timings struct {
	mu sync.Mutex
	ms map[string]float64 // stage -> milliseconds
}

func newTimings() *timings { return &timings{ms: make(map[string]float64)} }

// record stores the elapsed time for a stage under the given name, and — for the
// bounded set of internal pipeline stages — also emits it to the stage_latency
// Prometheus histogram so the per-request timing map gets history + percentiles
// in Grafana. Per-crawler stages (crawl_<SITE>) are excluded: they already have
// website_latency. The whole-request roll-ups (total, fanout_total) are excluded
// too — total is api_latency, and fanout_total is a sum, not a component. This
// keeps stage_latency's label set small and each series a real component.
func (t *timings) record(name string, d time.Duration) {
	t.mu.Lock()
	t.ms[name] = float64(d.Microseconds()) / 1000.0
	t.mu.Unlock()

	if isStageMetricName(name) {
		metrics.StageObserve(name, d.Seconds())
	}
}

// isStageMetricName reports whether a timing name is one of the bounded internal
// pipeline stages that should feed stage_latency. Excludes per-crawler timings
// (crawl_*, on website_latency) and the whole-request roll-ups.
func isStageMetricName(name string) bool {
	if strings.HasPrefix(name, "crawl_") {
		return false
	}
	switch name {
	case "total", "fanout_total":
		return false
	default:
		return true
	}
}

// since is sugar for record(name, time.Since(start)).
func (t *timings) since(name string, start time.Time) {
	t.record(name, time.Since(start))
}

// asMap returns a copy for embedding in the JSON response.
func (t *timings) asMap() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]float64, len(t.ms))
	for k, v := range t.ms {
		out[k] = v
	}
	return out
}

// serverTimingHeader renders the W3C Server-Timing header value, e.g.
// "decode;dur=0.2, crawl_FLIPKART;dur=812.4, total;dur=845.1".
func (t *timings) serverTimingHeader() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.ms))
	for k := range t.ms {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		// Server-Timing names must be tokens: no spaces.
		safe := strings.ReplaceAll(n, " ", "_")
		parts = append(parts, fmt.Sprintf("%s;dur=%.1f", safe, t.ms[n]))
	}
	return strings.Join(parts, ", ")
}
