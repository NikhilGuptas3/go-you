package tokenpool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sign3labs/go-you/internal/crawler"
)

// newTestPool builds a pool with a controllable clock and deterministic random
// selection (always index 0), plus a generator counter.
func newTestPool(cfg Config, gen generator) (*Pool, *int64, *time.Time) {
	var clock time.Time
	clock = time.Unix(1_700_000_000, 0)
	var calls int64
	wrapped := func(ctx context.Context) (map[string]string, error) {
		atomic.AddInt64(&calls, 1)
		return gen(ctx)
	}
	p := NewPool("TEST", crawler.KindPhone, cfg, wrapped)
	p.nowFn = func() time.Time { return clock }
	p.randIntn = func(int) int { return 0 }
	return p, &calls, &clock
}

func okGen(_ context.Context) (map[string]string, error) {
	return map[string]string{"csrf": "x"}, nil
}

func TestRefillFillsToSize(t *testing.T) {
	p, calls, _ := newTestPool(Config{Size: 3, TTL: time.Minute, UseLimit: 5, MaxThread: 5, Sleep: time.Hour}, okGen)
	p.refillOnce(context.Background())
	if p.size() != 3 {
		t.Fatalf("size=%d want 3", p.size())
	}
	if *calls != 3 {
		t.Fatalf("gen calls=%d want 3", *calls)
	}
	// A second cycle should mint nothing (already full).
	p.refillOnce(context.Background())
	if *calls != 3 {
		t.Fatalf("gen calls after full=%d want 3", *calls)
	}
}

func TestGetIncrementsUsageAndMissesWhenEmpty(t *testing.T) {
	p, _, _ := newTestPool(Config{Size: 1, TTL: time.Minute, UseLimit: 5}, okGen)
	if _, ok := p.Get(); ok {
		t.Fatal("empty pool should miss")
	}
	p.refillOnce(context.Background())
	if _, ok := p.Get(); !ok {
		t.Fatal("warm pool should hit")
	}
	// usages incremented on the single token.
	p.mu.Lock()
	u := p.tokens[0].Usages
	p.mu.Unlock()
	if u != 1 {
		t.Fatalf("usages=%d want 1", u)
	}
}

func TestFilterEvictsOnTTL(t *testing.T) {
	p, _, clock := newTestPool(Config{Size: 2, TTL: 10 * time.Second, UseLimit: 5, MaxThread: 5, Sleep: time.Hour}, okGen)
	p.refillOnce(context.Background())
	if p.size() != 2 {
		t.Fatalf("size=%d want 2", p.size())
	}
	*clock = clock.Add(11 * time.Second) // past TTL
	p.filter()
	if p.size() != 0 {
		t.Fatalf("size after TTL=%d want 0", p.size())
	}
}

func TestFilterEvictsOnUseLimit(t *testing.T) {
	p, _, _ := newTestPool(Config{Size: 1, TTL: time.Hour, UseLimit: 2, MaxThread: 5, Sleep: time.Hour}, okGen)
	p.refillOnce(context.Background())
	p.Get() // usages 1
	p.Get() // usages 2 == UseLimit
	p.filter()
	if p.size() != 0 {
		t.Fatalf("size after use-limit=%d want 0", p.size())
	}
}

func TestRefillCapsAtTen(t *testing.T) {
	// Size 50 but a cycle mints at most 10, and MaxThread further caps.
	p, calls, _ := newTestPool(Config{Size: 50, TTL: time.Hour, UseLimit: 100, MaxThread: 4, Sleep: time.Hour}, okGen)
	p.refillOnce(context.Background())
	if *calls != 4 { // min(10, MaxThread=4)
		t.Fatalf("gen calls=%d want 4 (MaxThread cap)", *calls)
	}
}

func TestGenFailureDoesntAdd(t *testing.T) {
	failGen := func(_ context.Context) (map[string]string, error) { return nil, fmt.Errorf("boom") }
	p, _, _ := newTestPool(Config{Size: 3, TTL: time.Hour, UseLimit: 5, MaxThread: 5, Sleep: time.Hour}, failGen)
	p.refillOnce(context.Background())
	if p.size() != 0 {
		t.Fatalf("size=%d want 0 (all gens failed)", p.size())
	}
}

// refillOnce reports (attempted, succeeded) so the run loop can back off a
// broken upstream. Pin that contract.
func TestRefillReportsCounts(t *testing.T) {
	// All fail => attempted>0, succeeded==0 (the back-off trigger).
	failGen := func(_ context.Context) (map[string]string, error) { return nil, fmt.Errorf("403") }
	p, _, _ := newTestPool(Config{Size: 4, TTL: time.Hour, UseLimit: 5, MaxThread: 4, Sleep: time.Hour}, failGen)
	att, ok := p.refillOnce(context.Background())
	if att != 4 || ok != 0 {
		t.Fatalf("all-fail cycle: attempted=%d succeeded=%d want 4,0", att, ok)
	}
	// All succeed => attempted==succeeded.
	p2, _, _ := newTestPool(Config{Size: 3, TTL: time.Hour, UseLimit: 5, MaxThread: 3, Sleep: time.Hour}, okGen)
	att2, ok2 := p2.refillOnce(context.Background())
	if att2 != 3 || ok2 != 3 {
		t.Fatalf("all-ok cycle: attempted=%d succeeded=%d want 3,3", att2, ok2)
	}
	// Full pool => nothing attempted (no back-off).
	att3, ok3 := p2.refillOnce(context.Background())
	if att3 != 0 || ok3 != 0 {
		t.Fatalf("full pool: attempted=%d succeeded=%d want 0,0", att3, ok3)
	}
}

func TestShrinkTrimsOverflow(t *testing.T) {
	p, _, _ := newTestPool(Config{Size: 2, TTL: time.Hour, UseLimit: 5, MaxThread: 5, Sleep: time.Hour}, okGen)
	// Manually overfill, then shrink.
	for i := 0; i < 5; i++ {
		p.add(map[string]string{"csrf": "x"})
	}
	p.shrink()
	if p.size() != 2 {
		t.Fatalf("size after shrink=%d want 2", p.size())
	}
}

func TestManagerMissWhenNilOrUnregistered(t *testing.T) {
	var m *Manager
	if _, ok := m.GetToken("X", crawler.KindPhone); ok {
		t.Fatal("nil manager should miss")
	}
	m2 := NewManager(nil)
	if _, ok := m2.GetToken("X", crawler.KindPhone); ok {
		t.Fatal("unregistered site should miss")
	}
}

// Race: concurrent Get + refill must not data-race (run with -race).
func TestConcurrentGetAndRefill(t *testing.T) {
	p, _, _ := newTestPool(Config{Size: 5, TTL: time.Hour, UseLimit: 1000, MaxThread: 5, Sleep: time.Hour}, okGen)
	p.refillOnce(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Get() }()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.refillOnce(context.Background()) }()
	}
	wg.Wait()
}
