package upi

import "sort"

// aggProfile ports upi_mapper.get_agg_upi_profile: collapse the per-suffix
// profile list into a single winning profile by suffix priority.
//
// Rules (verbatim from Python):
//   - Partition profiles into true (user_exist truthy), false (user_exist ==
//     false), error (error set or user_exist nil).
//   - If any true profiles: prefer profiles that carry a name; among the
//     candidate suffixes, intersect with the configured suffix_list and pick the
//     highest-priority one; the matching true profile wins. If no configured
//     suffix matches, fall back to the first true profile whose source is a TPI
//     source. If still none, error.
//   - Else if any error profiles: error.
//   - Else if any false profiles: user_exist = false.
//   - Else (empty): error.
func aggProfile(profiles []*Profile, meta *Meta) *Profiles {
	agg := &Profiles{Meta: meta}
	suffixList := meta.SuffixList
	tpiNames := map[string]struct{}{}
	for _, s := range meta.TPISources {
		tpiNames[s.Name] = struct{}{}
	}

	var truePr, falsePr, errPr []*Profile
	for _, p := range profiles {
		if !p.hasError() {
			if p.UserExist != nil && *p.UserExist {
				truePr = append(truePr, p)
			} else if p.UserExist != nil && !*p.UserExist {
				falsePr = append(falsePr, p)
			} else {
				errPr = append(errPr, p)
			}
		} else {
			errPr = append(errPr, p)
		}
	}

	if len(truePr) > 0 {
		// Candidate suffixes = suffixes of true profiles; if any true profile has
		// a name, restrict to the named ones (Python prefers a name-bearing hit).
		candidates := truePr
		named := filterNamed(truePr)
		if len(named) > 0 {
			candidates = named
		}
		candidateSuffixes := map[string]struct{}{}
		for _, p := range candidates {
			candidateSuffixes[p.Suffix] = struct{}{}
		}

		// common_suffix_list: configured suffix entries whose name is a candidate
		// suffix; else synthesize {name, priority:1} for each candidate suffix.
		var commonSuffixList []SuffixEntry
		for _, se := range suffixList {
			if _, ok := candidateSuffixes[se.Name]; ok {
				commonSuffixList = append(commonSuffixList, se)
			}
		}
		if len(commonSuffixList) == 0 {
			for s := range candidateSuffixes {
				commonSuffixList = append(commonSuffixList, SuffixEntry{Name: s, Priority: 1})
			}
		}

		var winner *Profile
		if len(commonSuffixList) > 0 {
			// max priority; ties -> first in list order (Python filter()[0]).
			maxPri := commonSuffixList[0].Priority
			for _, se := range commonSuffixList {
				if se.Priority > maxPri {
					maxPri = se.Priority
				}
			}
			var maxSuffix string
			for _, se := range commonSuffixList {
				if se.Priority == maxPri {
					maxSuffix = se.Name
					break
				}
			}
			for _, p := range truePr {
				if p.Suffix == maxSuffix {
					winner = p
					break
				}
			}
		} else {
			for _, p := range truePr {
				if _, ok := tpiNames[p.Source]; ok {
					winner = p
					break
				}
			}
		}

		if winner != nil {
			agg.UserExist = winner.UserExist
			agg.Name = winner.Name
			agg.Source = winner.Source
			agg.Suffix = winner.Suffix
			agg.AppName = winner.AppName
		} else {
			agg.Err = true
		}
	} else if len(errPr) > 0 {
		agg.Err = true
	} else if len(falsePr) > 0 {
		f := false
		agg.UserExist = &f
	} else {
		agg.Err = true
	}
	return agg
}

func filterNamed(profiles []*Profile) []*Profile {
	var out []*Profile
	for _, p := range profiles {
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// constructVPAForProfile ports upi_mapper.construct_vpa_for_profile.
func constructVPAForProfile(phone string, p *Profile) string {
	if p.VPA != "" {
		return p.VPA
	}
	if p.Suffix != "" && p.Suffix != commonSuffixConstant {
		return phone + "@" + p.Suffix
	}
	return ""
}

// appNameFromProfile ports upi_mapper.get_app_name_from_profile.
func appNameFromProfile(p *Profile) string {
	appName := p.AppName
	if appName == "" {
		appName = otherAppName
	}
	if appName == "COMMON" && p.VPA != "" {
		suffix := suffixFromVPA(p.VPA)
		if n, ok := supportedSuffix[suffix]; ok {
			appName = n
		} else {
			appName = otherAppName
		}
	}
	return appName
}

// verifiedNamesFromProfiles ports upi_mapper.get_verified_names_from_profiles:
// one {source, name, upi_ids} entry per distinct app_name among true profiles,
// collecting the constructed VPA ids. phone is the last-10 national number.
func verifiedNamesFromProfiles(phone string, profiles []*Profile) []map[string]any {
	type entry struct {
		source string
		name   string
	}
	var order []string
	names := map[string]entry{}
	appIDs := map[string][]string{}
	seenIDs := map[string]struct{}{}

	for _, p := range profiles {
		if p.hasError() || p.UserExist == nil || !*p.UserExist {
			continue
		}
		appName := appNameFromProfile(p)
		id := constructVPAForProfile(phone, p)
		if id == "" {
			continue
		}
		if _, dup := seenIDs[id]; dup {
			continue
		}
		if _, ok := appIDs[appName]; !ok {
			order = append(order, appName)
			names[appName] = entry{source: appName, name: p.Name}
			appIDs[appName] = []string{id}
			seenIDs[id] = struct{}{}
		} else {
			// Python appends the id under the app only if not already present.
			present := false
			for _, existing := range appIDs[appName] {
				if existing == id {
					present = true
					break
				}
			}
			if !present {
				appIDs[appName] = append(appIDs[appName], id)
				seenIDs[id] = struct{}{}
			}
		}
	}

	out := make([]map[string]any, 0, len(order))
	for _, app := range order {
		e := names[app]
		vn := map[string]any{"source": e.source, "upi_ids": appIDs[app]}
		if e.name != "" {
			vn["name"] = e.name
		} else {
			vn["name"] = nil
		}
		out = append(out, vn)
	}
	return out
}

// sortSuffixesByPriorityDesc returns suffix entries sorted by priority
// descending (stable), mirroring the ENRICHED-phase sorting.
func sortSuffixesByPriorityDesc(entries []SuffixEntry) []SuffixEntry {
	cp := make([]SuffixEntry, len(entries))
	copy(cp, entries)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Priority > cp[j].Priority })
	return cp
}
