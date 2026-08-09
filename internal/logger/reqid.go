package logger

import "context"

// Request-id context propagation. hey-you generates a per-request uuid but only
// threads it into the response and the analytics event, never onto log lines
// and never through context (the fan-out threads it as a function argument).
// go-you stashes it in context so middleware, the handler, and deep code (the
// crawler runner) can all stamp the same rid on their logs without threading a
// string everywhere. This mirrors auth.FromContext's pattern (a private key
// type + typed accessors).

type reqIDKey struct{}

// WithRequestID returns a child context carrying the request id. An empty id is
// ignored (the parent is returned unchanged) so callers need not guard.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, reqIDKey{}, id)
}

// RequestIDFromContext returns the request id stashed by WithRequestID, or ""
// when absent.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(reqIDKey{}).(string); ok {
		return v
	}
	return ""
}
