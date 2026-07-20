// Package upi ports the Python UPI subsystem (crawler/spiders/bank/upi) — the
// UPI aggregator crawler plus its token-free HTTP source crawlers.
//
// A UPI probe takes a phone number and, for a set of bank suffixes (ybl, axl,
// paytm, okicici, ...), checks whether "<national10>@<suffix>" is a valid VPA
// and, when the source enriches, recovers the account-holder name. The
// aggregator fans out across sampled sources and suffixes, aggregates the
// per-suffix profiles into one by suffix priority, and derives the verified_names
// list. This mirrors UPI.py + upi_mapper.py + upi_util.py + upi_base.py.
//
// Cloud-only paths are dropped per the no-cloud constraint: no organic cache
// read/write (always live), no DynamoDB. Token-pool sources (PayU family, the
// *_donate sources, EaseBuzz) are out of scope; only token-free direct sources
// and the Cashfree TPI source are ported.
package upi

// Profile is one per-suffix (or aggregated) UPI verdict, mirroring
// upi_profile.py UPIProfile. Analytics-only fields (vpa_mismatch/vpa_match/
// masked_vpa/match_type) are carried through aggregation and stripped before the
// client sees them (clean_upi_profile_for_response).
type Profile struct {
	Source    string
	Flow      string // "COMMON" | "NON_COMMON"
	Suffix    string
	AppName   string
	UserExist *bool
	Name      string
	VPA       string
	// Err is the profile-level error marker (Python's `error` attr): non-empty
	// string or a bool sentinel. Represented as *bool-like via ErrVal below.
	Err any
	// Analytics-only (stripped from client).
	VPAMismatch *bool
	VPAMatch    string
	MaskedVPA   string
	MatchType   string
}

// hasError reports whether the profile carries a Python-truthy `error`.
func (p *Profile) hasError() bool {
	switch e := p.Err.(type) {
	case nil:
		return false
	case bool:
		return e
	case string:
		return e != ""
	default:
		return true
	}
}

// Profiles is the aggregated result, mirroring upi_profile.py UPIProfiles.
type Profiles struct {
	UserExist     *bool
	Source        string
	Flow          string
	Suffix        string
	AppName       string
	Name          string
	Profiles      []*Profile
	VerifiedNames []map[string]any
	Err           any
	LiteError     any
	EnrichedError any
	Meta          *Meta
}

func (p *Profiles) hasError() bool {
	switch e := p.Err.(type) {
	case nil:
		return false
	case bool:
		return e
	case string:
		return e != ""
	default:
		return true
	}
}

// SuffixEntry is one {name, priority} from suffix_list.
type SuffixEntry struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

// SourceCommon is the per-source `common` block controlling COMMON-flow behavior.
type SourceCommon struct {
	Lite     bool `json:"lite"`
	Enriched bool `json:"enriched"`
}

// SourceConfig is one entry of source_list / TPI_SOURCES.
type SourceConfig struct {
	Name    string        `json:"name"`
	Type    string        `json:"type"` // "LITE" | "ENRICHED"
	Weight  float64       `json:"weight"`
	Enabled bool          `json:"enabled"`
	Timeout float64       `json:"timeout"`
	Common  *SourceCommon `json:"common"`
}

// Config is the resolved upi_config (global default overlaid with tenant
// website_config[UPI]), mirroring UPIConfig.
type Config struct {
	ClientResponse             bool           `json:"CLIENT_RESPONSE"`
	SuffixList                 []SuffixEntry  `json:"suffix_list"`
	SourceList                 []SourceConfig `json:"source_list"`
	TPISources                 []SourceConfig `json:"TPI_SOURCES"`
	HitOrganicSources          bool           `json:"hit_organic_sources"`
	Lite                       bool           `json:"LITE"`
	LiteReturnAll              bool           `json:"LITE_RETURN_ALL"`
	LiteSeq                    bool           `json:"LITE_SEQ"`
	Enriched                   bool           `json:"ENRICHED"`
	EnrichedReturnAll          bool           `json:"ENRICHED_RETURN_ALL"`
	EnrichedSeq                bool           `json:"ENRICHED_SEQ"`
	LiteSourcesSampleCount     int            `json:"lite_sources_sample_count"`
	EnrichedSourcesSampleCount int            `json:"enriched_sources_sample_count"`
	EnrichedSourcesCountForHA  int            `json:"enriched_sources_count_for_ha"`
	EnrichedSuffixSampleCount  int            `json:"enriched_suffix_sample_count"`
}

// Meta is the per-request derived config the engine consumes, mirroring UPIMeta.
type Meta struct {
	Lite                      bool
	LiteReturnAll             bool
	LiteSeq                   bool
	Enriched                  bool
	EnrichedReturnAll         bool
	EnrichedSeq               bool
	HitOrganicSources         bool
	SuffixList                []SuffixEntry
	SourceList                []SourceConfig
	TPISources                []SourceConfig
	LiteSources               []SourceConfig
	EnrichedSources           []SourceConfig
	EnrichedSuffixSampleCount int
	EnrichedSourcesCountForHA int
}

// SourceMeta is passed to each source crawler for one fan-out call, mirroring
// UPISourceMeta: which source, which suffixes to probe, and whether to return
// all suffix results vs stop at the first hit.
type SourceMeta struct {
	Source     SourceConfig
	SuffixList []string
	ReturnAll  bool
}
