// Package tokenpool is go-you's background token-pool subsystem — the Go port of
// hey-you's TokenPoolGenerator (service/token_pool/token_pool_service.py) plus
// the pool bookkeeping in base_api_spider.py (get_or_generate_token /
// get_pool_with_lock / filter_pool).
//
// A two-step token-gated crawler (Twitter, Microsoft, Apple, Zoho, …) needs a
// short-lived, IDENTIFIER-AGNOSTIC session token (a CSRF/cookie/bearer) that it
// normally fetches inline as request step 1. This package keeps a warm pool of
// those tokens per (website, kind): a background loop refills the pool toward a
// target SIZE, evicting tokens past their TTL or USE_LIMIT, so a request can pull
// a ready token and skip step 1. On an empty pool the crawler still generates one
// inline (the faithful get_or_generate_token fallback), so results are identical
// whether or not the pool is warm — the pool is purely a latency/rate-limit win.
//
// The pool is in-memory (a slice guarded by a mutex, one Pool per site+kind, one
// background goroutine each) — it adds no datastore. It is gated so it can be
// turned off via config without a redeploy.
package tokenpool

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/logger"
	"github.com/sign3labs/go-you/internal/metrics"
)

var poolLog = logger.Component("tokenpool")

// Config is the per-site pool tuning, mirroring an entry of the Python
// WEBSITE_TOKEN_POOL_CONFIG_DEFAULT.
type Config struct {
	Size      int           // target warm-token count
	TTL       time.Duration // a token past Created+TTL is evicted
	UseLimit  int           // a token at >= UseLimit usages is evicted
	MaxThread int           // max tokens generated concurrently per refill cycle
	Sleep     time.Duration // pause between refill cycles
}

// withDefaults fills unset fields so a partial/zero Config is still safe.
func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = 300 * time.Second
	}
	if c.UseLimit <= 0 {
		c.UseLimit = 5
	}
	if c.MaxThread <= 0 {
		c.MaxThread = 5
	}
	if c.Sleep <= 0 {
		c.Sleep = 5 * time.Second
	}
	if c.Size < 0 {
		c.Size = 0
	}
	return c
}

// generator is the step-1 token fetch a Pool calls off the request path. It is
// crawler.TokenCrawler.GenerateToken bound to a proxy.
type generator func(ctx context.Context) (map[string]string, error)

// Pool holds warm tokens for one (website, kind). Safe for concurrent use.
type Pool struct {
	website string
	kind    crawler.Kind
	cfg     Config
	gen     generator

	mu     sync.Mutex
	tokens []*crawler.Token

	// nowFn / randIntn are seams so tests can control time and selection.
	nowFn    func() time.Time
	randIntn func(int) int
}

// NewPool builds a pool for website+kind using gen to mint tokens.
func NewPool(website string, kind crawler.Kind, cfg Config, gen generator) *Pool {
	return &Pool{
		website:  website,
		kind:     crawler.Kind(kind),
		cfg:      cfg.withDefaults(),
		gen:      gen,
		nowFn:    time.Now,
		randIntn: rand.Intn,
	}
}

// Get returns a warm token's Values, incrementing its usage — the request-path
// read. ok is false when the pool is empty (the caller then generates inline).
// Faithful to get_token_from_pool: pick a random live token, bump usages.
func (p *Pool) Get() (map[string]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tokens) == 0 {
		metrics.TokenPool.WithLabelValues(p.website, "not_found", "").Inc()
		return nil, false
	}
	i := p.randIntn(len(p.tokens))
	t := p.tokens[i]
	t.Usages++
	metrics.TokenPool.WithLabelValues(p.website, "found", "").Inc()
	// Return a copy of Values so a consumer can't mutate pooled state.
	return copyMap(t.Values), true
}

// filter drops tokens past TTL or at/over USE_LIMIT (get_pool_with_lock
// flow='filter'). Caller holds no lock.
func (p *Pool) filter() {
	now := p.nowFn()
	p.mu.Lock()
	kept := p.tokens[:0:0]
	for _, t := range p.tokens {
		expired := !t.Created.IsZero() && now.After(t.Created.Add(p.cfg.TTL))
		used := t.Usages >= p.cfg.UseLimit
		if !expired && !used {
			kept = append(kept, t)
		}
	}
	p.tokens = kept
	size := len(p.tokens)
	p.mu.Unlock()
	metrics.TokenPoolSize.WithLabelValues(p.website, string(p.kind)).Set(float64(size))
}

// add inserts a freshly generated token at the front (add_token_to_pool).
func (p *Pool) add(values map[string]string) {
	p.mu.Lock()
	p.tokens = append([]*crawler.Token{{Values: values, Usages: 0, Created: p.nowFn()}}, p.tokens...)
	size := len(p.tokens)
	p.mu.Unlock()
	metrics.TokenPool.WithLabelValues(p.website, "add_succ", "").Inc()
	metrics.TokenPoolSize.WithLabelValues(p.website, string(p.kind)).Set(float64(size))
}

func (p *Pool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tokens)
}

// shrink trims the pool down to Size when it overflows (the pool_creator shrink
// branch: e.g. after a config SIZE decrease).
func (p *Pool) shrink() {
	p.mu.Lock()
	if len(p.tokens) > p.cfg.Size {
		poolLog.Info("removing tokens", "website", p.website, "kind", string(p.kind),
			"have", len(p.tokens), "want", p.cfg.Size)
		if p.cfg.Size == 0 {
			p.tokens = nil
		} else {
			p.tokens = p.tokens[:p.cfg.Size]
		}
	}
	size := len(p.tokens)
	p.mu.Unlock()
	metrics.TokenPoolSize.WithLabelValues(p.website, string(p.kind)).Set(float64(size))
}

// refillOnce runs one cycle: filter → compute how many to mint → generate them
// concurrently → add. Mirrors pool_creator's body (cap at 10 then at MaxThread).
// It returns (attempted, succeeded) so the run loop can back off a pool whose
// upstream keeps rejecting (e.g. Microsoft 403 through a non-allowlisted proxy),
// instead of hammering it every Sleep interval.
func (p *Pool) refillOnce(ctx context.Context) (attempted, succeeded int) {
	p.filter()

	need := p.cfg.Size - p.size()
	if need <= 0 {
		p.shrink()
		return 0, 0
	}
	if need > 10 {
		need = 10 // pool_creator hard cap per cycle
	}
	if need > p.cfg.MaxThread {
		need = p.cfg.MaxThread
	}
	poolLog.Info("tokens to generate", "count", need, "website", p.website, "kind", string(p.kind))

	var wg sync.WaitGroup
	var ok int64
	for i := 0; i < need; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values, err := p.gen(ctx)
			if err != nil {
				metrics.TokenPool.WithLabelValues(p.website, "add_fail", metrics.ErrorClass("failed", err.Error())).Inc()
				poolLog.Warn("token generation failed", "website", p.website, "kind", string(p.kind), "err", err.Error())
				return
			}
			if len(values) == 0 {
				metrics.TokenPool.WithLabelValues(p.website, "add_fail", "empty").Inc()
				poolLog.Warn("token generation returned empty", "website", p.website, "kind", string(p.kind))
				return
			}
			p.add(values)
			atomic.AddInt64(&ok, 1)
			poolLog.Debug("token generated", "website", p.website, "kind", string(p.kind))
		}()
	}
	wg.Wait()
	return need, int(ok)
}

// maxBackoff caps how long a persistently-failing pool waits between cycles, so
// an upstream that always rejects (e.g. Microsoft 403 through a proxy it doesn't
// allowlist, or Quora's Safari-TLS gate) does not become a hot retry loop
// hammering the endpoint every Sleep interval.
const maxBackoff = 5 * time.Minute

// run is the per-pool background loop until ctx is cancelled. It sleeps for the
// configured interval on a healthy cycle, but backs off EXPONENTIALLY when a
// cycle needed tokens yet minted none (all gens failed) — doubling the wait up
// to maxBackoff. Any success (or a full pool that needs nothing) resets the wait
// to the normal Sleep. This keeps a broken pool from spamming its upstream while
// leaving healthy pools at their normal cadence.
func (p *Pool) run(ctx context.Context) {
	poolLog.Info("pool started", "website", p.website, "kind", string(p.kind),
		"size", p.cfg.Size, "ttl_s", int(p.cfg.TTL.Seconds()), "use_limit", p.cfg.UseLimit)
	wait := p.cfg.Sleep
	for {
		var attempted, succeeded int
		func() {
			defer func() {
				if r := recover(); r != nil {
					poolLog.Error("pool cycle panic recovered", "website", p.website, "panic", toStr(r))
				}
			}()
			attempted, succeeded = p.refillOnce(ctx)
		}()

		switch {
		case attempted > 0 && succeeded == 0:
			// Every generation failed this cycle — back off (double, capped).
			wait *= 2
			if wait > maxBackoff {
				wait = maxBackoff
			}
			poolLog.Warn("pool backing off (all token gens failed)",
				"website", p.website, "kind", string(p.kind), "next_retry_s", int(wait.Seconds()))
		default:
			// Success, partial success, or nothing needed — resume normal cadence.
			wait = p.cfg.Sleep
		}

		select {
		case <-ctx.Done():
			poolLog.Info("pool stopped", "website", p.website, "kind", string(p.kind))
			return
		case <-time.After(wait):
		}
	}
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toStr(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "non-string panic"
}
