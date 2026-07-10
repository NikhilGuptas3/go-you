package handler

import (
	"context"
	"time"
)

// contextWithTimeout is a thin wrapper so the timeout policy lives in one place.
func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
