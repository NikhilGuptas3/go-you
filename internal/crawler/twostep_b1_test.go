package crawler

import (
	"testing"
	"time"
)

// Contract test for the Phase B1 token-gated crawlers: a stable Website() id
// (config gating keys off it) and the correct Kind() per registered variant.
// The two-step network behavior is covered structurally by twostep_test.go
// (jar replay) and validated live via the opt-in smoke test; these crawlers hit
// hardcoded real hosts, matching the package's no-networked-unit-test convention.
func TestB1CrawlerContract(t *testing.T) {
	cases := []struct {
		c    Crawler
		site string
		kind Kind
	}{
		{NewEventbrite(time.Second), "EVENTBRITE", KindEmail},
		{NewTrivago(time.Second), "TRIVAGO", KindEmail},
		{NewVimeo(time.Second), "VIMEO", KindEmail},
		{NewOyorooms(time.Second), "OYOROOMS", KindPhone},
		{NewZohoPhone(time.Second), "ZOHO", KindPhone},
		{NewZohoEmail(time.Second), "ZOHO", KindEmail},
		{NewShopcluesPhone(time.Second), "SHOPCLUES", KindPhone},
		{NewShopcluesEmail(time.Second), "SHOPCLUES", KindEmail},
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
