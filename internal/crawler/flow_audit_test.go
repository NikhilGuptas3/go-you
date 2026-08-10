package crawler

import (
	"testing"
	"time"
)

// Contract test for the 2026-08-10 flow-audit additions: the new email variants
// of phone-registered sites, the Tier-1 stateless crawlers, and the Tier-2
// two-step crawlers. Asserts Website()/Kind() so a registration mistake (wrong
// kind, typo'd website id) fails loudly.
func TestFlowAuditCrawlerContract(t *testing.T) {
	cases := []struct {
		c    Crawler
		site string
		kind Kind
	}{
		// Email variants of previously phone-only sites.
		{NewFlipkartEmail(time.Second), "FLIPKART", KindEmail},
		{NewInstagramEmail(time.Second), "INSTAGRAM", KindEmail},
		{NewIrctcEmail(time.Second), "IRCTC", KindEmail},
		{NewHousingEmail(time.Second), "HOUSING", KindEmail},
		{NewToiEmail(time.Second), "TOI", KindEmail},
		{NewTimesPrimeEmail(time.Second), "TIMES_PRIME", KindEmail},
		// The phone originals must keep their kind.
		{NewFlipkart(time.Second), "FLIPKART", KindPhone},
		{NewInstagram(time.Second), "INSTAGRAM", KindPhone},
		{NewIrctc(time.Second), "IRCTC", KindPhone},
		{NewHousing(time.Second), "HOUSING", KindPhone},
		// Tier 1.
		{NewShaadiPhone(time.Second), "SHAADI", KindPhone},
		{NewGoogleEmail(time.Second), "GOOGLE", KindEmail},
		{NewNetflixEmail(time.Second), "NETFLIX", KindEmail},
		{NewFacebookPhone(time.Second), "FACEBOOK", KindPhone},
		{NewFacebookEmail(time.Second), "FACEBOOK", KindEmail},
		// Tier 2.
		{NewMicrosoftPhone(time.Second), "MICROSOFT", KindPhone},
		{NewMicrosoftEmail(time.Second), "MICROSOFT", KindEmail},
		{NewTwitterPhone(time.Second), "TWITTER", KindPhone},
	}
	for _, tc := range cases {
		if got := tc.c.Website(); got != tc.site {
			t.Errorf("Website() = %q, want %q", got, tc.site)
		}
		if got := tc.c.Kind(); got != tc.kind {
			t.Errorf("%s Kind() = %q, want %q", tc.site, got, tc.kind)
		}
	}
}

// The Microsoft and Twitter token-scrape regexes are the fragile bits — pin them
// against representative markup so a refactor can't silently break token parsing.
func TestTier2Regexes(t *testing.T) {
	msHTML := []byte(`...,"sFT":"AAABBB.token","sCtx":"ctx-123","apiCanary":"canary-xyz","correlationId":"corr-1","sessionId":"sess-9","hpgact":1800,"hpgid":1104,...`)
	first := func(m [][]byte) string {
		if len(m) == 2 {
			return string(m[1])
		}
		return ""
	}
	ms := map[string]string{
		"sFT":       first(msFT.FindSubmatch(msHTML)),
		"sCtx":      first(msCtx.FindSubmatch(msHTML)),
		"apiCanary": first(msCanary.FindSubmatch(msHTML)),
		"corrID":    first(msCorrID.FindSubmatch(msHTML)),
		"sessionId": first(msSession.FindSubmatch(msHTML)),
		"hpgact":    first(msHpgact.FindSubmatch(msHTML)),
		"hpgid":     first(msHpgid.FindSubmatch(msHTML)),
	}
	want := map[string]string{
		"sFT": "AAABBB.token", "sCtx": "ctx-123", "apiCanary": "canary-xyz",
		"corrID": "corr-1", "sessionId": "sess-9", "hpgact": "1800", "hpgid": "1104",
	}
	for k, w := range want {
		if ms[k] != w {
			t.Errorf("microsoft %s = %q, want %q", k, ms[k], w)
		}
	}

	twHTML := []byte(`<input type="hidden" name="authenticity_token" value="tok_abc123XYZ">`)
	if m := twitterAuthTokenRe.FindSubmatch(twHTML); len(m) != 2 || string(m[1]) != "tok_abc123XYZ" {
		t.Errorf("twitter auth token regex failed: %v", m)
	}
}
