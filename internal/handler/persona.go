// Package handler implements the POST /v1/persona route, the Go equivalent of
// engine/resources/you.py getpersona() + the thin slice of
// get_persona_by_both it needs.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/breach"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/crawler/upi"
	"github.com/sign3labs/go-you/internal/intelligence"
	"github.com/sign3labs/go-you/internal/meta"
	"github.com/sign3labs/go-you/internal/metrics"
	"github.com/sign3labs/go-you/internal/model"
)

const (
	route = "/v1/persona"

	// Default aggregate deadline. Python derives this per tenant from
	// youConfig.request_timeout with a 14s app-config fallback; the POC uses a
	// fixed default and honours a per-request override.
	defaultTimeout = 14 * time.Second

	// Section + top-level status codes, matching utility/error_handler.py.
	sectionStatusSuccess = 2000
	sectionStatusInvalid = 2100 // invalid id (unused for now — inputs are pre-validated)

	statusOK = "SUCCESS"

	statusCodeSuccess          = 2000
	statusCodeInvalidPhone     = 2101
	statusCodeInvalidEmail     = 2102
	statusCodePhoneServerError = 2201
	statusCodeEmailServerError = 2202
	statusCodeMultiFieldError  = 2500

	statusInvalidPhone     = "INVALID_PHONE"
	statusInvalidEmail     = "INVALID_EMAIL"
	statusPhoneServerError = "PHONE_SERVER_ERROR"
	statusEmailServerError = "EMAIL_SERVER_ERROR"
	statusMultiFieldError  = "MULTI_FIELD_ERROR"
)

type Persona struct {
	runner    *crawler.Runner
	phoneMeta *meta.PhoneMetaService
	emailMeta *meta.EmailMetaService
	breach    *breach.Service
	intel     *intelligence.Service
	// cfg is the ConfigFetcher (per-tenant youConfig gates, global settings).
	// nil in LOCAL_DEV where MySQL — and therefore the configs table — is absent.
	cfg *appconfig.Fetcher
}

func NewPersona(runner *crawler.Runner, phoneMeta *meta.PhoneMetaService, emailMeta *meta.EmailMetaService, breachSvc *breach.Service, intel *intelligence.Service, cfg *appconfig.Fetcher) *Persona {
	return &Persona{runner: runner, phoneMeta: phoneMeta, emailMeta: emailMeta, breach: breachSvc, intel: intel, cfg: cfg}
}

// breachOn reports whether the breach lane runs (tenant breach flag; nil => on).
func breachOn(yc *appconfig.YouConfiguration) bool { return yc == nil || yc.Breach }

// upiConfig returns the registered UPI crawler's parsed config (for the
// transform's CLIENT_RESPONSE / verified-names handling), or nil when UPI is not
// registered (LOCAL_DEV).
func (h *Persona) upiConfig() *upi.Config {
	c := h.runner.Lookup(crawler.KindPhone, "UPI")
	if uc, ok := c.(*crawler.UPICrawler); ok {
		return uc.Config()
	}
	return nil
}

func (h *Persona) Handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := uuid.NewString()

	tenant, ok := auth.FromContext(r.Context())
	if !ok {
		// Middleware guarantees this, but stay defensive.
		writeError(w, http.StatusUnauthorized, requestID, "Unauthorized")
		return
	}
	tenantID := tenant.ID

	var status = http.StatusOK
	defer func() {
		metrics.APILatency.WithLabelValues(route, tenantID).Observe(time.Since(start).Seconds())
		metrics.APIStatus.WithLabelValues(route, tenantID, strconv.Itoa(status)).Inc()
	}()

	tm := newTimings()

	decodeStart := time.Now()
	var req model.PersonaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		status = http.StatusBadRequest
		writeError(w, status, requestID, "Invalid request body")
		return
	}
	tm.since("decode", decodeStart)
	if req.Phone == nil && req.Email == "" {
		status = http.StatusBadRequest
		writeError(w, status, requestID, "phone or email required")
		return
	}

	// Establish the aggregate deadline (leaf-only timeout model: this context
	// deadline is the single bound; crawlers and meta respect it, nothing above
	// them imposes its own).
	timeout := defaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout)*time.Second + time.Second // +1s buffer, as Python
	}
	ctx, cancel := contextWithTimeout(r.Context(), timeout)
	defer cancel()

	resp := model.PersonaResponse{RequestID: requestID}

	// Resolve the tenant's youConfig once: it drives the per-kind crawl sets AND
	// the meta feature gates (phone_meta/email_meta/postpaid). On any failure
	// (LOCAL_DEV fake tenant, missing/invalid config, no fetcher) yc is nil and
	// the crawl sets are nil (run every registered crawler) — meta then runs
	// with permissive defaults so the service still works without a configs table.
	yc, phoneSites, emailSites := h.resolveConfig(tenant)

	// Phone branch and email branch run concurrently; within each, the crawler
	// fan-out and the meta lookup run concurrently too — matching Python's
	// per-branch parallel sub-tasks.
	fanoutStart := time.Now()
	var wg sync.WaitGroup
	if req.Phone != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.PhoneData = h.buildPhoneSection(ctx, req.Phone, tm, phoneSites, yc)
		}()
	}
	if req.Email != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.EmailData = h.buildEmailSection(ctx, req.Email, tm, emailSites, yc)
		}()
	}
	wg.Wait()
	tm.since("fanout_total", fanoutStart)

	// Intelligence (remote ml_service) runs after both sections resolve — it
	// sends the assembled response + request to ml_service and merges the score
	// back into per-section and common intelligence_data, then derives the
	// prediction. Gated on tenant common_intelligence.enabled inside the service.
	if h.intel != nil && yc != nil && yc.IsCommonIntelligenceEnabled() {
		intelStart := time.Now()
		h.applyIntelligence(ctx, &req, &resp, yc)
		tm.since("intelligence", intelStart)
	}

	// Top-level status from the section status codes (compute_top_level_status).
	resp.StatusCode, resp.Status = computeTopLevelStatus(&resp)

	tm.since("total", start)
	resp.Timings = tm.asMap()

	// Transform into the final client shape: account_details as a keyed map,
	// client_response:false sites dropped, social_profile_count recomputed,
	// prediction reshaped, meta stripped unless ?meta is absent-but-present.
	// (See transform.go for the full rule set.)
	metaParam := r.URL.Query().Has("meta")
	ut := resolveUPITransform(h.upiConfig(), yc)
	out := transformResponse(&resp, yc, metaParam, ut)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server-Timing", tm.serverTimingHeader())
	w.Header().Set("you_time", fmt.Sprintf("%f", time.Since(start).Seconds()))
	_ = json.NewEncoder(w).Encode(out)
}

// resolveConfig parses the tenant youConfig and derives the per-kind crawl
// sets. Returns (nil, nil, nil) when the fetcher/config is unavailable — the
// caller then runs every registered crawler and applies permissive meta gates.
func (h *Persona) resolveConfig(tenant *auth.Tenant) (yc *appconfig.YouConfiguration, phoneSites, emailSites []string) {
	if h.cfg == nil || tenant == nil || tenant.Config == "" {
		return nil, nil, nil
	}
	parsed, err := appconfig.ParseYouConfig(tenant.Config)
	if err != nil {
		return nil, nil, nil
	}
	globalDisabled := appconfig.GlobalDisabled(h.cfg)
	phoneSites = appconfig.CrawlSet("phone", h.runner.Available(crawler.KindPhone), parsed, globalDisabled)
	emailSites = appconfig.CrawlSet("email", h.runner.Available(crawler.KindEmail), parsed, globalDisabled)
	return parsed, phoneSites, emailSites
}

// phoneMetaOn reports whether the phone_meta lane should run for this tenant.
//
// Python fetches phone_meta UNCONDITIONALLY: YouServicePhone.get_user_persona
// always submits get_phone_intelligence and map_response always attaches
// phone_meta (you_service_phone.py:32,82). The top-level youConfig "phone_meta"
// flag only drives analytics events (request_context_service.py:18) and is not
// even present in the root config, so it must never suppress the meta output.
// The real per-feature gates live deeper (postpaid via is_postpaid_enabled). So
// phone_meta always runs here.
func phoneMetaOn(yc *appconfig.YouConfiguration) bool { return true }

// emailMetaOn reports whether the email_meta lane runs.
//
// Python also fetches email_meta UNCONDITIONALLY (you_service_email.py:92 always
// submits get_email_intelligence). Inside that lane, the domain-intelligence
// fetch — WHOIS / is_disposable / SPF-DMARC-MX / website, which is ALL that
// go-you's EmailMetaService produces — is the part gated by
// is_domain_intelligence_enabled (email_info_service.py:46). So go-you runs the
// lane exactly when domain intelligence is enabled; the (nonexistent) top-level
// email_meta flag is NOT part of the gate. nil youConfig (LOCAL_DEV) => on.
func emailMetaOn(yc *appconfig.YouConfiguration) bool {
	if yc == nil {
		return true
	}
	return yc.IsDomainIntelligenceEnabled()
}

// postpaidOn reports whether the postpaid sub-lane runs (nil => on).
func postpaidOn(yc *appconfig.YouConfiguration) bool { return yc == nil || yc.IsPostpaidEnabled() }

// runCrawlers runs the config-selected sites, or every registered crawler of
// the kind when sites is nil (fallback).
func (h *Persona) runCrawlers(ctx context.Context, kind crawler.Kind, identifier string, sites []string) []crawler.Result {
	if sites == nil {
		return h.runner.Run(ctx, kind, identifier)
	}
	return h.runner.RunSites(ctx, kind, identifier, sites)
}

// buildPhoneSection runs the phone crawlers and phone meta concurrently.
func (h *Persona) buildPhoneSection(ctx context.Context, phone *model.Phone, tm *timings, sites []string, yc *appconfig.YouConfiguration) *model.Section {
	identifier := normalizePhone(phone.CountryCode, phone.Number)

	var (
		results   []crawler.Result
		phoneMeta *model.PhoneMeta
		inner     sync.WaitGroup
	)
	inner.Add(1)
	go func() { defer inner.Done(); results = h.runCrawlers(ctx, crawler.KindPhone, identifier, sites) }()

	if h.phoneMeta != nil && phoneMetaOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			national := nationalFromIdentifier(identifier)
			r := h.phoneMeta.Fetch(ctx, national, identifier, postpaidOn(yc))
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
	// Phone breach is deterministic-empty (no static-data source); attach it
	// synchronously when the tenant enables breach.
	if h.breach != nil && breachOn(yc) {
		sec.PrimaryData.BreachDetails = h.breach.Phone(identifier)
	}
	return sec
}

// buildEmailSection runs the email crawlers and email meta concurrently.
func (h *Persona) buildEmailSection(ctx context.Context, email string, tm *timings, sites []string, yc *appconfig.YouConfiguration) *model.Section {
	var (
		results   []crawler.Result
		emailMeta *model.EmailMeta
		breachDet *model.BreachDetails
		inner     sync.WaitGroup
	)
	inner.Add(1)
	go func() { defer inner.Done(); results = h.runCrawlers(ctx, crawler.KindEmail, email, sites) }()

	if h.emailMeta != nil && emailMetaOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			r := h.emailMeta.Fetch(ctx, email)
			tm.since("meta_email", metaStart)
			em := &model.EmailMeta{Email: email, IsDisposable: r.IsDisposable}
			if r.DomainAttributes != nil {
				em.DomainAttributes = domainAttrsToModel(r.DomainAttributes)
			}
			emailMeta = em
		}()
	}

	if h.breach != nil && breachOn(yc) {
		inner.Add(1)
		go func() {
			defer inner.Done()
			breachStart := time.Now()
			breachDet = h.breach.Email(ctx, email)
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
	return sec
}

// domainAttrsToModel maps the meta service's loose domain_attributes map into
// the typed model.DomainAttributes. The error-only shape ({domain, error:true})
// is preserved.
func domainAttrsToModel(m map[string]any) *model.DomainAttributes {
	da := &model.DomainAttributes{}
	da.Domain, _ = m["domain"].(string)
	if errv, ok := m["error"].(bool); ok && errv {
		e := true
		da.Error = &e
		return da
	}
	da.CreatedAt, _ = m["created_at"].(string)
	da.UpdatedAt, _ = m["updated_at"].(string)
	da.ExpiresAt, _ = m["expires_at"].(string)
	da.TLD, _ = m["tld"].(string)
	da.Registered, _ = m["registered"].(string)
	da.RegistrarName, _ = m["registrar_name"].(string)
	da.RegisteredTo, _ = m["registered_to"].(string)
	da.DmarcEnforced, _ = m["dmarc_enforced"].(string)
	da.SpfStrict, _ = m["spf_strict"].(string)
	da.ValidMX, _ = m["valid _mx"].(string)
	if we, ok := m["website_exists"].(bool); ok {
		da.WebsiteExists = &we
	}
	return da
}

// recordCrawlerTimings logs each crawler's measured duration under crawl_<SITE>
// for the per-request timing view, and also feeds the per-crawler Prometheus
// histogram so Grafana can show p50/p95/p99 latency per crawler over time.
func recordCrawlerTimings(tm *timings, results []crawler.Result) {
	for _, res := range results {
		tm.record("crawl_"+res.Website, res.Duration)
		metrics.CrawlerLatency.
			WithLabelValues(res.Website, string(res.Kind), crawlerStatus(res)).
			Observe(res.Duration.Seconds())
	}
}

// crawlerStatus maps a crawler Result to the histogram's status label. The
// runner reports the shared-deadline case with the "timed out" sentinel; any
// other error is a crawler-specific failure.
func crawlerStatus(res crawler.Result) string {
	if res.Err == nil {
		return "ok"
	}
	if res.Err.Error() == "timed out" {
		return "timeout"
	}
	return "failed"
}

// normalizePhone returns the international form "+<cc><number>" the phone
// spiders expect, tolerating inputs that already carry a leading '+'.
func normalizePhone(countryCode, number string) string {
	cc := strings.TrimPrefix(strings.TrimSpace(countryCode), "+")
	num := strings.TrimSpace(number)
	// If the number already starts with '+', assume it's fully qualified.
	if strings.HasPrefix(num, "+") {
		return num
	}
	// If the number already starts with the country code, don't double it.
	if cc != "" && strings.HasPrefix(num, cc) {
		return "+" + num
	}
	return "+" + cc + num
}

// applyIntelligence runs the ml_service merge and attaches intelligence_data to
// the per-section and top-level response, plus the raw prediction (reshaped
// later by the transform's cleanup_prediction). The you_request/you_response
// payloads are the response marshalled to maps and null-stripped, matching the
// Python payload construction.
func (h *Persona) applyIntelligence(ctx context.Context, req *model.PersonaRequest, resp *model.PersonaResponse, yc *appconfig.YouConfiguration) {
	youResponse := toStrippedMap(resp)
	youRequest := toStrippedMap(req)

	out := h.intel.Run(ctx, intelligence.Input{
		HasPhone:           req.Phone != nil,
		HasEmail:           req.Email != "",
		Tenant:             "", // filled by the caller's tenant id if needed; ml payload tolerates ""
		CommonIntelligence: yc.CommonIntelligence,
		YouRequest:         youRequest,
		YouResponse:        youResponse,
	})

	if resp.PhoneData != nil && len(out.PhoneIntel) > 0 {
		resp.PhoneData.IntelligenceData = mapToIntelligenceData(out.PhoneIntel)
	}
	if resp.EmailData != nil && len(out.EmailIntel) > 0 {
		resp.EmailData.IntelligenceData = mapToIntelligenceData(out.EmailIntel)
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

// toStrippedMap marshals v to JSON then back to a map, dropping nil values
// recursively (Python clean_empty: removes None, keeps false/0/""/[]/{}). Used
// to build the ml_service YOU payload so it matches the Python serialization.
func toStrippedMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return cleanEmpty(m).(map[string]any)
}

// cleanEmpty recursively removes nil map values and nil list elements, matching
// clean_empty (service/you_service_aggregator.py:182). Non-nil zero values
// (false, 0, "", empty containers) are kept.
func cleanEmpty(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = cleanEmpty(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			if e == nil {
				continue
			}
			out = append(out, cleanEmpty(e))
		}
		return out
	default:
		return v
	}
}

// mapToIntelligenceData wraps a merged intelligence map into the model type.
// The Score sub-map is lifted into the typed Score field; other keys are folded
// into a generic holder via the same map (kept loose because ml_service writes
// arbitrary shapes).
func mapToIntelligenceData(m map[string]any) *model.IntelligenceData {
	id := &model.IntelligenceData{}
	if score, ok := m["score"].(map[string]any); ok {
		id.Score = score
	}
	if bvn, ok := m["bank_verified_name"].(string); ok {
		id.BankVerifiedName = bvn
	}
	if da, ok := m["digital_age"].(map[string]any); ok {
		id.DigitalAge = da
	}
	return id
}

// nationalFromIdentifier strips the leading "+91" from a normalized identifier
// to the 10-digit national number the phone-meta vendors expect.
func nationalFromIdentifier(identifier string) string {
	s := strings.TrimPrefix(identifier, "+")
	if strings.HasPrefix(s, "91") && len(s) == 12 {
		return s[2:]
	}
	return s
}

// buildSection assembles a phone_data/email_data block from crawler results and
// derives a section status: all-failed, partial, or ok.
func buildSection(kind, key string, results []crawler.Result) *model.Section {
	accounts := make([]model.AccountDetails, 0, len(results))
	profileCount := 0
	failures := 0
	for _, res := range results {
		ad := model.AccountDetails{Website: res.Website}
		if res.Err != nil {
			ad.ErrorMsg = res.Err.Error()
			failures++
		} else {
			ad.UserExist = res.UserExist
			ad.Data = res.Data // rich per-site fields (DetailCrawler), if any
			if res.UserExist != nil && *res.UserExist {
				profileCount++
			}
		}
		accounts = append(accounts, ad)
	}

	// A section for a valid identifier is SUCCESS (2000) even if some/all
	// crawlers failed — per-crawler failures are carried in account_details and
	// are NOT a section-level failure in prod (compute_top_level_status only
	// escalates on invalid-id 2100 or server-error 2200). _ = failures keeps the
	// count available if a future rule needs it.
	_ = failures
	return &model.Section{
		Key:  key,
		Type: kind,
		PrimaryData: &model.PrimaryData{
			AccountDetails:     accounts,
			SocialProfileCount: profileCount,
		},
		StatusCode: sectionStatusSuccess,
		Status:     statusOK,
	}
}

func writeError(w http.ResponseWriter, status int, requestID, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.ErrorResponse{RequestID: requestID, ErrorMsg: msg})
}
