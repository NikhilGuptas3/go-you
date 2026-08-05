package crawler

import (
	"testing"
	"time"
)

// Contract test for the Phase B2 token-gated crawlers.
func TestB2CrawlerContract(t *testing.T) {
	cases := []struct {
		c    Crawler
		site string
	}{
		{NewTumblr(time.Second), "TUMBLR"},
		{NewQuora(time.Second), "QUORA"},
		{NewCodecademy(time.Second), "CODECADEMY"},
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

// quoraScan reproduces Python's index-then-split('"')[2] extraction; pin it so a
// refactor can't silently break token parsing.
func TestQuoraScan(t *testing.T) {
	body := []byte(`window.ansFrontendGlobals={"formkey":"abc123","revision":"r-99","window_id":"w-77","broadcastId":"b-55"};`)
	cases := map[string]string{
		"formkey":     "abc123",
		"revision":    "r-99",
		"window_id":   "w-77",
		"broadcastId": "b-55",
	}
	for key, want := range cases {
		if got := quoraScan(body, key); got != want {
			t.Errorf("quoraScan(%q) = %q, want %q", key, got, want)
		}
	}
	if got := quoraScan(body, "nonexistent_key"); got != "" {
		t.Errorf("quoraScan(missing) = %q, want empty", got)
	}
}

// The Tumblr and Codecademy regexes are the fragile bits — pin them against
// representative markup.
func TestB2Regexes(t *testing.T) {
	tumblrHTML := `<script>var x={"API_TOKEN":"tok_abcDEF123","other":"y"};</script>`
	if m := tumblrAPITokenRe.FindStringSubmatch(tumblrHTML); len(m) != 2 || m[1] != "tok_abcDEF123" {
		t.Errorf("tumblr API_TOKEN regex failed: %v", m)
	}

	// name-before-content and content-before-name orderings both parse.
	meta1 := `<meta name="csrf-token" content="csrf_xyz789">`
	meta2 := `<meta content="csrf_xyz789" name="csrf-token">`
	if m := codecademyCSRFRe.FindStringSubmatch(meta1); len(m) != 2 || m[1] != "csrf_xyz789" {
		t.Errorf("codecademy csrf regex (name-first) failed: %v", m)
	}
	if m := codecademyCSRFRe2.FindStringSubmatch(meta2); len(m) != 2 || m[1] != "csrf_xyz789" {
		t.Errorf("codecademy csrf regex (content-first) failed: %v", m)
	}
}
