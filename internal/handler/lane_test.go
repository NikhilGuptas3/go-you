package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/model"
	"github.com/sign3labs/go-you/internal/personacache"
)

// fakeDynamo is an in-memory personacache.DynamoAPI for exercising the cache
// decorator without AWS.
type fakeDynamo struct {
	mu    sync.Mutex
	items map[string]map[string]ddbtypes.AttributeValue
	puts  int
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}}
}

func (f *fakeDynamo) GetItem(_ context.Context, _, id string) (map[string]ddbtypes.AttributeValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[id], nil
}

func (f *fakeDynamo) PutItem(_ context.Context, _ string, item map[string]ddbtypes.AttributeValue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	id := item["id"].(*ddbtypes.AttributeValueMemberS).Value
	f.items[id] = item
	return nil
}

// countingSection is a sectionLane whose Run just records that it ran and writes
// a marker section. It lets a test assert whether the inner lane executed (i.e.
// whether a cache hit skipped it).
type countingSection struct {
	kind string
	ran  *int32Counter
}

type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) inc()     { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *int32Counter) get() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func (s countingSection) Name() string                            { return s.kind }
func (s countingSection) Gate(_ *Orchestrator, _ *laneState) bool { return true }
func (s countingSection) Run(_ context.Context, _ *Orchestrator, st *laneState) {
	s.ran.inc()
	sec := &model.Section{Key: "crawled", Type: s.kind, StatusCode: sectionStatusSuccess, Status: statusOK}
	s.setSection(st.resp, sec)
}
func (s countingSection) cacheKind() string           { return s.kind }
func (s countingSection) loginID(_ *laneState) string { return "id-" + s.kind }
func (s countingSection) getSection(resp *model.PersonaResponse) *model.Section {
	if s.kind == "phone" {
		return resp.PhoneData
	}
	return resp.EmailData
}
func (s countingSection) setSection(resp *model.PersonaResponse, sec *model.Section) {
	if s.kind == "phone" {
		resp.PhoneData = sec
	} else {
		resp.EmailData = sec
	}
}

func ycCaching(t *testing.T, on bool) *appconfig.YouConfiguration {
	t.Helper()
	body := `{"youConfig": {"caching": ` + map[bool]string{true: "true", false: "false"}[on] + `}}`
	yc, err := appconfig.ParseYouConfig(body)
	if err != nil {
		t.Fatalf("ParseYouConfig: %v", err)
	}
	return yc
}

func newState(yc *appconfig.YouConfiguration) *laneState {
	return &laneState{
		req:      &model.PersonaRequest{Phone: &model.Phone{CountryCode: "91", Number: "9999999999"}},
		yc:       yc,
		tenantID: "t1",
		tm:       newTimings(),
		resp:     &model.PersonaResponse{RequestID: "r1"},
	}
}

// On a cache MISS the inner lane runs and the fresh section is written back.
func TestCacheDecorator_MissRunsAndWritesBack(t *testing.T) {
	dyn := newFakeDynamo()
	pc := personacache.NewWithClient(dyn, "go-you-OrganicData", true)
	o := NewOrchestrator(Deps{PersonaCache: pc})

	ran := &int32Counter{}
	dec := newCachedSectionLane(countingSection{kind: "phone", ran: ran})
	st := newState(ycCaching(t, true))

	// Gate should return true (miss => run), Run executes the inner lane.
	if !dec.Gate(o, st) {
		t.Fatal("Gate should return true on a cache miss")
	}
	dec.Run(context.Background(), o, st)

	if ran.get() != 1 {
		t.Errorf("inner lane should run once on a miss, ran %d", ran.get())
	}
	if st.resp.PhoneData == nil || st.resp.PhoneData.Key != "crawled" {
		t.Errorf("section not populated by the inner lane: %+v", st.resp.PhoneData)
	}
	// write-back is fire-and-forget; poll briefly.
	if !waitForPut(dyn, 1) {
		t.Errorf("expected a cache write-back, puts=%d", dyn.puts)
	}
}

// On a cache HIT the inner lane is SKIPPED and the cached section is replayed.
func TestCacheDecorator_HitSkipsRun(t *testing.T) {
	dyn := newFakeDynamo()
	pc := personacache.NewWithClient(dyn, "go-you-OrganicData", true)
	o := NewOrchestrator(Deps{PersonaCache: pc})

	// Pre-seed: run once (miss) to populate the cache, then a fresh decorator hits.
	ran1 := &int32Counter{}
	warm := newCachedSectionLane(countingSection{kind: "phone", ran: ran1})
	st1 := newState(ycCaching(t, true))
	warm.Gate(o, st1)
	warm.Run(context.Background(), o, st1)
	if !waitForPut(dyn, 1) {
		t.Fatal("warm-up write-back did not happen")
	}

	// Second request: same login id => cache hit.
	ran2 := &int32Counter{}
	dec := newCachedSectionLane(countingSection{kind: "phone", ran: ran2})
	st2 := newState(ycCaching(t, true))
	if dec.Gate(o, st2) {
		t.Fatal("Gate should return false on a cache hit (skip Run)")
	}
	if ran2.get() != 0 {
		t.Errorf("inner lane must NOT run on a hit, ran %d", ran2.get())
	}
	if st2.resp.PhoneData == nil || st2.resp.PhoneData.Key != "crawled" {
		t.Errorf("cached section not replayed: %+v", st2.resp.PhoneData)
	}
}

// With caching OFF the decorator is a transparent pass-through: the inner lane
// runs and nothing is read or written.
func TestCacheDecorator_CachingOffPassThrough(t *testing.T) {
	dyn := newFakeDynamo()
	pc := personacache.NewWithClient(dyn, "go-you-OrganicData", true)
	o := NewOrchestrator(Deps{PersonaCache: pc})

	ran := &int32Counter{}
	dec := newCachedSectionLane(countingSection{kind: "phone", ran: ran})
	st := newState(ycCaching(t, false)) // caching off

	if !dec.Gate(o, st) {
		t.Fatal("Gate should return true when caching is off")
	}
	dec.Run(context.Background(), o, st)

	if ran.get() != 1 {
		t.Errorf("inner lane should run once, ran %d", ran.get())
	}
	if dyn.puts != 0 {
		t.Errorf("no write-back expected when caching is off, puts=%d", dyn.puts)
	}
}

func waitForPut(dyn *fakeDynamo, want int) bool {
	for i := 0; i < 200; i++ {
		dyn.mu.Lock()
		n := dyn.puts
		dyn.mu.Unlock()
		if n >= want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
