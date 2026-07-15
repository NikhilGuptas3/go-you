package appconfig

import "testing"

const sampleTenantConfig = `{
  "someOtherKey": {"x": 1},
  "youConfig": {
    "breach": true,
    "phone_meta": true,
    "email_meta": false,
    "prediction": true,
    "request_timeout": [8.0, 12.0, 14.0],
    "websites": {
      "FLIPKART":  {"enabled": true,  "phone_enabled": true,  "email_enabled": true},
      "INSTAGRAM": {"enabled": true,  "phone_enabled": true,  "email_enabled": false},
      "SPOTIFY":   {"enabled": true,  "phone_enabled": false, "email_enabled": true},
      "AMAZON":    {"enabled": true,  "phone_enabled": true,  "email_enabled": true, "client_response": false},
      "DISABLED":  {"enabled": false, "phone_enabled": true,  "email_enabled": true}
    },
    "phone_info": {"enabled": true, "postpaid": true, "dnd_status": false},
    "email_info": {"enabled": true, "domain_intelligence": {"enabled": true}},
    "common_intelligence": {"enabled": true},
    "intelligence": {"enabled": true}
  }
}`

func mustParse(t *testing.T) *YouConfiguration {
	t.Helper()
	yc, err := ParseYouConfig(sampleTenantConfig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return yc
}

func TestParseYouConfig_MissingYouConfig(t *testing.T) {
	if _, err := ParseYouConfig(`{"foo": 1}`); err == nil {
		t.Fatal("expected error when youConfig absent")
	}
	if _, err := ParseYouConfig(`not json`); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestGates(t *testing.T) {
	yc := mustParse(t)
	if !yc.Breach || !yc.PhoneMeta || yc.EmailMeta || !yc.Prediction {
		t.Errorf("top-level gates: breach=%v phone_meta=%v email_meta=%v prediction=%v",
			yc.Breach, yc.PhoneMeta, yc.EmailMeta, yc.Prediction)
	}
	if !yc.IsPostpaidEnabled() {
		t.Error("postpaid should be enabled (phone_info.enabled && phone_info.postpaid)")
	}
	if yc.IsDndEnabled() {
		t.Error("dnd should be disabled (phone_info.dnd_status false)")
	}
	if !yc.IsDomainIntelligenceEnabled() {
		t.Error("domain intelligence should be enabled")
	}
	if !yc.IsCommonIntelligenceEnabled() {
		t.Error("common intelligence should be enabled")
	}
}

func TestPhoneInfoAbsentDefaultsEnabled(t *testing.T) {
	// phone_info absent => is_phone_info_enabled true, but postpaid flag absent
	// => IsPostpaidEnabled false.
	yc, err := ParseYouConfig(`{"youConfig": {"websites": {}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if yc.IsPostpaidEnabled() {
		t.Error("postpaid should be false when phone_info.postpaid absent")
	}
	// domain_intelligence absent => false
	if yc.IsDomainIntelligenceEnabled() {
		t.Error("domain intelligence should be false when absent")
	}
}

func TestIsWebsiteEnabled(t *testing.T) {
	yc := mustParse(t)
	cases := []struct {
		site, kind string
		want       bool
	}{
		{"FLIPKART", "phone", true},
		{"FLIPKART", "email", true},
		{"INSTAGRAM", "phone", true},
		{"INSTAGRAM", "email", false}, // email_enabled false
		{"SPOTIFY", "phone", false},   // phone_enabled false
		{"SPOTIFY", "email", true},
		{"DISABLED", "phone", false}, // enabled false
		{"MISSING", "phone", false},  // absent
	}
	for _, c := range cases {
		if got := yc.IsWebsiteEnabled(c.site, c.kind); got != c.want {
			t.Errorf("IsWebsiteEnabled(%s,%s)=%v want %v", c.site, c.kind, got, c.want)
		}
	}
}

func TestClientResponseDefault(t *testing.T) {
	yc := mustParse(t)
	if yc.ClientResponse("AMAZON") { // explicitly false
		t.Error("AMAZON client_response should be false")
	}
	if !yc.ClientResponse("FLIPKART") { // absent => true
		t.Error("FLIPKART client_response should default true")
	}
	if !yc.ClientResponse("MISSING") { // absent site => true
		t.Error("missing site client_response should default true")
	}
}

func TestRequestTimeoutFor(t *testing.T) {
	yc := mustParse(t) // [8, 12, 14]
	cases := []struct {
		count int
		want  float64
	}{
		{1, 8}, {2, 12}, {3, 14}, {5, 14}, {0, 8}, // clamp low/high, matches Python min(count,len)-1 with count>=1
	}
	for _, c := range cases {
		got, ok := yc.RequestTimeoutFor(c.count)
		if !ok || got != c.want {
			t.Errorf("RequestTimeoutFor(%d)=%v,%v want %v", c.count, got, ok, c.want)
		}
	}
	// Empty list => not configured.
	empty, _ := ParseYouConfig(`{"youConfig":{"request_timeout":[],"websites":{}}}`)
	if _, ok := empty.RequestTimeoutFor(1); ok {
		t.Error("empty request_timeout should report not-configured")
	}
}

func TestCrawlSet(t *testing.T) {
	yc := mustParse(t)
	available := []string{"FLIPKART", "INSTAGRAM", "AMAZON", "SPOTIFY", "DISABLED", "LINKEDIN"}
	globalDisabled := []string{"AMAZON"} // AMAZON disabled globally

	phone := CrawlSet("phone", available, yc, globalDisabled)
	// FLIPKART(phone yes), INSTAGRAM(phone yes), AMAZON(disabled global), SPOTIFY(phone no),
	// DISABLED(enabled false), LINKEDIN(token-pool). => FLIPKART, INSTAGRAM
	assertSet(t, "phone", phone, []string{"FLIPKART", "INSTAGRAM"})

	email := CrawlSet("email", available, yc, globalDisabled)
	// FLIPKART(email yes), INSTAGRAM(email no), AMAZON(global disabled), SPOTIFY(email yes),
	// DISABLED(off), LINKEDIN(token). => FLIPKART, SPOTIFY
	assertSet(t, "email", email, []string{"FLIPKART", "SPOTIFY"})
}

func assertSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s crawl set: got %v want %v", label, got, want)
	}
	gm := map[string]struct{}{}
	for _, g := range got {
		gm[g] = struct{}{}
	}
	for _, w := range want {
		if _, ok := gm[w]; !ok {
			t.Errorf("%s crawl set missing %q (got %v)", label, w, got)
		}
	}
}
