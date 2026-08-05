package handler

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/model"
)

// A Lane is one independent contributor to the persona response: the phone
// section, the email section, or the common_data enrich block. Lanes run
// concurrently in Build's fan-out and each writes its OWN field of the response
// (resp.PhoneData / resp.EmailData / resp.CommonData), so no lane races another.
//
// This mirrors the crawler.Crawler seam one level up: adding a new fan-out lane
// is a new Lane implementation registered in Orchestrator.lanes(), not surgery on
// Build. Gate decides whether the lane runs for this request; Run does the work
// and writes into st.resp. Cross-cutting behavior (e.g. persona caching) is added
// by WRAPPING a Lane (see cacheDecorator), not by editing it.
type Lane interface {
	// Name is the lane's short identifier (used for timing/debug).
	Name() string
	// Gate reports whether this lane should run for the given request/state.
	Gate(o *Orchestrator, st *laneState) bool
	// Run executes the lane and writes its result into st.resp. It must only
	// touch its own response field so concurrent lanes stay race-free.
	Run(ctx context.Context, o *Orchestrator, st *laneState)
}

// laneState is the per-request context shared by the lanes: the decoded request,
// the resolved youConfig, the derived crawl sets, the timing collector, and the
// response being assembled. Fields are read by all lanes; each lane writes only
// its own slice of resp.
type laneState struct {
	req        *model.PersonaRequest
	yc         *appconfig.YouConfiguration
	tenantID   string
	phoneSites []string
	emailSites []string
	tm         *timings
	resp       *model.PersonaResponse
}

// runLanes runs every gated lane concurrently under a single errgroup bound to
// ctx (the leaf-only request deadline). Because each lane writes a distinct
// response field, there is no shared-write race and no mutex is needed; errgroup
// gives structured concurrency (one place to wait, shared ctx) in place of the
// old hand-rolled WaitGroup. Lanes never return errors (they degrade to
// empty/error forms internally), so the group error is ignored.
func (o *Orchestrator) runLanes(ctx context.Context, lanes []Lane, st *laneState) {
	fanoutStart := time.Now()
	g, gctx := errgroup.WithContext(ctx)
	for _, ln := range lanes {
		if !ln.Gate(o, st) {
			continue
		}
		ln := ln
		g.Go(func() error {
			ln.Run(gctx, o, st)
			return nil
		})
	}
	_ = g.Wait()
	st.tm.since("fanout_total", fanoutStart)
}

// lanes returns the persona fan-out lanes in a stable order. Section lanes are
// wrapped with the persona-cache decorator (read-before/write-after); the common
// lane has no persona cache. To add a fan-out contributor, add its Lane here.
func (o *Orchestrator) lanes() []Lane {
	return []Lane{
		newCachedSectionLane(phoneLane{}),
		newCachedSectionLane(emailLane{}),
		commonLane{},
	}
}

// --- phone lane ---

type phoneLane struct{}

func (phoneLane) Name() string { return "phone" }

func (phoneLane) Gate(_ *Orchestrator, st *laneState) bool {
	return st.req.Phone != nil
}

func (phoneLane) Run(ctx context.Context, o *Orchestrator, st *laneState) {
	st.resp.PhoneData = o.buildPhoneSection(ctx, st.req.Phone, st.tm, st.phoneSites, st.yc)
}

// --- email lane ---

type emailLane struct{}

func (emailLane) Name() string { return "email" }

func (emailLane) Gate(_ *Orchestrator, st *laneState) bool {
	return st.req.Email != ""
}

func (emailLane) Run(ctx context.Context, o *Orchestrator, st *laneState) {
	st.resp.EmailData = o.buildEmailSection(ctx, st.req.Email, st.tm, st.emailSites, st.yc)
}

// --- common_data lane ---

type commonLane struct{}

func (commonLane) Name() string { return "common_data" }

func (commonLane) Gate(o *Orchestrator, _ *laneState) bool {
	return o.deps.Common != nil
}

func (commonLane) Run(ctx context.Context, o *Orchestrator, st *laneState) {
	commonStart := time.Now()
	if cd := o.deps.Common.Fetch(ctx, st.req, st.yc); len(cd) > 0 {
		st.resp.CommonData = cd
	}
	st.tm.since("common_data", commonStart)
}
