package handler

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sign3labs/go-you/internal/analytics"
	"github.com/sign3labs/go-you/internal/model"
)

// This file holds the transport-layer helpers for the persona route: response
// error writing, request-header/IP extraction, and the analytics-event assembly.
// They are pure functions of the *http.Request / response and carry no
// orchestration or business logic — kept out of persona.go so the handler reads
// as decode → orchestrate → encode.

// writeError encodes a model.ErrorResponse with the given HTTP status.
func writeError(w http.ResponseWriter, status int, requestID, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.ErrorResponse{RequestID: requestID, ErrorMsg: msg})
}

// buildAnalyticsEvent assembles the Kinesis event for a persona request, porting
// create_analytic_event. request carries the raw identifiers (the sink md5-hashes
// email/phone in place before publish); response is the final client shape;
// analyticResponse is the pre-transform stripped response. Headers are the subset
// go-you has (Flask carries more; the extras are omitted, not consumed downstream).
func buildAnalyticsEvent(r *http.Request, requestID, tenantID string, req *model.PersonaRequest, out, analyticResp map[string]any, apiTime float64) analytics.Event {
	return analytics.Event{
		RequestID:        requestID,
		TenantID:         tenantID,
		Headers:          analyticsHeaders(r),
		Request:          toStrippedMap(req),
		LoggingContext:   map[string]any{"client_ip": clientIP(r)},
		Response:         out,
		AnalyticResponse: analyticResp,
		Timestamp:        time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		EventName:        analytics.EventName,
		Namespace:        os.Getenv("NAMESPACE"),
		ClientReqStr:     "",
		APITimeTaken:     roundTo(apiTime, 3),
	}
}

// analyticsHeaders extracts the request headers the analytics event records (the
// subset go-you receives behind the ingress).
func analyticsHeaders(r *http.Request) map[string]string {
	h := map[string]string{}
	for _, k := range []string{"X-Request-Id", "X-Real-Ip", "X-Forwarded-For", "User-Agent", "Referer", "X-Original-Forwarded-For"} {
		if v := r.Header.Get(k); v != "" {
			h[k] = v
		}
	}
	return h
}

// clientIP returns the best-effort client IP from the standard proxy headers,
// falling back to RemoteAddr.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-Ip"); v != "" {
		return v
	}
	return r.RemoteAddr
}

// roundTo rounds x to n decimal places (matches Python round(_, 3)).
func roundTo(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}
