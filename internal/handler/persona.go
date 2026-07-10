// Package handler implements the POST /v1/persona route, the Go equivalent of
// engine/resources/you.py getpersona() + the thin slice of
// get_persona_by_both it needs.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/metrics"
	"github.com/sign3labs/go-you/internal/model"
)

const (
	route = "/v1/persona"

	// Default aggregate deadline. Python derives this per tenant from
	// youConfig.request_timeout with a 14s app-config fallback; the POC uses a
	// fixed default and honours a per-request override.
	defaultTimeout = 14 * time.Second

	statusOK       = "SUCCESS"
	statusServeErr = "SERVER_ERROR"
)

type Persona struct {
	runner *crawler.Runner
}

func NewPersona(runner *crawler.Runner) *Persona {
	return &Persona{runner: runner}
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

	var status int = http.StatusOK
	defer func() {
		metrics.APILatency.WithLabelValues(route, tenantID).Observe(time.Since(start).Seconds())
		metrics.APIStatus.WithLabelValues(route, tenantID, strconv.Itoa(status)).Inc()
	}()

	var req model.PersonaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		status = http.StatusBadRequest
		writeError(w, status, requestID, "Invalid request body")
		return
	}
	if req.Phone == nil && req.Email == "" {
		status = http.StatusBadRequest
		writeError(w, status, requestID, "phone or email required")
		return
	}

	// Establish the aggregate deadline (leaf-only timeout model: this context
	// deadline is the single bound; crawlers respect it, nothing above them
	// imposes its own).
	timeout := defaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout)*time.Second + time.Second // +1s buffer, as Python
	}
	ctx, cancel := contextWithTimeout(r.Context(), timeout)
	defer cancel()

	resp := model.PersonaResponse{RequestID: requestID}

	// Phone fan-out (Flipkart, Instagram).
	if req.Phone != nil {
		// Flipkart (and the other phone spiders) use the Python
		// PhoneNumber.international_number form: "+<cc><number>".
		identifier := "+" + req.Phone.CountryCode + req.Phone.Number
		results := h.runner.Run(ctx, crawler.KindPhone, identifier)
		resp.PhoneData = buildSection("phone", req.Phone.Number, results)
	}

	// Email fan-out (Spotify, Freelancer).
	if req.Email != "" {
		results := h.runner.Run(ctx, crawler.KindEmail, req.Email)
		resp.EmailData = buildSection("email", req.Email, results)
	}

	resp.StatusCode = 200
	resp.Status = statusOK

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("you_time", fmt.Sprintf("%f", time.Since(start).Seconds()))
	_ = json.NewEncoder(w).Encode(resp)
}

// buildSection assembles a phone_data/email_data block from crawler results.
func buildSection(kind, key string, results []crawler.Result) *model.Section {
	accounts := make([]model.AccountDetails, 0, len(results))
	profileCount := 0
	for _, res := range results {
		ad := model.AccountDetails{Website: res.Website}
		if res.Err != nil {
			ad.ErrorMsg = res.Err.Error()
		} else {
			ad.UserExist = res.UserExist
			if res.UserExist != nil && *res.UserExist {
				profileCount++
			}
		}
		accounts = append(accounts, ad)
	}
	return &model.Section{
		Key:  key,
		Type: kind,
		PrimaryData: &model.PrimaryData{
			AccountDetails:     accounts,
			SocialProfileCount: profileCount,
		},
		StatusCode: 200,
		Status:     statusOK,
	}
}

func writeError(w http.ResponseWriter, status int, requestID, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.ErrorResponse{RequestID: requestID, ErrorMsg: msg})
}
