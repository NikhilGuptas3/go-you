package handler

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/sign3labs/go-you/internal/analytics"
	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/breach"
	"github.com/sign3labs/go-you/internal/commondata"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/crawler/upi"
	"github.com/sign3labs/go-you/internal/intelligence"
	"github.com/sign3labs/go-you/internal/meta"
	"github.com/sign3labs/go-you/internal/metacache"
	"github.com/sign3labs/go-you/internal/metrics"
	"github.com/sign3labs/go-you/internal/model"
	"github.com/sign3labs/go-you/internal/personacache"
	"github.com/sign3labs/go-you/internal/staticdata"
)

// Deps is the set of lane dependencies for the persona pipeline. Every field is
// optional/nil-safe: a nil dep disables that lane and the response degrades to
// its empty/error form (the contract main.go relies on). Using a named-field
// struct instead of positional args keeps the wiring readable as lanes are added
// (no growing positional constructor) and lets callers set only what they have.
type Deps struct {
	Runner    *crawler.Runner
	PhoneMeta *meta.PhoneMetaService
	EmailMeta *meta.EmailMetaService
	Breach    *breach.Service
	Intel     *intelligence.Service
	// Static is the MySQL-backed static-persona repo (feeds phone breach,
	// digital_age, linked_ids). nil in LOCAL_DEV / when no DB — the derived
	// signals then degrade to their empty/error forms.
	Static *staticdata.Repo
	// PersonaCache is the DynamoDB OrganicData persona cache (read-before-crawl,
	// write-after). nil => caching off (always crawl). Gated per-tenant by
	// youConfig.caching.
	PersonaCache *personacache.Repo
	// MetaCache is the DynamoDB EmailPhoneMeta cache for the phone/email meta lane.
	// nil => meta caching off. Gated by phone_info.caching / email_info.caching.
	MetaCache *metacache.Repo
	// Common is the enrichdata.in common_data service (up to 6 enrich checks).
	// nil => feature off (no common_data block). Gated per-tenant by the
	// intelligence/common_intelligence sub-flags inside the service.
	Common *commondata.Service
	// Config is the ConfigFetcher (per-tenant youConfig gates, global settings).
	// nil in LOCAL_DEV where MySQL — and therefore the configs table — is absent.
	Config *appconfig.Fetcher
	// Sink is the Kinesis analytics sink. It is transport-level (the event is built
	// from the *http.Request), so the Orchestrator ignores it — Persona reads it.
	Sink *analytics.Sink
}

// Orchestrator is the persona application/service layer: it owns the lane
// dependencies (crawlers, meta, breach, intelligence, common_data, static repo,
// caches) and turns a decoded request into a fully-assembled *model.PersonaResponse.
// It is transport-agnostic — no *http.Request, no ResponseWriter — so the HTTP
// handler (Persona) is left as decode → Build → transform → encode.
type Orchestrator struct {
	deps Deps
}

// NewOrchestrator builds the application-layer service from its lane deps.
func NewOrchestrator(deps Deps) *Orchestrator {
	return &Orchestrator{deps: deps}
}

// Build runs the full persona pipeline for one request and returns the assembled
// response (post-intelligence, post-status, PRE-transform) plus the resolved
// youConfig the transport layer needs for the client transform. requestID is
// stamped on the response; tenant drives config; tm collects stage timings.
//
// This is the orchestration extracted verbatim from the old Persona.Handle: cache
// read → concurrent phone/email/common fan-out → cache write-back → intelligence
// merge → top-level status. It does no transport work.
func (o *Orchestrator) Build(ctx context.Context, req *model.PersonaRequest, tenant *auth.Tenant, requestID string, tm *timings) (*model.PersonaResponse, *appconfig.YouConfiguration) {
	tenantID := ""
	if tenant != nil {
		tenantID = tenant.ID
	}

	// Resolve the tenant's youConfig once: it drives the per-kind crawl sets AND
	// the meta feature gates (phone_meta/email_meta/postpaid). On any failure
	// (LOCAL_DEV fake tenant, missing/invalid config, no fetcher) yc is nil and
	// the crawl sets are nil (run every registered crawler) — meta then runs
	// with permissive defaults so the service still works without a configs table.
	yc, phoneSites, emailSites := o.resolveConfig(tenant)

	st := &laneState{
		req:        req,
		yc:         yc,
		tenantID:   tenantID,
		phoneSites: phoneSites,
		emailSites: emailSites,
		tm:         tm,
		resp:       &model.PersonaResponse{RequestID: requestID},
	}

	// Fan out the lanes concurrently. Each lane writes its own response field;
	// the section lanes are wrapped with the persona-cache decorator (read-before/
	// write-after), so a cache hit replays the section and skips the crawl.
	// Mirrors Python's phone/email/common futures under one deadline.
	o.runLanes(ctx, o.lanes(), st)
	resp := st.resp

	// Intelligence (remote ml_service) runs after both sections resolve — it
	// sends the assembled response + request to ml_service and merges the score
	// back into per-section and common intelligence_data, then derives the
	// prediction. Gated on tenant common_intelligence.enabled inside the service.
	if o.deps.Intel != nil && yc != nil && yc.IsCommonIntelligenceEnabled() {
		intelStart := time.Now()
		o.applyIntelligence(ctx, req, resp, yc, tenantID)
		tm.since("intelligence", intelStart)
	}

	// Top-level status from the section status codes (compute_top_level_status).
	resp.StatusCode, resp.Status = computeTopLevelStatus(resp)

	return resp, yc
}

// upiConfig returns the registered UPI crawler's parsed config (for the
// transform's CLIENT_RESPONSE / verified-names handling), or nil when UPI is not
// registered (LOCAL_DEV).
func (o *Orchestrator) upiConfig() *upi.Config {
	c := o.deps.Runner.Lookup(crawler.KindPhone, "UPI")
	if uc, ok := c.(*crawler.UPICrawler); ok {
		return uc.Config()
	}
	return nil
}

// resolveConfig parses the tenant youConfig and derives the per-kind crawl
// sets. Returns (nil, nil, nil) when the fetcher/config is unavailable — the
// caller then runs every registered crawler and applies permissive meta gates.
func (o *Orchestrator) resolveConfig(tenant *auth.Tenant) (yc *appconfig.YouConfiguration, phoneSites, emailSites []string) {
	if o.deps.Config == nil || tenant == nil || tenant.Config == "" {
		return nil, nil, nil
	}
	parsed, err := appconfig.ParseYouConfig(tenant.Config)
	if err != nil {
		return nil, nil, nil
	}
	globalDisabled := appconfig.GlobalDisabled(o.deps.Config)
	phoneSites = appconfig.CrawlSet("phone", o.deps.Runner.Available(crawler.KindPhone), parsed, globalDisabled)
	emailSites = appconfig.CrawlSet("email", o.deps.Runner.Available(crawler.KindEmail), parsed, globalDisabled)
	return parsed, phoneSites, emailSites
}

// runCrawlers runs the config-selected sites, or every registered crawler of
// the kind when sites is nil (fallback).
func (o *Orchestrator) runCrawlers(ctx context.Context, kind crawler.Kind, identifier string, sites []string) []crawler.Result {
	if sites == nil {
		return o.deps.Runner.Run(ctx, kind, identifier)
	}
	return o.deps.Runner.RunSites(ctx, kind, identifier, sites)
}

// buildPhoneSection runs the phone crawlers and phone meta concurrently. phone
// is the raw request string (e.g. "9607639515" or "+919607639515"); it is
// normalized to the international form here (parsePhone, India default).
func (o *Orchestrator) buildPhoneSection(ctx context.Context, phone string, tm *timings, sites []string, yc *appconfig.YouConfiguration) *model.Section {
	identifier := parsePhone(phone)

	var (
		results   []crawler.Result
		phoneMeta *model.PhoneMeta
		static    map[string]any
		inner     sync.WaitGroup
	)
	// static login id for phone = country_code+national_number, no '+'
	// (StaticDataService: login_id.country_code + login_id.national_number).
	staticID := strings.TrimPrefix(identifier, "+")

	inner.Add(1)
	go func() { defer inner.Done(); results = o.runCrawlers(ctx, crawler.KindPhone, identifier, sites) }()

	// static_data feeds phone breach + digital_age + linked_ids. Fetch once,
	// concurrently, under the same leaf-only ctx. Only when a signal that needs
	// it is enabled (breach / digital_age / linked_ids).
	if o.deps.Static != nil && phoneStaticNeeded(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			staticStart := time.Now()
			doc, err := o.deps.Static.GetInorganic(ctx, staticID)
			tm.since("static_phone", staticStart)
			metrics.StageStatus.WithLabelValues("static_phone", stageStatusFor(err)).Inc()
			if err == nil {
				static = doc
			}
		}()
	}

	if o.deps.PhoneMeta != nil && phoneMetaOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			// Meta cache (EmailPhoneMeta) read: keyed by the international number.
			// A hit replays the stored PhoneMeta and skips the meta RPCs.
			if o.deps.MetaCache != nil && yc != nil && yc.IsPhoneInfoCachingEnabled() {
				if raw, hit, err := o.deps.MetaCache.Get(ctx, identifier, time.Now().Unix()); err == nil && hit {
					var pm model.PhoneMeta
					if json.Unmarshal(raw, &pm) == nil {
						if pm.Revocations == nil {
							pm.Revocations = map[string]any{}
						}
						phoneMeta = &pm
						tm.record("meta_cache_phone", 0)
						return
					}
				}
			}
			national := nationalFromIdentifier(identifier)
			r := o.deps.PhoneMeta.Fetch(ctx, national, identifier, postpaidOn(yc))
			tm.since("meta_phone", metaStart)
			revocations := r.Revocations
			if revocations == nil {
				// Prod keeps "revocations": {} (clean_empty preserves empty
				// dicts); never emit null.
				revocations = map[string]any{}
			}
			phoneMeta = &model.PhoneMeta{
				PhoneNumber: identifier,
				Operator:    r.Operator,
				Circle:      r.Circle,
				Postpaid:    r.Postpaid,
				Revocations: revocations,
			}
			// Write-back fire-and-forget.
			if o.deps.MetaCache != nil && yc != nil && yc.IsPhoneInfoCachingEnabled() {
				pm := phoneMeta
				go func() { _ = o.deps.MetaCache.Put(context.Background(), identifier, pm, time.Now().Unix()) }()
			}
		}()
	}
	inner.Wait()

	recordCrawlerTimings(ctx, tm, results)
	// Section key is the international number (prod: phone_data.primary_data.key
	// = login_id.international_number, e.g. "+917667701982").
	sec := buildSection("phone", identifier, results)
	if phoneMeta != nil {
		sec.PrimaryData.PhoneMeta = phoneMeta
	}
	// Phone breach: computed from static_data (pawn_service.get_breach_details).
	// With no static repo / no match it yields the empty not-found block.
	if o.deps.Breach != nil && breachOn(yc) {
		sec.PrimaryData.BreachDetails = o.deps.Breach.Phone(identifier, staticID, static)
	}
	// digital_age + linked_ids derived from static_data, gated per-key. Stashed on
	// the section intelligence_data BEFORE applyIntelligence, which then merges the
	// ml_service output on top without clobbering these.
	if intel := phoneStaticIntelligence(yc, static, phoneMeta); intel != nil {
		sec.IntelligenceData = intel
	}
	// Attach static_data so it flows into the ml_service payload (prod parity).
	// transform (remove_static_data) strips it from the client response.
	if len(static) > 0 {
		sec.StaticData = static
	}
	return sec
}

// buildEmailSection runs the email crawlers and email meta concurrently.
func (o *Orchestrator) buildEmailSection(ctx context.Context, email string, tm *timings, sites []string, yc *appconfig.YouConfiguration) *model.Section {
	var (
		results   []crawler.Result
		emailMeta *model.EmailMeta
		breachDet *model.BreachDetails
		static    map[string]any
		inner     sync.WaitGroup
	)
	inner.Add(1)
	go func() { defer inner.Done(); results = o.runCrawlers(ctx, crawler.KindEmail, email, sites) }()

	// static_data feeds email linked_ids (primary_ph). digital_age for email uses
	// the HIBP breach dates, not static_data. Fetch only when linked_ids is on.
	if o.deps.Static != nil && emailStaticNeeded(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			staticStart := time.Now()
			doc, err := o.deps.Static.GetInorganic(ctx, email)
			tm.since("static_email", staticStart)
			metrics.StageStatus.WithLabelValues("static_email", stageStatusFor(err)).Inc()
			if err == nil {
				static = doc
			}
		}()
	}

	if o.deps.EmailMeta != nil && emailMetaOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			// Meta cache (EmailPhoneMeta) read: keyed by the email address.
			if o.deps.MetaCache != nil && yc != nil && yc.IsEmailInfoCachingEnabled() {
				if raw, hit, err := o.deps.MetaCache.Get(ctx, email, time.Now().Unix()); err == nil && hit {
					var em model.EmailMeta
					if json.Unmarshal(raw, &em) == nil {
						emailMeta = &em
						tm.record("meta_cache_email", 0)
						return
					}
				}
			}
			r := o.deps.EmailMeta.Fetch(ctx, email)
			tm.since("meta_email", metaStart)
			em := &model.EmailMeta{Email: email, IsDisposable: r.IsDisposable}
			if r.DomainAttributes != nil {
				em.DomainAttributes = domainAttrsToModel(r.DomainAttributes)
			}
			emailMeta = em
			// Write-back fire-and-forget.
			if o.deps.MetaCache != nil && yc != nil && yc.IsEmailInfoCachingEnabled() {
				m := em
				go func() { _ = o.deps.MetaCache.Put(context.Background(), email, m, time.Now().Unix()) }()
			}
		}()
	}

	if o.deps.Breach != nil && breachOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			breachStart := time.Now()
			breachDet = o.deps.Breach.Email(ctx, email)
			tm.since("breach_email", breachStart)
		}()
	}
	inner.Wait()

	recordCrawlerTimings(ctx, tm, results)
	sec := buildSection("email", email, results)
	if emailMeta != nil {
		sec.PrimaryData.EmailMeta = emailMeta
	}
	if breachDet != nil {
		sec.PrimaryData.BreachDetails = breachDet
	}
	// digital_age (from verified breach dates) + linked_ids (from static primary_ph),
	// gated per-key; merged by applyIntelligence afterwards.
	if intel := emailStaticIntelligence(yc, static, breachDet); intel != nil {
		sec.IntelligenceData = intel
	}
	// Attach static_data for the ml_service payload; transform strips it client-side.
	if len(static) > 0 {
		sec.StaticData = static
	}
	return sec
}

// applyIntelligence runs the ml_service merge and attaches intelligence_data to
// the per-section and top-level response, plus the raw prediction (reshaped
// later by the transform's cleanup_prediction). The you_request/you_response
// payloads are the response marshalled to maps and null-stripped, matching the
// Python payload construction.
func (o *Orchestrator) applyIntelligence(ctx context.Context, req *model.PersonaRequest, resp *model.PersonaResponse, yc *appconfig.YouConfiguration, tenantID string) {
	youResponse := toStrippedMap(resp)
	youRequest := toStrippedMap(req)

	// ml_service expects account_details as an OBJECT keyed by website
	// (you_response['phone_data']['primary_data']['account_details']['SKYPE']), the
	// same shape Python sends (account_details is a dict in the you_response before
	// the ml call). go-you carries it as a LIST in the model and only converts to a
	// map in transform (client path), so the ml payload built from the raw response
	// must be keyed here too — otherwise ml_service's .get('SITE') hits a list and
	// every FEATURE_ENGINE feature dies with "'list' object has no attribute 'get'".
	// Unlike transform, we keep the FULL crawl (no client_response/UPI drops): the ml
	// model reads the complete persona.
	keyAccountDetailsForML(youResponse)
	// common_data is NOT part of the ml_service payload: Python attaches
	// you_response.common_data AFTER the ml call (you_service_aggregator.py:1047,
	// vs the ml payload built at :349), so the ml model never sees it. go-you sets
	// resp.CommonData during the fan-out (before this), so strip it here to keep
	// the ml analytic_response identical to prod.
	delete(youResponse, "common_data")

	out := o.deps.Intel.Run(ctx, intelligence.Input{
		HasPhone: parsePhone(req.Phone) != "",
		HasEmail: req.Email != "",
		// Tenant is the authenticated tenant username (tenantapp.id), matching
		// Python's payload["tenant"] = request_context.tenant_app[0]
		// (you_service_aggregator.get_intelligence). ml_service's FEATURE_ENGINE
		// feature store is tenant-scoped: an empty tenant makes every
		// FEATURE_ENGINE feature resolve to {error:true} while the (non-tenant-
		// scoped) PREDICTION model still returns a score — exactly the split
		// observed when this was sent as "".
		Tenant:             tenantID,
		CommonIntelligence: yc.CommonIntelligence,
		YouRequest:         youRequest,
		YouResponse:        youResponse,
	})

	if resp.PhoneData != nil && len(out.PhoneIntel) > 0 {
		resp.PhoneData.IntelligenceData = mergeIntelligenceData(resp.PhoneData.IntelligenceData, out.PhoneIntel)
		recordIntelFeatures(tenantID, "phone", out.PhoneIntel)
	}
	if resp.EmailData != nil && len(out.EmailIntel) > 0 {
		resp.EmailData.IntelligenceData = mergeIntelligenceData(resp.EmailData.IntelligenceData, out.EmailIntel)
		recordIntelFeatures(tenantID, "email", out.EmailIntel)
	}
	if len(out.CommonIntel) > 0 {
		resp.IntelligenceData = mapToIntelligenceData(out.CommonIntel)
		recordIntelFeatures(tenantID, "common", out.CommonIntel)
	}
	// Prediction: reshaped to {identity_fraud_score: score} or {error:true} in
	// Phase 6 (cleanup_prediction); here we stash the raw outcome, gated by the
	// tenant prediction flag.
	if yc.Prediction {
		if out.PredictionError || out.PredictionScore == nil {
			resp.Prediction = map[string]any{"error": true}
			metrics.YouIntelligence.WithLabelValues(tenantID, "prediction", "error").Inc()
		} else {
			// Placeholder key; cleanup_prediction renames to the tenant's
			// output_key_name (default identity_fraud_score) in Phase 6.
			resp.Prediction = map[string]any{"predicted_score": *out.PredictionScore}
			metrics.YouIntelligence.WithLabelValues(tenantID, "prediction", "ok").Inc()
		}
	}
}

// stageStatusFor maps a stage's terminal error to the bounded stage_status
// status label. Used by the internal DB lanes (static_*) that previously had no
// error signal — a failing MySQL fetch now increments stage_status{status=error}
// so Grafana shows the broken stage, not just a slow/empty response.
func stageStatusFor(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// recordIntelFeatures emits one you_intelligence counter per ml_service feature
// returned for a section, matching hey-you's per-feature outcome tracking. The
// status is coarse: a feature whose value carries {error:true} is "error",
// otherwise "ok". feature_name is the ml key (score, digital_age, linked_ids,
// …) — a bounded set the ml_service defines, so the label stays low-cardinality.
func recordIntelFeatures(tenant, section string, feats map[string]any) {
	for name, v := range feats {
		status := "ok"
		if m, ok := v.(map[string]any); ok {
			if e, ok := m["error"].(bool); ok && e {
				status = "error"
			}
		}
		metrics.YouIntelligence.WithLabelValues(tenant, section+"."+name, status).Inc()
	}
}
