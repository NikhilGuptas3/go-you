// Command loadtest drives the go-you /v1/persona endpoint at several fixed QPS
// levels using synthetic random Indian phones + emails, and reports tail
// latency (p70/p90/p95/p99) per level. Every response body is saved to a JSONL
// file for evidence, and a markdown summary is written at the end.
//
// It uses an OPEN-LOOP arrival model: requests are launched on a fixed schedule
// (every 1/QPS seconds) regardless of how long earlier requests take. This is
// deliberate — a closed loop (wait for response, then send next) would hide
// tail latency via "coordinated omission" and understate p99. Each request runs
// in its own goroutine so a slow request never delays the next arrival.
//
// Config via env (all optional):
//
//	TARGET_URL   default http://localhost:8080/v1/persona
//	AUTH_HEADER  full "Basic <base64>" value  (or set AUTH_USER/AUTH_PASS)
//	AUTH_USER    tenant id     } used to build Basic auth if AUTH_HEADER unset
//	AUTH_PASS    tenant secret }
//	N            requests per QPS level        (default 300)
//	QPS_LEVELS   comma list, e.g. "1,2,5,10"   (default 1,2,5,10)
//	OUT_DIR      evidence directory            (default ./loadtest-results)
//	REQ_TIMEOUT  per-request client timeout    (default 30s)
//	SEED         RNG seed for reproducible data (default 1)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sample struct {
	Index      int     `json:"index"`
	Phone      string  `json:"phone"`
	Email      string  `json:"email"`
	StatusCode int     `json:"status_code"`
	LatencyMs  float64 `json:"latency_ms"`
	ServerTime string  `json:"server_timing,omitempty"`
	YouTime    string  `json:"you_time,omitempty"`
	Err        string  `json:"error,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

func main() {
	target := env("TARGET_URL", "http://localhost:8080/v1/persona")
	authHeader := buildAuth()
	n := envInt("N", 300)
	levels := parseLevels(env("QPS_LEVELS", "1,2,5,10"))
	outDir := env("OUT_DIR", "./loadtest-results")
	reqTimeout := envDur("REQ_TIMEOUT", 30*time.Second)
	seed := int64(envInt("SEED", 1))

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal("cannot create OUT_DIR: %v", err)
	}

	fmt.Printf("go-you loadtest\n")
	fmt.Printf("  target      : %s\n", target)
	fmt.Printf("  N per level : %d\n", n)
	fmt.Printf("  QPS levels  : %v\n", levels)
	fmt.Printf("  out dir     : %s\n", outDir)
	fmt.Printf("  model       : open-loop (fixed arrival schedule)\n\n")

	// Pre-generate one shared pool of N synthetic identities so every QPS level
	// hits the SAME data set — differences between levels are then attributable
	// to load, not to different inputs.
	rng := rand.New(rand.NewSource(seed))
	pool := make([]struct{ phone, email string }, n)
	for i := range pool {
		pool[i].phone = randIndianMobile(rng)
		pool[i].email = randEmail(rng)
	}

	client := &http.Client{Timeout: reqTimeout}

	type levelResult struct {
		qps       int
		samples   []sample
		wallSecs  float64
	}
	var results []levelResult

	for _, qps := range levels {
		fmt.Printf("── level %d QPS : firing %d requests ", qps, n)
		res := runLevel(client, target, authHeader, pool, qps, outDir)
		results = append(results, levelResult{qps: qps, samples: res.samples, wallSecs: res.wall})

		lat := latencies(res.samples)
		errs := countErrors(res.samples)
		fmt.Printf("done in %.1fs\n", res.wall)
		fmt.Printf("     ok=%d err=%d  p70=%.0f p90=%.0f p95=%.0f p99=%.0f ms\n\n",
			len(res.samples)-errs, errs,
			pct(lat, 70), pct(lat, 90), pct(lat, 95), pct(lat, 99))

		// Brief cool-down between levels so the proxy/target isn't carrying
		// overlapping load from the previous level into the next measurement.
		if qps != levels[len(levels)-1] {
			time.Sleep(5 * time.Second)
		}
	}

	// ---- write the markdown summary report ----
	var b strings.Builder
	fmt.Fprintf(&b, "# go-you /v1/persona — latency under load\n\n")
	fmt.Fprintf(&b, "- Target: `%s`\n", target)
	fmt.Fprintf(&b, "- Requests per level: **%d** (same %d synthetic identities reused across levels, seed=%d)\n", n, n, seed)
	fmt.Fprintf(&b, "- Arrival model: **open-loop** (fixed schedule; avoids coordinated omission)\n")
	fmt.Fprintf(&b, "- Data: synthetic random Indian mobiles + emails (mostly non-existent — latency reflects full proxy round-trip, not hit rate)\n\n")

	fmt.Fprintf(&b, "| QPS | requests | ok | err | err%% | p70 ms | p90 ms | p95 ms | p99 ms | max ms | actual QPS |\n")
	fmt.Fprintf(&b, "|----:|---------:|---:|----:|-----:|-------:|-------:|-------:|-------:|-------:|-----------:|\n")
	for _, r := range results {
		lat := latencies(r.samples)
		errs := countErrors(r.samples)
		errPct := 0.0
		if len(r.samples) > 0 {
			errPct = float64(errs) / float64(len(r.samples)) * 100
		}
		actualQPS := 0.0
		if r.wallSecs > 0 {
			actualQPS = float64(len(r.samples)) / r.wallSecs
		}
		maxMs := 0.0
		if len(lat) > 0 {
			maxMs = lat[len(lat)-1]
		}
		fmt.Fprintf(&b, "| %d | %d | %d | %d | %.1f | %.0f | %.0f | %.0f | %.0f | %.0f | %.2f |\n",
			r.qps, len(r.samples), len(r.samples)-errs, errs, errPct,
			pct(lat, 70), pct(lat, 90), pct(lat, 95), pct(lat, 99), maxMs, actualQPS)
	}
	fmt.Fprintf(&b, "\n_Percentiles computed over successful + errored requests (an error still consumed a real latency until it failed). Per-request evidence in `level-<qps>qps.jsonl`._\n")

	reportPath := filepath.Join(outDir, "SUMMARY.md")
	if err := os.WriteFile(reportPath, []byte(b.String()), 0o644); err != nil {
		fatal("cannot write summary: %v", err)
	}

	fmt.Printf("═══════════════════════════════════════\n")
	fmt.Print(b.String())
	fmt.Printf("\nEvidence written to %s/\n", outDir)
	fmt.Printf("  SUMMARY.md + level-<qps>qps.jsonl (one JSON line per request, with full response body)\n")
}

type runResult struct {
	samples []sample
	wall    float64
}

// runLevel fires n requests at a fixed QPS using an open-loop scheduler, writes
// every response to level-<qps>qps.jsonl, and returns the collected samples.
func runLevel(client *http.Client, target, auth string, pool []struct{ phone, email string }, qps int, outDir string) runResult {
	n := len(pool)
	interval := time.Duration(float64(time.Second) / float64(qps))

	samples := make([]sample, n)
	var wg sync.WaitGroup

	start := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for i := 0; i < n; i++ {
		if i > 0 {
			<-ticker.C // wait for the scheduled arrival slot
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := pool[idx]
			samples[idx] = fire(client, target, auth, idx, p.phone, p.email)
		}(i)
	}
	wg.Wait()
	wall := time.Since(start).Seconds()

	// Persist evidence: one JSON object per line.
	path := filepath.Join(outDir, fmt.Sprintf("level-%dqps.jsonl", qps))
	f, err := os.Create(path)
	if err != nil {
		fatal("cannot create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, s := range samples {
		_ = enc.Encode(s)
	}

	return runResult{samples: samples, wall: wall}
}

// fire sends one POST and measures wall-clock latency.
func fire(client *http.Client, target, auth string, idx int, phone, email string) sample {
	bodyReq := fmt.Sprintf(`{"phone":{"country_code":"91","number":%q},"email":%q}`, phone, email)
	s := sample{Index: idx, Phone: phone, Email: email}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader([]byte(bodyReq)))
	if err != nil {
		s.Err = err.Error()
		return s
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	t0 := time.Now()
	resp, err := client.Do(req)
	s.LatencyMs = float64(time.Since(t0).Microseconds()) / 1000.0
	if err != nil {
		s.Err = err.Error()
		return s
	}
	defer resp.Body.Close()
	s.StatusCode = resp.StatusCode
	s.ServerTime = resp.Header.Get("Server-Timing")
	s.YouTime = resp.Header.Get("you_time")
	raw, _ := io.ReadAll(resp.Body)
	if json.Valid(raw) {
		s.Body = json.RawMessage(raw)
	} else {
		s.Body = json.RawMessage(strconv.Quote(string(raw)))
	}
	if resp.StatusCode != http.StatusOK {
		s.Err = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return s
}

// --- synthetic data ---

// randIndianMobile returns a 10-digit number starting 6-9 (valid Indian mobile
// format). These are format-valid but almost certainly not real accounts — fine
// for latency, since each still triggers a full proxy round-trip.
func randIndianMobile(rng *rand.Rand) string {
	first := byte('6' + rng.Intn(4)) // 6,7,8,9
	var sb strings.Builder
	sb.WriteByte(first)
	for i := 0; i < 9; i++ {
		sb.WriteByte(byte('0' + rng.Intn(10)))
	}
	return sb.String()
}

var emailDomains = []string{"gmail.com", "yahoo.com", "outlook.com", "hotmail.com", "rediffmail.com"}

func randEmail(rng *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	nLen := 6 + rng.Intn(6)
	var sb strings.Builder
	for i := 0; i < nLen; i++ {
		sb.WriteByte(letters[rng.Intn(len(letters))])
	}
	sb.WriteByte(byte('0' + rng.Intn(10)))
	sb.WriteByte(byte('0' + rng.Intn(10)))
	return sb.String() + "@" + emailDomains[rng.Intn(len(emailDomains))]
}

// --- stats helpers ---

func latencies(samples []sample) []float64 {
	out := make([]float64, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.LatencyMs)
	}
	sort.Float64s(out)
	return out
}

func countErrors(samples []sample) int {
	c := 0
	for _, s := range samples {
		if s.Err != "" {
			c++
		}
	}
	return c
}

// pct returns the p-th percentile (nearest-rank) of a sorted slice.
func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p/100*float64(len(sorted)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// --- env helpers ---

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
func parseLevels(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = []int{1, 2, 5, 10}
	}
	return out
}

func buildAuth() string {
	if h := os.Getenv("AUTH_HEADER"); h != "" {
		return h
	}
	u, p := os.Getenv("AUTH_USER"), os.Getenv("AUTH_PASS")
	if u == "" {
		return ""
	}
	// base64(user:pass)
	raw := u + ":" + p
	const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	for i := 0; i < len(raw); i += 3 {
		var chunk [3]byte
		nb := copy(chunk[:], raw[i:])
		out.WriteByte(b64[chunk[0]>>2])
		out.WriteByte(b64[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if nb > 1 {
			out.WriteByte(b64[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			out.WriteByte('=')
		}
		if nb > 2 {
			out.WriteByte(b64[chunk[2]&0x3f])
		} else {
			out.WriteByte('=')
		}
	}
	return "Basic " + out.String()
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "loadtest: "+format+"\n", a...)
	os.Exit(1)
}
