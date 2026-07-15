// Command loadgen is a small in-cluster load generator for go-you. It fires a
// sustained stream of POST /v1/persona requests at a fixed concurrency for a
// fixed duration, so Grafana can plot api_latency under load. Kept deliberately
// simple and dependency-free (stdlib only) — it ships as a k8s Job.
//
// Config via env:
//
//	TARGET_URL   full URL, e.g. http://go-you.go-you-poc.svc.cluster.local/v1/persona
//	AUTH_USER    tenant id (Basic auth)   — required unless target is LOCAL_DEV
//	AUTH_PASS    tenant secret
//	CONCURRENCY  parallel workers          (default 5 — keep low: real endpoints)
//	DURATION     e.g. 60s                   (default 60s)
//	BODY         request JSON               (default: sample phone+email)
//
// It prints a percentile summary at the end; the real graph comes from Grafana
// scraping go-you's own api_latency during the run.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	target := env("TARGET_URL", "http://go-you.go-you-poc.svc.cluster.local/v1/persona")
	user := os.Getenv("AUTH_USER")
	pass := os.Getenv("AUTH_PASS")
	concurrency := envInt("CONCURRENCY", 5)
	duration := envDur("DURATION", 60*time.Second)
	body := env("BODY", `{"phone":{"country_code":"91","number":"7667701982"},"email":"nikhilkr496@gmail.com"}`)

	fmt.Printf("loadgen → %s\n  concurrency=%d duration=%s\n", target, concurrency, duration)

	var (
		total int64
		errs  int64
		mu    sync.Mutex
		latMs []float64
	)
	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(duration)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader([]byte(body)))
				req.Header.Set("Content-Type", "application/json")
				if user != "" {
					req.SetBasicAuth(user, pass)
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				elapsed := float64(time.Since(t0).Microseconds()) / 1000.0
				atomic.AddInt64(&total, 1)
				if err != nil {
					atomic.AddInt64(&errs, 1)
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					atomic.AddInt64(&errs, 1)
				}
				mu.Lock()
				latMs = append(latMs, elapsed)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	sort.Float64s(latMs)
	fmt.Printf("\n=== results ===\n")
	fmt.Printf("requests: %d  errors: %d\n", total, errs)
	if len(latMs) > 0 {
		fmt.Printf("throughput: %.1f req/s\n", float64(len(latMs))/duration.Seconds())
		fmt.Printf("p50: %.0f ms\n", pct(latMs, 50))
		fmt.Printf("p90: %.0f ms\n", pct(latMs, 90))
		fmt.Printf("p95: %.0f ms\n", pct(latMs, 95))
		fmt.Printf("p99: %.0f ms\n", pct(latMs, 99))
		fmt.Printf("max: %.0f ms\n", latMs[len(latMs)-1])
	}
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p / 100 * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

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
