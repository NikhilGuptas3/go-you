package handler

import (
	"context"
	"time"

	"github.com/sign3labs/go-you/internal/metrics"
	"github.com/sign3labs/go-you/internal/model"
)

// sectionLane is the extra contract the phone/email lanes expose so the persona
// cache can be applied generically (the common lane is NOT a sectionLane — it has
// no persona cache). It lets the cacheDecorator read/write a section without
// knowing whether it's phone or email:
//   - cacheKind is "phone"/"email" (the OrganicData key type)
//   - loginID is the value hashed into the cache key (international phone / email)
//   - get/set move the *model.Section in and out of the response
//
// phoneLane and emailLane implement this alongside Lane.
type sectionLane interface {
	Lane
	cacheKind() string
	loginID(st *laneState) string
	getSection(resp *model.PersonaResponse) *model.Section
	setSection(resp *model.PersonaResponse, sec *model.Section)
}

// cacheDecorator wraps a sectionLane with the OrganicData persona cache:
// read-before (a hit replays the cached section and SKIPS the inner Run) and
// write-after (a fresh section is persisted fire-and-forget). This replaces the
// three inlined cache blocks that used to live in Build. It is itself a Lane, so
// the pipeline treats a cached lane exactly like any other.
//
// Caching is active only when the persona cache is configured AND the tenant's
// youConfig.caching gate is on; otherwise the decorator is a transparent
// pass-through to the inner lane.
type cacheDecorator struct {
	inner sectionLane
	// hit records whether this request's section came from the cache, so Run
	// knows to skip the write-back. It is set during Gate (the read happens there,
	// before the pipeline decides to run the lane) and read during Run — both on
	// the same goroutine per lane, so no synchronization is needed.
	hit bool
	// key/now are captured at read time for the write-back.
	key string
	now int64
}

// newCachedSectionLane wraps a section lane so it participates in the persona cache.
func newCachedSectionLane(inner sectionLane) *cacheDecorator {
	return &cacheDecorator{inner: inner}
}

func (c *cacheDecorator) Name() string { return c.inner.Name() }

// Gate decides whether the inner lane must actually run. It first applies the
// inner gate (is there a phone/email at all?). If caching is on, it performs the
// cache READ here: on a hit it populates the section, records the hit, and
// returns false so the pipeline SKIPS Run (no crawl). On a miss it returns the
// inner gate's verdict so Run proceeds and crawls.
func (c *cacheDecorator) Gate(o *Orchestrator, st *laneState) bool {
	if !c.inner.Gate(o, st) {
		return false
	}
	if o.deps.PersonaCache == nil || !st.yc.IsCachingEnabled() {
		return true // caching off: run normally, no read/write
	}
	c.now = time.Now().Unix()
	kind := c.inner.cacheKind()
	c.key = o.deps.PersonaCache.Key(kind, c.inner.loginID(st), st.tenantID)
	if cached, hit, err := o.deps.PersonaCache.Get(context.Background(), c.key, c.now); err == nil && hit {
		c.hit = true
		c.inner.setSection(st.resp, sectionFromCache(cached, kind))
		st.tm.record("cache_"+kind, 0)
		// hey-you carries hit/miss as the STATUS LABEL of one counter
		// (real_time_cache), not as separate metrics.
		metrics.RealTimeCache.WithLabelValues(kind, "hit").Inc()
		return false // hit: skip the inner Run (no crawl)
	}
	metrics.RealTimeCache.WithLabelValues(kind, "miss").Inc()
	return true // miss: run the inner lane
}

// Run executes the inner lane (only reached on a cache miss, since a hit made
// Gate return false), then writes the fresh section back to the cache
// fire-and-forget. A no-cache request (c.key == "") skips the write.
func (c *cacheDecorator) Run(ctx context.Context, o *Orchestrator, st *laneState) {
	c.inner.Run(ctx, o, st)
	if c.hit || c.key == "" || o.deps.PersonaCache == nil {
		return
	}
	sec := c.inner.getSection(st.resp)
	if sec == nil {
		return
	}
	key, now := c.key, c.now
	pc := o.deps.PersonaCache
	kind := c.inner.cacheKind()
	go func() {
		_ = pc.Put(context.Background(), key, wrapSection(sec, kind), now)
	}()
}

// sectionFromCache pulls the stored section out of a cached single-section
// PersonaResponse (written by wrapSection). The write path stores the section
// under its own field, so read it back from the matching one.
func sectionFromCache(cached *model.PersonaResponse, kind string) *model.Section {
	if cached == nil {
		return nil
	}
	if kind == "phone" {
		return cached.PhoneData
	}
	return cached.EmailData
}

// wrapSection builds the single-section PersonaResponse the persona cache stores
// (matching the per-type Python write: one section per key).
func wrapSection(sec *model.Section, kind string) *model.PersonaResponse {
	if kind == "phone" {
		return &model.PersonaResponse{PhoneData: sec}
	}
	return &model.PersonaResponse{EmailData: sec}
}

// --- sectionLane implementations for phone/email (Lane methods live in lane.go) ---

func (phoneLane) cacheKind() string { return "phone" }
func (phoneLane) loginID(st *laneState) string {
	return normalizePhone(st.req.Phone.CountryCode, st.req.Phone.Number)
}
func (phoneLane) getSection(resp *model.PersonaResponse) *model.Section { return resp.PhoneData }
func (phoneLane) setSection(resp *model.PersonaResponse, sec *model.Section) {
	resp.PhoneData = sec
}

func (emailLane) cacheKind() string                                     { return "email" }
func (emailLane) loginID(st *laneState) string                          { return st.req.Email }
func (emailLane) getSection(resp *model.PersonaResponse) *model.Section { return resp.EmailData }
func (emailLane) setSection(resp *model.PersonaResponse, sec *model.Section) {
	resp.EmailData = sec
}
