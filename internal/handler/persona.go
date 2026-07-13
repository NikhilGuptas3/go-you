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
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/crawler"
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

	// Section status values, mirroring the spirit of the Python section states.
	statusOK        = "SUCCESS"        // at least one crawler returned a verdict
	statusPartial   = "PARTIAL"        // some crawlers returned, some failed
	statusAllFailed = "SECTION_FAILED" // every crawler in the section failed
	statusServeErr  = "SERVER_ERROR"
)

type Persona struct {
	runner *crawler.Runner
	meta   *meta.Client
}

func NewPersona(runner *crawler.Runner, metaClient *meta.Client) *Persona {
	return &Persona{runner: runner, meta: metaClient}
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

	// Phone branch and email branch run concurrently; within each, the crawler
	// fan-out and the meta lookup run concurrently too — matching Python's
	// per-branch parallel sub-tasks.
	fanoutStart := time.Now()
	var wg sync.WaitGroup
	if req.Phone != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.PhoneData = h.buildPhoneSection(ctx, req.Phone, tm)
		}()
	}
	if req.Email != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.EmailData = h.buildEmailSection(ctx, req.Email, tm)
		}()
	}
	wg.Wait()
	tm.since("fanout_total", fanoutStart)

	resp.StatusCode = 200
	resp.Status = statusOK

	tm.since("total", start)
	resp.Timings = tm.asMap()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server-Timing", tm.serverTimingHeader())
	w.Header().Set("you_time", fmt.Sprintf("%f", time.Since(start).Seconds()))
	_ = json.NewEncoder(w).Encode(resp)
}

// buildPhoneSection runs the phone crawlers and phone meta concurrently.
func (h *Persona) buildPhoneSection(ctx context.Context, phone *model.Phone, tm *timings) *model.Section {
	identifier := normalizePhone(phone.CountryCode, phone.Number)

	var (
		results   []crawler.Result
		phoneMeta *meta.PhoneMeta
		inner     sync.WaitGroup
	)
	inner.Add(1)
	go func() { defer inner.Done(); results = h.runner.Run(ctx, crawler.KindPhone, identifier) }()

	if h.meta.Enabled() {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			m, err := h.meta.FetchPhone(ctx, identifier)
			tm.since("meta_phone", metaStart)
			if err == nil {
				phoneMeta = m
			}
		}()
	}
	inner.Wait()

	recordCrawlerTimings(tm, results)
	sec := buildSection("phone", phone.Number, results)
	if phoneMeta != nil {
		sec.PrimaryData.PhoneMeta = phoneMeta
	}
	return sec
}

// buildEmailSection runs the email crawlers and email meta concurrently.
func (h *Persona) buildEmailSection(ctx context.Context, email string, tm *timings) *model.Section {
	var (
		results   []crawler.Result
		emailMeta *meta.EmailMeta
		inner     sync.WaitGroup
	)
	inner.Add(1)
	go func() { defer inner.Done(); results = h.runner.Run(ctx, crawler.KindEmail, email) }()

	if h.meta.Enabled() {
		inner.Add(1)
		go func() {
			defer inner.Done()
			metaStart := time.Now()
			m, err := h.meta.FetchEmail(ctx, email)
			tm.since("meta_email", metaStart)
			if err == nil {
				emailMeta = m
			}
		}()
	}
	inner.Wait()

	recordCrawlerTimings(tm, results)
	sec := buildSection("email", email, results)
	if emailMeta != nil {
		sec.PrimaryData.EmailMeta = emailMeta
	}
	return sec
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
			if res.UserExist != nil && *res.UserExist {
				profileCount++
			}
		}
		accounts = append(accounts, ad)
	}

	status := statusOK
	switch {
	case len(results) > 0 && failures == len(results):
		status = statusAllFailed
	case failures > 0:
		status = statusPartial
	}

	return &model.Section{
		Key:  key,
		Type: kind,
		PrimaryData: &model.PrimaryData{
			AccountDetails:     accounts,
			SocialProfileCount: profileCount,
		},
		StatusCode: 200,
		Status:     status,
	}
}

func writeError(w http.ResponseWriter, status int, requestID, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.ErrorResponse{RequestID: requestID, ErrorMsg: msg})
}
