package upi

// DefaultConfig ports constants/config_constants.py upi_config_default (the
// token-free subset). Token-pool / donate / vendor sources (PAYU_UPI, EASEBUZZ,
// PHONEPE_EMULATOR-disabled) are retained in the list with Enabled matching the
// Python default, but getSourceCrawler returns nil for the unported ones so they
// are simply skipped at fan-out time. This keeps sampling counts faithful to
// prod while only actually calling token-free endpoints.
func DefaultConfig() *Config {
	return &Config{
		ClientResponse: false,
		SuffixList: []SuffixEntry{
			{"ybl", 1}, {"axl", 1}, {"ibl", 3}, {"paytm", 1}, {"bhim", 1},
			{"okicici", 1}, {"okhdfcbank", 1}, {"oksbi", 1}, {"okaxis", 1}, {"axisb", 1},
		},
		SourceList: []SourceConfig{
			{Name: "EMT_UPI", Type: "ENRICHED", Weight: 3, Enabled: true},
			{Name: "MMT_UPI", Type: "ENRICHED", Weight: 2, Enabled: true},
			{Name: "PAYU_UPI", Type: "ENRICHED", Weight: 2, Common: &SourceCommon{Lite: false, Enriched: true}, Enabled: true},
			{Name: "GIVE_UPI", Type: "ENRICHED", Weight: 3, Common: &SourceCommon{Lite: false, Enriched: true}, Enabled: true},
			{Name: "EASEBUZZ", Type: "ENRICHED", Weight: 2, Enabled: true},
			{Name: "NYKAA_UPI", Type: "ENRICHED", Weight: 4, Enabled: false},
			{Name: "RAILYATRI_UPI", Type: "ENRICHED", Weight: 1, Enabled: false},
			{Name: "PHONEPE_EMULATOR", Type: "ENRICHED", Weight: 0, Enabled: false},
			{Name: "JUST_PAY_UPI", Type: "LITE", Weight: 2, Enabled: true},
			{Name: "KAYAK_EMT", Type: "LITE", Weight: 2, Enabled: true},
			{Name: "KETTO_UPI", Type: "ENRICHED", Weight: 2, Enabled: false},
			{Name: "CONFIRMTKT_UPI", Type: "ENRICHED", Weight: 2, Enabled: true},
			{Name: "GOIBIBO_UPI", Type: "ENRICHED", Weight: 3, Enabled: false},
			{Name: "IndiGo_UPI", Type: "LITE", Weight: 2, Enabled: false},
			{Name: "PRACTO_UPI", Type: "LITE", Weight: 2, Enabled: false},
			{Name: "BRB_RAZORPAY_UPI", Type: "LITE", Weight: 3, Common: &SourceCommon{Lite: true, Enriched: false}, Enabled: true},
		},
		TPISources: []SourceConfig{
			{Name: "CASH_FREE_UPI", Type: "ENRICHED", Weight: 1, Enabled: true},
		},
		HitOrganicSources:          true,
		Lite:                       false,
		LiteReturnAll:              false,
		LiteSeq:                    false,
		Enriched:                   true,
		EnrichedReturnAll:          false,
		EnrichedSeq:                false,
		LiteSourcesSampleCount:     3,
		EnrichedSourcesSampleCount: 4,
		EnrichedSourcesCountForHA:  2,
		EnrichedSuffixSampleCount:  4,
	}
}
