package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/logger"
)

// This file holds the HTTP middleware for the persona service: request-id
// propagation, a structured access log, and a logging panic recoverer. They are
// plain net/http middleware (func(http.Handler) http.Handler), composed in
// main.go's chi chain. Keeping them here (not in main.go) keeps main.go a thin
// composition root and lets them share the handler package's component logger.

// mwLog is the component logger for middleware lines ("http:<func> - …").
var mwLog = logger.Component("http")

// headerRequestID is the correlation header read on the way in and echoed out.
const headerRequestID = "X-Request-Id"

// RequestID middleware reconciles an inbound X-Request-Id with a freshly minted
// uuid: if the client (or the ingress) sent one, reuse it; otherwise generate
// one. Either way it is stashed in the request context (logger.WithRequestID)
// so the handler and deep code read the same id, and echoed back on the
// response so callers can correlate. This replaces the previous split where
// persona.go minted its own id and the inbound header was only seen by
// analytics.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(headerRequestID)
		if rid == "" {
			rid = uuid.NewString()
		}
		w.Header().Set(headerRequestID, rid)
		ctx := logger.WithRequestID(r.Context(), rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code and byte
// count for the access log. It defaults to 200 (the status when WriteHeader is
// never called explicitly, matching net/http).
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// AccessLog emits one INFO line per request with method, path, status, latency,
// rid, and — when auth has already populated it — the tenant. It is the generic
// access log; the persona handler additionally logs a richer domain line with
// id-presence and timings. Probe/scrape endpoints are skipped so the access log
// is real traffic only.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isNoiseRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		attrs := []any{
			"rid", logger.RequestIDFromContext(r.Context()),
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "bytes", rec.bytes,
			"took", roundTo(time.Since(start).Seconds(), 3),
		}
		// tenant is known only if auth ran before this point in the chain.
		if t, ok := auth.FromContext(r.Context()); ok {
			attrs = append(attrs, "tenant", t.ID)
		}
		mwLog.Info("request", attrs...)
	})
}

// Recoverer replaces chi's middleware.Recoverer so a panic is logged through
// go-you's logger (with the rid) at ERROR before returning 500, instead of
// chi's default stderr dump. It never re-panics.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				mwLog.Error("panic recovered",
					"rid", logger.RequestIDFromContext(r.Context()),
					"path", r.URL.Path, "panic", toStr(rec))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"Internal server error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// isNoiseRoute reports whether a path is an infra probe / scrape endpoint that
// should not produce an access-log line on every scrape tick.
func isNoiseRoute(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/metrics", "/ping":
		return true
	}
	return false
}

// toStr renders a recovered panic value for the log without importing fmt at
// the top level of the hot path.
func toStr(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "non-string panic value"
}
