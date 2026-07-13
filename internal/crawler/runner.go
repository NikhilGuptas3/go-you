package crawler

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"
)

// errTimedOut is reported for a crawler that didn't finish before the shared
// request deadline, so partial responses distinguish timeout from failure.
var errTimedOut = errors.New("timed out")

// Runner fans out a set of crawlers concurrently over one identifier, the way
// real_time_data_service.get_organic_persona does with its thread pool. Each
// crawler runs in its own goroutine; the shared context deadline enforces the
// leaf-level timeout model (parents do not impose their own timeout — see the
// hey-you-timeout-model note).
//
// The POC uses a single static proxy (or none) rather than the Python Redis
// rotating pool. proxyURL may be nil => crawl direct.
type Runner struct {
	proxyURL *url.URL
	crawlers []Crawler
}

func NewRunner(proxyURL *url.URL, crawlers ...Crawler) *Runner {
	return &Runner{proxyURL: proxyURL, crawlers: crawlers}
}

// Run probes every crawler of the given kind for identifier and returns one
// Result each. A single crawler failing never fails the batch — its Result
// carries the error, mirroring the Python per-section error handling.
func (r *Runner) Run(ctx context.Context, kind Kind, identifier string) []Result {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var results []Result

	for _, c := range r.crawlers {
		if c.Kind() != kind {
			continue
		}
		wg.Add(1)
		go func(c Crawler) {
			defer wg.Done()

			started := time.Now()
			exist, err := c.Check(ctx, identifier, r.proxyURL)
			res := Result{Website: c.Website(), Kind: c.Kind(), Duration: time.Since(started)}
			if err != nil {
				// Distinguish "we ran out of time" from a crawler-specific
				// failure so partial responses read clearly. A single crawler's
				// error never fails the batch — the others still return.
				if ctx.Err() != nil {
					res.Err = errTimedOut
				} else {
					res.Err = err
				}
			} else {
				res.UserExist = &exist
			}

			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(c)
	}

	wg.Wait()
	return results
}
