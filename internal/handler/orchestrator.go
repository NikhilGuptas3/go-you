package handler

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/breach"
	"github.com/sign3labs/go-you/internal/commondata"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/crawler/upi"
	"github.com/sign3labs/go-you/internal/intelligence"
	"github.com/sign3labs/go-you/internal/meta"
	"github.com/sign3labs/go-you/internal/metacache"
	"github.com/sign3labs/go-you/internal/model"
	"github.com/sign3labs/go-you/internal/personacache"
	"github.com/sign3labs/go-you/internal/staticdata"
)

// Orchestrator is the persona application/service layer: it owns the lane
// dependencies (crawlers, meta, breach, intelligence, common_data, static repo,
// caches) and turns a decoded request into a fully-assembled *model.PersonaResponse.
// It is transport-agnostic — no *http.Request, no ResponseWriter — so the HTTP
// handler (Persona) is left as decode → Build → transform → encode.
//
// Every dependency is optional/nil-safe: a nil field disables that lane and the
// response degrades to its empty/error form (the same contract main.go relies on).
type Orchestrator struct {
	runner    *crawler.Runner
	phoneMeta *meta.PhoneMetaService
	emailMeta *meta.EmailMetaService
	breach    *breach.Service
	intel     *intelligence.Service
	// static is the MySQL-backed static-persona repo (feeds phone breach,
	// digital_age, linked_ids). nil in LOCAL_DEV / when no DB — the derived
	// signals then degrade to their empty/error forms.
	static *staticdata.Repo
	// pcache is the DynamoDB OrganicData persona cache (read-before-crawl,
	// write-after). nil => caching off (always crawl). Gated per-tenant by
	// youConfig.caching.
	pcache *personacache.Repo
	// mcache is the DynamoDB EmailPhoneMeta cache for the phone/email meta lane.
	// nil => meta caching off. Gated by phone_info.caching / email_info.caching.
	mcache *metacache.Repo
	// common is the enrichdata.in common_data service (up to 6 enrich checks).
	// nil => feature off (no common_data block). Gated per-tenant by the
	// intelligence/common_intelligence sub-flags inside the service.
	common *commondata.Service
	// cfg is the ConfigFetcher (per-tenant youConfig gates, global settings).
	// nil in LOCAL_DEV where MySQL — and therefore the configs table — is absent.
	cfg *appconfig.Fetcher
}

// NewOrchestrator builds the application-layer service from its lane deps.
func NewOrchestrator(runner *crawler.Runner, phoneMeta *meta.PhoneMetaService, emailMeta *meta.EmailMetaService, breachSvc *breach.Service, intel *intelligence.Service, static *staticdata.Repo, cfg *appconfig.Fetcher, pcache *personacache.Repo, mcache *metacache.Repo, common *commondata.Service) *Orchestrator {
	return &Orchestrator{
		runner:    runner,
		phoneMeta: phoneMeta,
		emailMeta: emailMeta,
		breach:    breachSvc,
		intel:     intel,
		static:    static,
		pcache:    pcache,
		mcache:    mcache,
		common:    common,
		cfg:       cfg,
	}
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
	resp := &model.PersonaResponse{RequestID: requestID}

	// Resolve the tenant's youConfig once: it drives the per-kind crawl sets AND
	// the meta feature gates (phone_meta/email_meta/postpaid). On any failure
	// (LOCAL_DEV fake tenant, missing/invalid config, no fetcher) yc is nil and
	// the crawl sets are nil (run every registered crawler) — meta then runs
	// with permissive defaults so the service still works without a configs table.
	yc, phoneSites, emailSites := o.resolveConfig(tenant)

	// Persona cache (DynamoDB OrganicData): read before crawling. Gated by
	// youConfig.caching. A hit replays the cached section and skips that section's
	// crawl entirely (mirroring get_organic_persona → skip get_persona_by_type).
	// Phone and email cache independently under separate keys (two primary_cache_ids).
	now := time.Now().Unix()
	cacheOn := o.pcache != nil && yc.IsCachingEnabled()
	var phoneKey, emailKey string
	var phoneHit, emailHit bool
	if cacheOn {
		if req.Phone != nil {
			phoneKey = o.pcache.Key("phone", normalizePhone(req.Phone.CountryCode, req.Phone.Number), tenantID)
			if cached, hit, err := o.pcache.Get(ctx, phoneKey, now); err == nil && hit {
				resp.PhoneData, phoneHit = cached.PhoneData, true
				tm.record("cache_phone", 0)
			}
		}
		if req.Email != "" {
			emailKey = o.pcache.Key("email", req.Email, tenantID)
			if cached, hit, err := o.pcache.Get(ctx, emailKey, now); err == nil && hit {
				resp.EmailData, emailHit = cached.EmailData, true
				tm.record("cache_email", 0)
			}
		}
	}

	// Phone branch and email branch run concurrently; within each, the crawler
	// fan-out and the meta lookup run concurrently too — matching Python's
	// per-branch parallel sub-tasks. A section that hit the cache is NOT re-crawled.
	// The common_data (enrichdata.in) service runs concurrently too, mirroring
	// Python's common_future submitted alongside the phone/email futures.
	fanoutStart := time.Now()
	var wg sync.WaitGroup
	if req.Phone != nil && !phoneHit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.PhoneData = o.buildPhoneSection(ctx, req.Phone, tm, phoneSites, yc)
		}()
	}
	if req.Email != "" && !emailHit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.EmailData = o.buildEmailSection(ctx, req.Email, tm, emailSites, yc)
		}()
	}
	if o.common != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			commonStart := time.Now()
			if cd := o.common.Fetch(ctx, req, yc); len(cd) > 0 {
				resp.CommonData = cd
			}
			tm.since("common_data", commonStart)
		}()
	}
	wg.Wait()
	tm.since("fanout_total", fanoutStart)

	// Persona cache write-back: persist freshly-crawled sections (cache miss only)
	// fire-and-forget. Each section is stored under its own key as a single-section
	// PersonaResponse, matching the per-type Python write. Skipped when the section
	// hit the cache (nothing new) or caching is off.
	if cacheOn {
		if resp.PhoneData != nil && !phoneHit {
			sec := resp.PhoneData
			go func() { _ = o.pcache.Put(context.Background(), phoneKey, &model.PersonaResponse{PhoneData: sec}, now) }()
		}
		if resp.EmailData != nil && !emailHit {
			sec := resp.EmailData
			go func() { _ = o.pcache.Put(context.Background(), emailKey, &model.PersonaResponse{EmailData: sec}, now) }()
		}
	}

	// Intelligence (remote ml_service) runs after both sections resolve — it
	// sends the assembled response + request to ml_service and merges the score
	// back into per-section and common intelligence_data, then derives the
	// prediction. Gated on tenant common_intelligence.enabled inside the service.
	if o.intel != nil && yc != nil && yc.IsCommonIntelligenceEnabled() {
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
	c := o.runner.Lookup(crawler.KindPhone, "UPI")
	if uc, ok := c.(*crawler.UPICrawler); ok {
		return uc.Config()
	}
	return nil
}

// resolveConfig parses the tenant youConfig and derives the per-kind crawl
// sets. Returns (nil, nil, nil) when the fetcher/config is unavailable — the
// caller then runs every registered crawler and applies permissive meta gates.
func (o *Orchestrator) resolveConfig(tenant *auth.Tenant) (yc *appconfig.YouConfiguration, phoneSites, emailSites []string) {
	if o.cfg == nil || tenant == nil || tenant.Config == "" {
		return nil, nil, nil
	}
	parsed, err := appconfig.ParseYouConfig(tenant.Config)
	if err != nil {
		return nil, nil, nil
	}
	globalDisabled := appconfig.GlobalDisabled(o.cfg)
	phoneSites = appconfig.CrawlSet("phone", o.runner.Available(crawler.KindPhone), parsed, globalDisabled)
	emailSites = appconfig.CrawlSet("email", o.runner.Available(crawler.KindEmail), parsed, globalDisabled)
	return parsed, phoneSites, emailSites
}

// runCrawlers runs the config-selected sites, or every registered crawler of
// the kind when sites is nil (fallback).
func (o *Orchestrator) runCrawlers(ctx context.Context, kind crawler.Kind, identifier string, sites []string) []crawler.Result {
	if sites == nil {
		return o.runner.Run(ctx, kind, identifier)
	}
	return o.runner.RunSites(ctx, kind, identifier, sites)
}

// buildPhoneSection runs the phone crawlers and phone meta concurrently.
func (o *Orchestrator) buildPhoneSection(ctx context.Context, phone *model.Phone, tm *timings, sites []string, yc *appconfig.YouConfiguration) *model.Section {
	identifier := normalizePhone(phone.CountryCode, phone.Number)

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
	if o.static != nil && phoneStaticNeeded(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			staticStart := time.Now()
			doc, err := o.static.GetInorganic(ctx, staticID)
			tm.since("static_phone", staticStart)
			if err == nil {
				static = doc
			}
		}()
	}

	if o.phoneMeta != nil && phoneMetaOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			// Meta cache (EmailPhoneMeta) read: keyed by the international number.
			// A hit replays the stored PhoneMeta and skips the meta RPCs.
			if o.mcache != nil && yc != nil && yc.IsPhoneInfoCachingEnabled() {
				if raw, hit, err := o.mcache.Get(ctx, identifier, time.Now().Unix()); err == nil && hit {
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
			r := o.phoneMeta.Fetch(ctx, national, identifier, postpaidOn(yc))
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
			if o.mcache != nil && yc != nil && yc.IsPhoneInfoCachingEnabled() {
				pm := phoneMeta
				go func() { _ = o.mcache.Put(context.Background(), identifier, pm, time.Now().Unix()) }()
			}
		}()
	}
	inner.Wait()

	recordCrawlerTimings(tm, results)
	// Section key is the international number (prod: phone_data.primary_data.key
	// = login_id.international_number, e.g. "+917667701982").
	sec := buildSection("phone", identifier, results)
	if phoneMeta != nil {
		sec.PrimaryData.PhoneMeta = phoneMeta
	}
	// Phone breach: computed from static_data (pawn_service.get_breach_details).
	// With no static repo / no match it yields the empty not-found block.
	if o.breach != nil && breachOn(yc) {
		sec.PrimaryData.BreachDetails = o.breach.Phone(identifier, staticID, static)
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
	if o.static != nil && emailStaticNeeded(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			staticStart := time.Now()
			doc, err := o.static.GetInorganic(ctx, email)
			tm.since("static_email", staticStart)
			if err == nil {
				static = doc
			}
		}()
	}

	if o.emailMeta != nil && emailMetaOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			// Meta cache (EmailPhoneMeta) read: keyed by the email address.
			if o.mcache != nil && yc != nil && yc.IsEmailInfoCachingEnabled() {
				if raw, hit, err := o.mcache.Get(ctx, email, time.Now().Unix()); err == nil && hit {
					var em model.EmailMeta
					if json.Unmarshal(raw, &em) == nil {
						emailMeta = &em
						tm.record("meta_cache_email", 0)
						return
					}
				}
			}
			r := o.emailMeta.Fetch(ctx, email)
			tm.since("meta_email", metaStart)
			em := &model.EmailMeta{Email: email, IsDisposable: r.IsDisposable}
			if r.DomainAttributes != nil {
				em.DomainAttributes = domainAttrsToModel(r.DomainAttributes)
			}
			emailMeta = em
			// Write-back fire-and-forget.
			if o.mcache != nil && yc != nil && yc.IsEmailInfoCachingEnabled() {
				m := em
				go func() { _ = o.mcache.Put(context.Background(), email, m, time.Now().Unix()) }()
			}
		}()
	}

	if o.breach != nil && breachOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			breachStart := time.Now()
			breachDet = o.breach.Email(ctx, email)
			tm.since("breach_email", breachStart)
		}()
	}
	inner.Wait()

	recordCrawlerTimings(tm, results)
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

	out := o.intel.Run(ctx, intelligence.Input{
		HasPhone: req.Phone != nil,
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
	}
	if resp.EmailData != nil && len(out.EmailIntel) > 0 {
		resp.EmailData.IntelligenceData = mergeIntelligenceData(resp.EmailData.IntelligenceData, out.EmailIntel)
	}
	if len(out.CommonIntel) > 0 {
		resp.IntelligenceData = mapToIntelligenceData(out.CommonIntel)
	}
	// Prediction: reshaped to {identity_fraud_score: score} or {error:true} in
	// Phase 6 (cleanup_prediction); here we stash the raw outcome, gated by the
	// tenant prediction flag.
	if yc.Prediction {
		if out.PredictionError || out.PredictionScore == nil {
			resp.Prediction = map[string]any{"error": true}
		} else {
			// Placeholder key; cleanup_prediction renames to the tenant's
			// output_key_name (default identity_fraud_score) in Phase 6.
			resp.Prediction = map[string]any{"predicted_score": *out.PredictionScore}
		}
	}
}
