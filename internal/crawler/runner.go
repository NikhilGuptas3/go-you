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

// errNoConditionMatched mirrors the Python NoConditionMatchedException — a
// response that fits none of a spider's known verdict branches. It is an error,
// not a false verdict.
var errNoConditionMatched = errors.New("no condition matched")

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
	// byKind indexes registered crawlers by kind then website, so a config-driven
	// request can select exactly the sites the tenant enabled.
	byKind map[Kind]map[string]Crawler
}

func NewRunner(proxyURL *url.URL, crawlers ...Crawler) *Runner {
	r := &Runner{proxyURL: proxyURL, crawlers: crawlers, byKind: map[Kind]map[string]Crawler{}}
	for _, c := range crawlers {
		m := r.byKind[c.Kind()]
		if m == nil {
			m = map[string]Crawler{}
			r.byKind[c.Kind()] = m
		}
		m[c.Website()] = c
	}
	return r
}

// Lookup returns the registered crawler for (kind, website), or nil. Used by the
// handler to reach a rich crawler's extra API (e.g. the UPI adapter's Config()).
func (r *Runner) Lookup(kind Kind, website string) Crawler {
	if m := r.byKind[kind]; m != nil {
		return m[website]
	}
	return nil
}

// Available returns the registered website names for a kind — go-you's
// equivalent of the PhoneFactory/EmailFactory registry, used by appconfig.CrawlSet
// to intersect with tenant enablement.
func (r *Runner) Available(kind Kind) []string {
	m := r.byKind[kind]
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	return out
}

// Run probes every crawler of the given kind for identifier and returns one
// Result each. A single crawler failing never fails the batch — its Result
// carries the error, mirroring the Python per-section error handling.
//
// This is the unfiltered path (all registered crawlers of the kind). The
// config-driven path is RunSites.
func (r *Runner) Run(ctx context.Context, kind Kind, identifier string) []Result {
	sel := make([]Crawler, 0, len(r.byKind[kind]))
	for _, c := range r.byKind[kind] {
		sel = append(sel, c)
	}
	return r.run(ctx, sel, identifier)
}

// RunSites probes only the named crawlers of the given kind (the tenant's
// CrawlSet). Unknown/unregistered names are silently skipped — the config may
// enable a site go-you hasn't ported yet.
func (r *Runner) RunSites(ctx context.Context, kind Kind, identifier string, sites []string) []Result {
	m := r.byKind[kind]
	sel := make([]Crawler, 0, len(sites))
	for _, name := range sites {
		if c, ok := m[name]; ok {
			sel = append(sel, c)
		}
	}
	return r.run(ctx, sel, identifier)
}

func (r *Runner) run(ctx context.Context, crawlers []Crawler, identifier string) []Result {
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]Result, 0, len(crawlers))

	for _, c := range crawlers {
		wg.Add(1)
		go func(c Crawler) {
			defer wg.Done()

			started := time.Now()
			res := Result{Website: c.Website(), Kind: c.Kind()}

			// Prefer the rich path when the crawler is a DetailCrawler.
			var err error
			if dc, ok := c.(DetailCrawler); ok {
				var exist *bool
				var data map[string]any
				exist, data, err = dc.CheckDetail(ctx, identifier, r.proxyURL)
				if err == nil {
					res.UserExist = exist
					res.Data = data
				}
			} else {
				var exist bool
				exist, err = c.Check(ctx, identifier, r.proxyURL)
				if err == nil {
					res.UserExist = &exist
				}
			}
			res.Duration = time.Since(started)

			if err != nil {
				// Distinguish "we ran out of time" from a crawler-specific
				// failure so partial responses read clearly. A single crawler's
				// error never fails the batch — the others still return.
				if ctx.Err() != nil {
					res.Err = errTimedOut
				} else {
					res.Err = err
				}
			}

			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(c)
	}

	wg.Wait()
	return results
}
