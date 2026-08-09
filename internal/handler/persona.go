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
	"time"

	"github.com/google/uuid"
	"github.com/sign3labs/go-you/internal/analytics"
	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/logger"
	"github.com/sign3labs/go-you/internal/metrics"
	"github.com/sign3labs/go-you/internal/model"
	"github.com/sign3labs/go-you/internal/staticdata"
)

// personaLog is the component logger for the persona transport layer, so lines
// render as "persona:<func> - …" (the module:func column from hey-you's format).
var personaLog = logger.Component("persona")

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

// Persona is the HTTP transport layer for POST /v1/persona. It owns the
// application service (Orchestrator) and the analytics sink, and does only
// transport work: decode → validate → deadline → Orchestrator.Build → transform
// → encode → fire-and-forget analytics. All fan-out/business logic lives in the
// Orchestrator (orchestrator.go).
type Persona struct {
	orch *Orchestrator
	// sink is the Kinesis analytics sink (fire-and-forget, one event per request).
	// nil => analytics off. It stays on the transport layer because the event is
	// assembled from the *http.Request + the final client response.
	sink *analytics.Sink
}

// NewPersona wires the transport handler from a Deps struct. It builds the
// Orchestrator from the lane deps and holds the sink for the transport layer
// (the analytics event is assembled from the *http.Request + client response).
func NewPersona(deps Deps) *Persona {
	return &Persona{
		orch: NewOrchestrator(deps),
		sink: deps.Sink,
	}
}

// breachOn reports whether the breach lane runs (tenant breach flag; nil => on).
func breachOn(yc *appconfig.YouConfiguration) bool { return yc == nil || yc.Breach }

func (h *Persona) Handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// Reuse the request id the middleware stashed in context (which reconciles an
	// inbound X-Request-Id with a freshly minted uuid); fall back to a new one if
	// the handler is somehow reached without the middleware.
	requestID := logger.RequestIDFromContext(r.Context())
	if requestID == "" {
		requestID = uuid.NewString()
	}

	tenant, ok := auth.FromContext(r.Context())
	if !ok {
		// Middleware guarantees this, but stay defensive.
		personaLog.Warn("unauthorized", "rid", requestID)
		writeError(w, http.StatusUnauthorized, requestID, "Unauthorized")
		return
	}
	tenantID := tenant.ID

	// Declared before the deferred logger so the closure can report id presence.
	var req model.PersonaRequest
	var status = http.StatusOK
	defer func() {
		took := time.Since(start)
		metrics.APILatency.WithLabelValues(route, tenantID).Observe(took.Seconds())
		metrics.APIStatus.WithLabelValues(route, tenantID, strconv.Itoa(status)).Inc()
		// One structured line per request — the handler has the richest facts
		// (tenant, id presence, timing) that the access-log middleware lacks.
		personaLog.Info("persona handled",
			"rid", requestID, "tenant", tenantID, "status", status,
			"took", roundTo(took.Seconds(), 3),
			"phone", req.Phone != nil, "email", req.Email != "")
	}()

	tm := newTimings()

	decodeStart := time.Now()
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		status = http.StatusBadRequest
		personaLog.Warn("bad request body", "rid", requestID, "tenant", tenantID, "err", err.Error())
		writeError(w, status, requestID, "Invalid request body")
		return
	}
	tm.since("decode", decodeStart)
	if req.Phone == nil && req.Email == "" {
		status = http.StatusBadRequest
		personaLog.Warn("missing identifier", "rid", requestID, "tenant", tenantID, "reason", "phone or email required")
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

	// Orchestration: the application service assembles the full persona response
	// (cache → fan-out → intelligence → status). Transport-agnostic; returns the
	// pre-transform response and the resolved youConfig this layer needs.
	resp, yc := h.orch.Build(ctx, &req, tenant, requestID, tm)

	tm.since("total", start)
	resp.Timings = tm.asMap()

	// Transform into the final client shape: account_details as a keyed map,
	// client_response:false sites dropped, social_profile_count recomputed,
	// prediction reshaped, meta stripped unless ?meta is absent-but-present.
	// (See transform.go for the full rule set.)
	metaParam := r.URL.Query().Has("meta")
	ut := resolveUPITransform(h.orch.upiConfig(), yc)
	// Build the ml-payload-shaped response BEFORE transform for the analytics
	// analytic_response field (the closest analogue to Python's filtered response),
	// while transformResponse produces the client `out`.
	analyticResp := toStrippedMap(resp)
	out := transformResponse(resp, yc, metaParam, ut)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server-Timing", tm.serverTimingHeader())
	w.Header().Set("you_time", fmt.Sprintf("%f", time.Since(start).Seconds()))
	_ = json.NewEncoder(w).Encode(out)

	// Analytics (Kinesis): fire-and-forget after the response is flushed. The sink
	// swallows all errors, so this can never affect the client. nil sink => no-op.
	if h.sink != nil {
		apiTime := time.Since(start).Seconds()
		go h.sink.Push(context.Background(), buildAnalyticsEvent(r, requestID, tenantID, &req, out, analyticResp, apiTime))
	}
}

// phoneMetaOn reports whether the phone_meta lane should run for this tenant.
//
// The gate is config_service.is_phone_info_enabled: the CachedPhoneInfoService
// returns None when phone_info.enabled is explicitly false
// (cached_phone_info_service.py:73), so phone_number_details is None and
// clean_empty drops the whole phone_meta key from the response. When the
// phone_info block is absent (or has no "enabled" key) it defaults to enabled,
// matching prod. The top-level youConfig "phone_meta" flag is analytics-only and
// is NOT this gate. nil youConfig (LOCAL_DEV) => on.
func phoneMetaOn(yc *appconfig.YouConfiguration) bool {
	return yc == nil || yc.IsPhoneInfoEnabled()
}

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

// phoneStaticNeeded reports whether any phone signal that consumes static_data is
// enabled — breach OR digital_age OR linked_ids OR the ml_service lane (which now
// receives static_data in its payload) — so we skip the DB round-trip when none
// are.
func phoneStaticNeeded(yc *appconfig.YouConfiguration) bool {
	if breachOn(yc) {
		return true
	}
	if yc == nil {
		return false
	}
	return yc.IntelligenceBool("digital_age") || yc.IntelligenceBool("linked_ids") ||
		yc.IsCommonIntelligenceEnabled()
}

// phoneStaticIntelligence builds the section intelligence_data derived from
// static_data (digital_age from static dates + revocations; linked_ids from
// primary_email), each gated by its youConfig.intelligence flag. Returns nil when
// nothing is enabled/derived so we don't attach an empty block.
func phoneStaticIntelligence(yc *appconfig.YouConfiguration, static map[string]any, phoneMeta *model.PhoneMeta) *model.IntelligenceData {
	if yc == nil {
		return nil
	}
	id := &model.IntelligenceData{}
	set := false
	if yc.IntelligenceBool("digital_age") {
		var revocations map[string]any
		if phoneMeta != nil {
			revocations = phoneMeta.Revocations
		}
		id.DigitalAge = staticdata.PhoneDigitalAge(static, revocations, time.Now()).Map()
		set = true
	}
	if yc.IntelligenceBool("linked_ids") {
		if ids := staticdata.LinkedIDs(static, staticdata.KindPhone); len(ids) > 0 {
			id.LinkedIDs = ids
			set = true
		}
	}
	if !set {
		return nil
	}
	return id
}

// emailStaticNeeded reports whether the email branch needs static_data: linked_ids
// consumes it (email digital_age uses breach dates, not static), and the ml_service
// lane now receives static_data in its payload.
func emailStaticNeeded(yc *appconfig.YouConfiguration) bool {
	if yc == nil {
		return false
	}
	return yc.IntelligenceBool("linked_ids") || yc.IsCommonIntelligenceEnabled()
}

// emailStaticIntelligence builds the email section intelligence_data: digital_age
// from the verified HIBP breach dates and linked_ids from static primary_ph, each
// gated. Returns nil when nothing is enabled/derived.
func emailStaticIntelligence(yc *appconfig.YouConfiguration, static map[string]any, breachDet *model.BreachDetails) *model.IntelligenceData {
	if yc == nil {
		return nil
	}
	id := &model.IntelligenceData{}
	set := false
	if yc.IntelligenceBool("digital_age") {
		id.DigitalAge = staticdata.EmailDigitalAge(breachesToMaps(breachDet), time.Now()).Map()
		set = true
	}
	if yc.IntelligenceBool("linked_ids") {
		if ids := staticdata.LinkedIDs(static, staticdata.KindEmail); len(ids) > 0 {
			id.LinkedIDs = ids
			set = true
		}
	}
	if !set {
		return nil
	}
	return id
}

// breachesToMaps converts the typed breach list into the {IsVerified, date} maps
// EmailDigitalAge expects (it reads breach_details.breaches from a loose doc).
func breachesToMaps(bd *model.BreachDetails) []map[string]any {
	if bd == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(bd.Breaches))
	for _, b := range bd.Breaches {
		out = append(out, map[string]any{"IsVerified": b.IsVerified, "date": b.Date})
	}
	return out
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

// recordCrawlerTimings records each crawler's measured duration under
// crawl_<SITE> for the per-request timing view, and feeds the per-crawler
// Prometheus metrics (website_latency / website_status / user_exist /
// spider_error) so Grafana can show latency and success/error rates per crawler
// over time. A failing crawler also gets one WARN log line, mirroring hey-you's
// per-crawl logger.exception in crawler_service.py. rid correlates the log with
// the request.
func recordCrawlerTimings(ctx context.Context, tm *timings, results []crawler.Result) {
	rid := logger.RequestIDFromContext(ctx)
	for _, res := range results {
		tm.record("crawl_"+res.Website, res.Duration)
		status := crawlerStatus(res)
		kind := string(res.Kind)
		metrics.WebsiteLatency.WithLabelValues(res.Website, kind, status).Observe(res.Duration.Seconds())
		metrics.WebsiteStatus.WithLabelValues(res.Website, status, kind).Inc()

		if res.Err != nil {
			metrics.SpiderError.WithLabelValues(res.Website, metrics.ErrorClass(status, res.Err.Error())).Inc()
			personaLog.Warn("crawl failed",
				"website", res.Website, "type", kind, "status", status,
				"rid", rid, "took_ms", res.Duration.Milliseconds(), "err", res.Err.Error())
			continue
		}
		// Only count a user_exist verdict when the crawler actually returned one.
		if res.UserExist != nil {
			metrics.UserExist.WithLabelValues(res.Website, strconv.FormatBool(*res.UserExist)).Inc()
		}
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
	if vns, ok := m["verified_names_status"].(string); ok {
		id.VerifiedNamesStatus = vns
	}
	if da, ok := m["digital_age"].(map[string]any); ok {
		id.DigitalAge = da
	}
	id.AssociatedNames = toStringList(m["associated_names"])
	id.NonVerifiedNames = toStringList(m["non_verified_names"])
	id.LinkedIDs = toStringList(m["linked_ids"])
	return id
}

// toStringList coerces a JSON-decoded value into []string (nil when not a list of
// strings), tolerating the []any-of-strings shape from a JSON round-trip.
func toStringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// mergeIntelligenceData layers the ml_service output map onto the section
// intelligence_data already derived from static_data (digital_age, linked_ids),
// without clobbering the static signals. base may be nil (no static derivation).
//
// Precedence rules:
//   - Score / bank_verified_name / verified_names* / associated_names / non_verified_names
//     come from ml_service (static does not produce them).
//   - digital_age: keep the static value UNLESS static is absent/errored, in which
//     case fall back to ml_service's (which is typically {error:true} in go-you).
//   - linked_ids: keep the static value when present; else take ml_service's.
func mergeIntelligenceData(base *model.IntelligenceData, m map[string]any) *model.IntelligenceData {
	ml := mapToIntelligenceData(m)
	if base == nil {
		return ml
	}
	// ml_service-owned fields overwrite (base never sets these).
	base.Score = ml.Score
	if ml.BankVerifiedName != "" {
		base.BankVerifiedName = ml.BankVerifiedName
	}
	if ml.VerifiedNamesStatus != "" {
		base.VerifiedNamesStatus = ml.VerifiedNamesStatus
	}
	if len(ml.AssociatedNames) > 0 {
		base.AssociatedNames = ml.AssociatedNames
	}
	if len(ml.NonVerifiedNames) > 0 {
		base.NonVerifiedNames = ml.NonVerifiedNames
	}
	// digital_age: prefer the static-derived value; only take ml_service's when
	// static produced nothing usable (nil or {error:true}).
	if isUsableDigitalAge(base.DigitalAge) {
		// keep base
	} else if ml.DigitalAge != nil {
		base.DigitalAge = ml.DigitalAge
	}
	// linked_ids: static wins when present.
	if len(base.LinkedIDs) == 0 && len(ml.LinkedIDs) > 0 {
		base.LinkedIDs = ml.LinkedIDs
	}
	return base
}

// isUsableDigitalAge reports whether a digital_age map carries a real value
// (has a "year") rather than being nil or an {error:true} placeholder.
func isUsableDigitalAge(da map[string]any) bool {
	if da == nil {
		return false
	}
	if e, ok := da["error"].(bool); ok && e {
		return false
	}
	_, hasYear := da["year"]
	return hasYear
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
