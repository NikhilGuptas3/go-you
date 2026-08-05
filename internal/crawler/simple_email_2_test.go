package crawler

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"
)

// These crawlers hit real third-party endpoints, so like the rest of the crawler
// package they have no networked unit tests. What we CAN pin cheaply is the
// registration contract every crawler must satisfy: a stable Website() id and the
// correct Kind(). A wrong Website() silently breaks config gating (CrawlSet keys
// off it); a wrong Kind() routes the crawler to the wrong fan-out branch.
func TestBatch2CrawlerContract(t *testing.T) {
	cases := []struct {
		c    Crawler
		site string
	}{
		{NewNaukri(time.Second), "NAUKRI"},
		{NewBodybuilding(time.Second), "BODYBUILDING"},
		{NewAtlassian(time.Second), "ATLASSIAN"},
		{NewFlickr(time.Second), "FLICKR"},
		{NewShaadiEmail(time.Second), "SHAADI"},
	}
	for _, tc := range cases {
		if got := tc.c.Website(); got != tc.site {
			t.Errorf("Website() = %q, want %q", got, tc.site)
		}
		if got := tc.c.Kind(); got != KindEmail {
			t.Errorf("%s Kind() = %q, want email", tc.site, got)
		}
	}
}

// TestBatch2Live is an opt-in smoke test against the real endpoints, run only
// when CRAWLER_LIVE=1 (and optionally CRAWLER_PROXY=<url>). It does not assert a
// specific verdict — the account may or may not exist — only that the crawler
// returns without a transport/parse error, i.e. the endpoint and parse still
// work. Use a known-existing email via CRAWLER_LIVE_EMAIL to eyeball true/false.
func TestBatch2Live(t *testing.T) {
	if os.Getenv("CRAWLER_LIVE") != "1" {
		t.Skip("set CRAWLER_LIVE=1 to run the live crawler smoke test")
	}
	email := os.Getenv("CRAWLER_LIVE_EMAIL")
	if email == "" {
		email = "test@example.com"
	}
	var proxy *url.URL
	if p := os.Getenv("CRAWLER_PROXY"); p != "" {
		u, err := url.Parse(p)
		if err != nil {
			t.Fatalf("bad CRAWLER_PROXY: %v", err)
		}
		proxy = u
	}
	crawlers := []Crawler{
		NewNaukri(6 * time.Second),
		NewBodybuilding(6 * time.Second),
		NewAtlassian(6 * time.Second),
		NewFlickr(6 * time.Second),
		NewShaadiEmail(6 * time.Second),
	}
	for _, c := range crawlers {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		exists, err := c.Check(ctx, email, proxy)
		cancel()
		if err != nil {
			t.Logf("%-14s email=%s -> ERROR: %v", c.Website(), email, err)
		} else {
			t.Logf("%-14s email=%s -> user_exist=%v", c.Website(), email, exists)
		}
	}
}
