package upi

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// Deps carries the runtime configuration the UPI subsystem needs, resolved by
// the handler from ConfigFetcher and passed in at construction.
type Deps struct {
	// UPIConfigJSON is the raw `upi_config` object (global default), already
	// overlaid with the tenant's website_config[UPI]. Parsed into Config.
	UPIConfigJSON json.RawMessage
	Cashfree      CashfreeCreds
	PhonePeURL    string
	PhonePeToken  string
}

// Crawler is the UPI phone DetailCrawler the runner registers. It runs the full
// aggregator and returns the aggregated profile as rich Data, matching the
// Python UPI spider's per-site account entry (before transform_upi_response
// reshapes it for the client).
type Crawler struct {
	deps    Deps
	cfg     *Config
	timeout time.Duration
}

// New builds the UPI crawler from resolved deps. cfg is parsed once; the tenant
// overlay must already be applied to deps.UPIConfigJSON by the caller.
func New(deps Deps, timeout time.Duration) *Crawler {
	cfg := parseConfig(deps.UPIConfigJSON)
	return &Crawler{deps: deps, cfg: cfg, timeout: timeout}
}

func (c *Crawler) Website() string { return "UPI" }
func (c *Crawler) Kind() string    { return "phone" }

// CheckDetail runs the aggregator for identifier (international "+91…") and
// returns (user_exist, rich-data, err). The rich-data map is the RAW aggregated
// profile (profiles + verified_names + name/vpa/suffix); transform_upi_response
// in the handler reshapes it into the client form. A nil user_exist means the
// aggregator errored (transform maps that to {"error": true}).
func (c *Crawler) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	national := last10(stripPlus(identifier))
	meta := buildMeta(c.cfg, national)

	// Leaf-only timeout: bound the whole aggregation to the crawler timeout.
	cctx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	agg := aggregate(cctx, meta, national, identifier, c.deps, proxyURL)

	data := rawProfileMap(agg)
	if agg.hasError() {
		// error aggregate -> user_exist unknown; transform emits {"error":true}.
		data["error"] = true
		return nil, data, nil
	}
	return agg.UserExist, data, nil
}

// Config exposes the parsed UPI config (client_response gate etc.) so the
// handler's transform can honor CLIENT_RESPONSE without re-parsing.
func (c *Crawler) Config() *Config { return c.cfg }

// rawProfileMap serializes the aggregated Profiles into the map shape the Python
// spider returns (to_dict(profile)), so transform_upi_response can reshape it.
func rawProfileMap(agg *Profiles) map[string]any {
	m := map[string]any{}
	if agg.UserExist != nil {
		m["user_exist"] = *agg.UserExist
	}
	if agg.Name != "" {
		m["name"] = agg.Name
	}
	if agg.Suffix != "" {
		m["suffix"] = agg.Suffix
	}
	if agg.Source != "" {
		m["source"] = agg.Source
	}
	if agg.AppName != "" {
		m["app_name"] = agg.AppName
	}
	if agg.VerifiedNames != nil {
		m["verified_names"] = agg.VerifiedNames
	}
	m["profiles"] = profilesToMaps(agg.Profiles)
	return m
}

func profilesToMaps(ps []*Profile) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		pm := map[string]any{"source": p.Source, "suffix": p.Suffix}
		if p.UserExist != nil {
			pm["user_exist"] = *p.UserExist
		}
		if p.Name != "" {
			pm["name"] = p.Name
		}
		if p.VPA != "" {
			pm["vpa"] = p.VPA
		}
		if p.AppName != "" {
			pm["app_name"] = p.AppName
		}
		if p.MatchType != "" {
			pm["match_type"] = p.MatchType
		}
		if p.hasError() {
			pm["error"] = true
		}
		out = append(out, pm)
	}
	return out
}

func stripPlus(s string) string {
	if len(s) > 0 && s[0] == '+' {
		return s[1:]
	}
	return s
}

// parseConfig decodes the merged upi_config JSON into Config, falling back to
// the built-in default when the JSON is empty/invalid.
func parseConfig(raw json.RawMessage) *Config {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg
	}
	var parsed Config
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return cfg
	}
	return &parsed
}
