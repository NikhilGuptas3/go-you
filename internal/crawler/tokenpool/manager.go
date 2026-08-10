package tokenpool

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/sign3labs/go-you/internal/crawler"
)

// DefaultConfigs mirrors the in-scope entries of the Python
// WEBSITE_TOKEN_POOL_CONFIG_DEFAULT (constants/config_constants.py:423+), keyed
// by website id. Only the identifier-agnostic two-step sites go-you pools are
// listed. Values (SIZE/TTL/USE_LIMIT) are carried verbatim; SNAPDEAL's Python
// SIZE of 300 is capped to a sane in-memory default (a warm pool that large is
// pointless for a POC and would hammer the token endpoint at boot).
var DefaultConfigs = map[string]Config{
	"APPLE":      {Size: 15, TTL: 300 * time.Second, UseLimit: 3},
	"MICROSOFT":  {Size: 10, TTL: 300 * time.Second, UseLimit: 20},
	"TWITTER":    {Size: 5, TTL: 300 * time.Second, UseLimit: 10},
	"QUORA":      {Size: 5, TTL: 300 * time.Second, UseLimit: 30},
	"TUMBLR":     {Size: 5, TTL: 300 * time.Second, UseLimit: 30},
	"VIMEO":      {Size: 5, TTL: 300 * time.Second, UseLimit: 30},
	"ZOHO":       {Size: 15, TTL: 25 * time.Second, UseLimit: 1},
	"OYOROOMS":   {Size: 5, TTL: 300 * time.Second, UseLimit: 30},
	"TRIVAGO":    {Size: 10, TTL: 300 * time.Second, UseLimit: 50},
	"EVENTBRITE": {Size: 15, TTL: 1300 * time.Second, UseLimit: 100},
	"SNAPDEAL":   {Size: 20, TTL: 2000 * time.Second, UseLimit: 20}, // Python SIZE 300 capped to 20
	"SHOPCLUES":  {Size: 3, TTL: 20000 * time.Second, UseLimit: 80},
	"CODECADEMY": {Size: 5, TTL: 300 * time.Second, UseLimit: 20}, // not in Python default; sane fallback
}

// key identifies a pool by site+kind (a site can pool separately for phone and
// email — e.g. ZOHO/SHOPCLUES/SNAPDEAL run both flows).
type key struct {
	website string
	kind    crawler.Kind
}

// Manager owns every Pool, starts/stops their background loops, and answers
// request-time token lookups. Nil-safe: a nil *Manager's GetToken always misses
// (so crawlers fall back to inline generation) and Start/Stop are no-ops.
type Manager struct {
	proxyURL *url.URL
	pools    map[key]*Pool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
}

// NewManager builds an empty manager bound to the crawl proxy (pools mint tokens
// through the same proxy the crawlers use).
func NewManager(proxyURL *url.URL) *Manager {
	return &Manager{proxyURL: proxyURL, pools: map[key]*Pool{}}
}

// Register adds a pool for tc using its DefaultConfigs entry (or a zero Config
// with defaults if unlisted). A crawler must implement crawler.TokenCrawler.
// Registering the same site+kind twice is ignored. Register before Start.
func (m *Manager) Register(tc crawler.TokenCrawler) {
	if m == nil || tc == nil {
		return
	}
	k := key{website: tc.Website(), kind: tc.Kind()}
	if _, dup := m.pools[k]; dup {
		return
	}
	cfg := DefaultConfigs[tc.Website()]
	proxy := m.proxyURL
	gen := func(ctx context.Context) (map[string]string, error) {
		return tc.GenerateToken(ctx, proxy)
	}
	m.pools[k] = NewPool(tc.Website(), tc.Kind(), cfg, gen)
}

// GetToken returns a warm token for site+kind, or (nil,false) on a miss (empty
// pool, unregistered site, or nil manager). The crawler generates inline on a
// miss — the get_or_generate_token fallback.
func (m *Manager) GetToken(website string, kind crawler.Kind) (map[string]string, bool) {
	if m == nil {
		return nil, false
	}
	p := m.pools[key{website: website, kind: kind}]
	if p == nil {
		return nil, false
	}
	return p.Get()
}

// Start spawns one background refill goroutine per registered pool. Idempotent;
// no-op on a nil manager or when there are no pools. ctx cancellation (or Stop)
// ends all loops.
func (m *Manager) Start(ctx context.Context) {
	if m == nil || m.started || len(m.pools) == 0 {
		return
	}
	m.started = true
	ctx, m.cancel = context.WithCancel(ctx)
	poolLog.Info("token pool manager starting", "pools", len(m.pools))
	for _, p := range m.pools {
		p := p
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			p.run(ctx)
		}()
	}
}

// Stop cancels all loops and waits for them to exit. Safe to call once after
// Start; no-op otherwise.
func (m *Manager) Stop() {
	if m == nil || !m.started {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}
