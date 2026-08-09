package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/logger"
)

// TestCrawlerStatus pins the Result → status-label mapping used by both the
// metrics and the failure log.
func TestCrawlerStatus(t *testing.T) {
	tru := true
	cases := []struct {
		name string
		res  crawler.Result
		want string
	}{
		{"ok", crawler.Result{UserExist: &tru}, "ok"},
		{"timeout", crawler.Result{Err: errors.New("timed out")}, "timeout"},
		{"failed", crawler.Result{Err: errors.New("quora: home status 403")}, "failed"},
	}
	for _, tc := range cases {
		if got := crawlerStatus(tc.res); got != tc.want {
			t.Errorf("%s: crawlerStatus = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRecordCrawlerTimings exercises the per-crawler timing + metric emission
// across ok / timeout / failed / no-verdict results. It asserts the timing map
// is populated and that the call is safe (metrics are global side effects; the
// point here is no panic and correct tm side effects). It reads rid from
// context so the failure log is correlated.
func TestRecordCrawlerTimings(t *testing.T) {
	tru := true
	results := []crawler.Result{
		{Website: "FLIPKART", Kind: crawler.KindPhone, UserExist: &tru, Duration: 500 * time.Millisecond},
		{Website: "QUORA", Kind: crawler.KindEmail, Err: errors.New("quora: home status 403"), Duration: 800 * time.Millisecond},
		{Website: "SNAPDEAL", Kind: crawler.KindEmail, Err: errors.New("timed out"), Duration: time.Second},
		{Website: "NAUKRI", Kind: crawler.KindEmail, Duration: 100 * time.Millisecond}, // no verdict, no error
	}
	tm := newTimings()
	ctx := logger.WithRequestID(context.Background(), "rid-test")

	recordCrawlerTimings(ctx, tm, results)

	m := tm.asMap()
	for _, w := range []string{"crawl_FLIPKART", "crawl_QUORA", "crawl_SNAPDEAL", "crawl_NAUKRI"} {
		if _, ok := m[w]; !ok {
			t.Errorf("timing %q not recorded", w)
		}
	}
	if m["crawl_FLIPKART"] != 500 {
		t.Errorf("crawl_FLIPKART = %v ms, want 500", m["crawl_FLIPKART"])
	}
}
