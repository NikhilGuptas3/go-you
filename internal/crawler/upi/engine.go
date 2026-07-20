package upi

import (
	"context"
	"net/url"
)

// buildMeta ports Upi.get_upi_meta: resolve the effective config into a Meta,
// splitting sources into LITE/ENRICHED, applying weighted no-replace sampling
// capped at the configured sample counts, and collecting enabled TPI sources.
//
// numpy.random.choice(weighted, no-replace) is reproduced with a deterministic
// weighted sampler seeded by the phone number — same intent (weighted, unique,
// capped), reproducible for tests.
func buildMeta(cfg *Config, seed string) *Meta {
	var lite, enriched []SourceConfig
	var liteW, enrichedW []float64
	for _, s := range cfg.SourceList {
		if s.Timeout == 0 {
			s.Timeout = 6
		}
		if !s.Enabled {
			continue
		}
		switch s.Type {
		case "LITE":
			lite = append(lite, s)
			liteW = append(liteW, s.Weight)
		case "ENRICHED":
			enriched = append(enriched, s)
			enrichedW = append(enrichedW, s.Weight)
		}
	}

	rng := newSeededRand(seed)
	var liteSel []SourceConfig
	if cfg.LiteSourcesSampleCount > 0 && len(lite) >= cfg.LiteSourcesSampleCount {
		liteSel = weightedSampleNoReplace(lite, liteW, cfg.LiteSourcesSampleCount, rng)
	}
	var enrichedSel []SourceConfig
	if len(enriched) > 0 {
		k := cfg.EnrichedSourcesSampleCount
		if k > len(enriched) {
			k = len(enriched)
		}
		enrichedSel = weightedSampleNoReplace(enriched, enrichedW, k, rng)
	}

	var tpi []SourceConfig
	for _, s := range cfg.TPISources {
		if s.Enabled {
			tpi = append(tpi, s)
		}
	}

	return &Meta{
		Lite: cfg.Lite, LiteReturnAll: cfg.LiteReturnAll, LiteSeq: cfg.LiteSeq,
		Enriched: cfg.Enriched, EnrichedReturnAll: cfg.EnrichedReturnAll, EnrichedSeq: cfg.EnrichedSeq,
		HitOrganicSources: cfg.HitOrganicSources, SuffixList: cfg.SuffixList, SourceList: cfg.SourceList,
		TPISources: tpi, LiteSources: liteSel, EnrichedSources: enrichedSel,
		EnrichedSuffixSampleCount: cfg.EnrichedSuffixSampleCount, EnrichedSourcesCountForHA: cfg.EnrichedSourcesCountForHA,
	}
}

// aggregate ports Upi.get_login_response: TPI-first (Cashfree), then LITE phase,
// then ENRICHED phase, aggregating into one Profiles with verified_names.
// national is the 10-digit number, intl the "+91…" form; ctx bounds the whole
// call (leaf-only timeout). proxyURL may be nil (direct).
func aggregate(ctx context.Context, meta *Meta, national, intl string, deps Deps, proxyURL *url.URL) *Profiles {
	var profileList []*Profile  // LITE-phase profiles
	var enrichedList []*Profile // TPI + ENRICHED profiles
	tpiSuffixes := map[string]bool{}

	// --- TPI phase (Cashfree) ---
	tpiErrCount := 0
	for _, s := range meta.TPISources {
		src := getSourceCrawler(s.Name, deps)
		if src == nil {
			continue
		}
		sm := SourceMeta{Source: s, SuffixList: []string{commonSuffixConstant}, ReturnAll: false}
		ps := runSource(ctx, src, national, intl, sm, proxyURL)
		for _, p := range ps {
			if !p.hasError() {
				enrichedList = append(enrichedList, p)
				if p.Suffix != "" {
					tpiSuffixes[p.Suffix] = true
				}
			} else {
				tpiErrCount++
			}
		}
	}
	errInAllTPI := len(meta.TPISources) > 0 && tpiErrCount == len(meta.TPISources) && len(enrichedList) == 0

	agg := &Profiles{Meta: meta}

	if meta.HitOrganicSources || errInAllTPI {
		// --- LITE phase ---
		if meta.Lite && len(meta.LiteSources) > 0 {
			sources := orderCommonFirst(meta.LiteSources, false)
			suffixNames := suffixNamesFor(meta.SuffixList, meta.LiteSeq)
			suffixNames = removeSuffixes(suffixNames, tpiSuffixes)
			commonCount := countCommon(sources, false)
			nonCommon := len(sources) - commonCount
			suffixSets := buildSuffixSets(commonCount, nonCommon, suffixNames)

			liteProfiles := fanOutSources(ctx, sources, suffixSets, national, intl, deps, proxyURL, meta.LiteReturnAll)
			profileList = append(profileList, liteProfiles...)

			agg = aggProfile(toIface(profileList), meta)
			agg.Profiles = profileList
			agg.Meta = meta
			// Early exits, mirroring Python.
			if agg.UserExist != nil && !*agg.UserExist {
				agg.VerifiedNames = verifiedNamesFromProfiles(last10(national), profileList)
				return agg
			}
			if agg.Name != "" {
				agg.VerifiedNames = verifiedNamesFromProfiles(last10(national), profileList)
				return agg
			}
			agg.LiteError = agg.Err
			if ctx.Err() != nil {
				return agg
			}
		}

		// --- ENRICHED phase ---
		if meta.Enriched && len(meta.EnrichedSources) > 0 {
			sources := orderCommonFirst(meta.EnrichedSources, true)
			var suffixList []SuffixEntry
			if agg.Err == nil && agg.UserExist != nil {
				// lite hit: probe only that suffix with HA source count.
				for _, se := range meta.SuffixList {
					if se.Name == agg.Suffix {
						suffixList = append(suffixList, se)
					}
				}
				n := meta.EnrichedSourcesCountForHA
				if n < 1 {
					n = 1
				}
				if n < len(sources) {
					sources = sources[:n]
				}
				suffixList = repeatSuffixes(suffixList, len(sources))
			} else {
				// error in lite: sample top-priority suffixes.
				k := meta.EnrichedSuffixSampleCount
				sorted := sortSuffixesByPriorityDesc(meta.SuffixList)
				sorted = removeSuffixEntries(sorted, tpiSuffixes)
				if k > len(sorted) {
					k = len(sorted)
				}
				suffixList = sorted[:k]
				if len(suffixList) < len(sources) {
					sources = sources[:len(suffixList)]
				}
			}

			suffixNames := make([]string, 0, len(suffixList))
			for _, se := range suffixList {
				suffixNames = append(suffixNames, se.Name)
			}
			commonCount := countCommon(sources, true)
			nonCommon := len(sources) - commonCount
			suffixSets := buildSuffixSets(commonCount, nonCommon, suffixNames)

			enrichedProfiles := fanOutSources(ctx, sources, suffixSets, national, intl, deps, proxyURL, meta.EnrichedReturnAll)
			enrichedList = append(enrichedList, enrichedProfiles...)
		}
	}

	agg = aggProfile(toIface(enrichedList), meta)
	agg.Profiles = append(profileList, enrichedList...)
	agg.VerifiedNames = verifiedNamesFromProfiles(last10(national), agg.Profiles)
	agg.EnrichedError = agg.Err
	agg.Meta = meta
	return agg
}

// fanOutSources runs each source over its assigned suffix set concurrently and
// flattens the resulting profiles.
func fanOutSources(ctx context.Context, sources []SourceConfig, suffixSets [][]string, national, intl string, deps Deps, proxyURL *url.URL, returnAll bool) []*Profile {
	type res struct{ ps []*Profile }
	ch := make(chan res, len(sources))
	live := 0
	for i, s := range sources {
		src := getSourceCrawler(s.Name, deps)
		if src == nil {
			continue
		}
		var suffixes []string
		if isCommonFlow(s) {
			suffixes = []string{commonSuffixConstant}
		} else if i < len(suffixSets) {
			suffixes = suffixSets[i]
		}
		sm := SourceMeta{Source: s, SuffixList: suffixes, ReturnAll: returnAll}
		live++
		go func(src source, sm SourceMeta) {
			ch <- res{ps: runSource(ctx, src, national, intl, sm, proxyURL)}
		}(src, sm)
	}
	var out []*Profile
	for i := 0; i < live; i++ {
		out = append(out, (<-ch).ps...)
	}
	return out
}

// --- ordering / distribution helpers (mirror UPI.py) ---

func isCommonFlow(s SourceConfig) bool {
	return s.Common != nil && (s.Common.Lite || s.Common.Enriched)
}

// orderCommonFirst puts common-flow sources first, matching Python's
// `common_flow_sources + non_common_flow_sources`. enriched selects which common
// flag to check.
func orderCommonFirst(sources []SourceConfig, enriched bool) []SourceConfig {
	var common, other []SourceConfig
	for _, s := range sources {
		c := s.Common != nil && ((enriched && s.Common.Enriched) || (!enriched && s.Common.Lite))
		if c {
			common = append(common, s)
		} else {
			other = append(other, s)
		}
	}
	return append(common, other...)
}

func countCommon(sources []SourceConfig, enriched bool) int {
	n := 0
	for _, s := range sources {
		if s.Common != nil && ((enriched && s.Common.Enriched) || (!enriched && s.Common.Lite)) {
			n++
		}
	}
	return n
}

// buildSuffixSets mirrors: [[""]*commonCount] + split_list(suffixNames, nonCommon).
// The common sources each get [""] (COMMON); the non-common sources split the
// suffix list into contiguous chunks.
func buildSuffixSets(commonCount, nonCommon int, suffixNames []string) [][]string {
	sets := make([][]string, 0, commonCount+nonCommon)
	for i := 0; i < commonCount; i++ {
		sets = append(sets, []string{commonSuffixConstant})
	}
	if nonCommon > 0 {
		sets = append(sets, splitList(suffixNames, nonCommon)...)
	}
	return sets
}

func suffixNamesFor(entries []SuffixEntry, seqDesc bool) []string {
	src := entries
	if seqDesc {
		src = sortSuffixesByPriorityDesc(entries)
	}
	out := make([]string, 0, len(src))
	for _, e := range src {
		out = append(out, e.Name)
	}
	return out
}

func removeSuffixes(list []string, drop map[string]bool) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !drop[s] {
			out = append(out, s)
		}
	}
	return out
}

func removeSuffixEntries(list []SuffixEntry, drop map[string]bool) []SuffixEntry {
	out := make([]SuffixEntry, 0, len(list))
	for _, s := range list {
		if !drop[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

func repeatSuffixes(list []SuffixEntry, n int) []SuffixEntry {
	if n <= 1 || len(list) == 0 {
		return list
	}
	out := make([]SuffixEntry, 0, len(list)*n)
	for i := 0; i < n; i++ {
		out = append(out, list...)
	}
	return out
}

func toIface(ps []*Profile) []*Profile { return ps }

// last10 returns the last 10 chars (national digits) for verified-name id build.
func last10(s string) string {
	if len(s) > 10 {
		return s[len(s)-10:]
	}
	return s
}
