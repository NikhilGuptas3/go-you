package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sign3labs/go-you/internal/logger"
)

// TestRequestIDMintsWhenAbsent: no inbound X-Request-Id → a fresh id is minted,
// stashed in context, and echoed on the response.
func TestRequestIDMintsWhenAbsent(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logger.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/persona", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen == "" {
		t.Fatal("request id not stashed in context")
	}
	if got := rec.Header().Get(headerRequestID); got != seen {
		t.Fatalf("echoed X-Request-Id %q != context id %q", got, seen)
	}
}

// TestRequestIDReusesInbound: an inbound X-Request-Id is reused, not replaced.
func TestRequestIDReusesInbound(t *testing.T) {
	const inbound = "client-supplied-rid-42"
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logger.RequestIDFromContext(r.Context())
	}))

	req := httptest.NewRequest("POST", "/v1/persona", nil)
	req.Header.Set(headerRequestID, inbound)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != inbound {
		t.Fatalf("inbound rid not reused: got %q, want %q", seen, inbound)
	}
	if got := rec.Header().Get(headerRequestID); got != inbound {
		t.Fatalf("echoed rid %q != inbound %q", got, inbound)
	}
}

// TestRecovererCatchesPanic: a panic downstream becomes a logged 500, not a
// crash.
func TestRecovererCatchesPanic(t *testing.T) {
	h := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest("GET", "/v1/persona", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // must not panic

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestAccessLogSkipsNoiseRoutes: probe/scrape paths are recognised as noise so
// they don't spam the access log. (We assert isNoiseRoute directly — the log
// side effect goes to stderr.)
func TestAccessLogSkipsNoiseRoutes(t *testing.T) {
	for _, p := range []string{"/healthz", "/readyz", "/metrics", "/ping"} {
		if !isNoiseRoute(p) {
			t.Errorf("%q should be a noise route", p)
		}
	}
	if isNoiseRoute("/v1/persona") {
		t.Error("/v1/persona must NOT be a noise route")
	}
}

// TestStatusRecorderDefaults: a handler that writes a body without calling
// WriteHeader records 200.
func TestStatusRecorderDefaults(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	_, _ = sr.Write([]byte("hi"))
	if sr.status != http.StatusOK {
		t.Errorf("status = %d, want 200", sr.status)
	}
	if sr.bytes != 2 {
		t.Errorf("bytes = %d, want 2", sr.bytes)
	}
}
