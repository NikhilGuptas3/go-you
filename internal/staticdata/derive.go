package staticdata

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file ports the pure static-data derivations go-you needs from the Python
// intelligence layer: digital_age (intelligence/digital_age.py get_phone_age /
// get_email_age) and linked-id resolution (you_service_aggregator.get_linked_id).
// Both operate on the decoded static persona document; neither performs any I/O.

// date_keys_list / phone_keys_list / email_keys_list from digital_age.py.
var (
	dateKeys  = map[string]struct{}{"domain_create_date": {}, "car_registration_date": {}, "leak_date": {}, "dunzo_join_date": {}}
	phoneKeys = map[string]struct{}{"whatsapp_mobile": {}, "mobile": {}, "mobile_2": {}, "primary_ph": {}, "mobile_4": {}, "mobile_3": {}, "mobile_1": {}}
	emailKeys = map[string]struct{}{"email": {}, "primary_email": {}, "work_email": {}, "email_3": {}, "email_2": {}}
)

// DigitalAge is the client digital_age block. When Error is set the map form is
// {"error": true}; otherwise {"year": <int>, "month": <int>, "age": <int>}.
// Ported from intelligence/digital_age.py DigitalAge.
type DigitalAge struct {
	Year  int
	Month int
	Age   int
	Error bool
}

// Map renders the DigitalAge into the loose map the response carries. Mirrors the
// serialized DigitalAge: error -> {"error": true}; else {year, month, age}.
//
// NOTE on the "8+" bucket: prod responses sometimes show age as a string like
// "8+". That bucketing is applied DOWNSTREAM (ml_service / formatting), not in
// DigitalAge itself — the raw DigitalAge.age is an integer (relativedelta years,
// calculate_diff_in_years). go-you emits the integer age here; if a string bucket
// is ever required it must be added at the same downstream layer, not here.
func (d DigitalAge) Map() map[string]any {
	if d.Error {
		return map[string]any{"error": true}
	}
	return map[string]any{"year": d.Year, "month": d.Month, "age": d.Age}
}

// PhoneDigitalAge ports get_phone_age. Dates are collected from the static-data
// payloads (date_keys_list) and, when present, the phone_meta revocation date
// (revocations.total_revocations > 0 -> datetime(year, month, 1)). The earliest
// qualifying date yields {year, month, age}; no dates -> {error:true}.
//
// The SKYPE creation-time date source (an organic crawler) is intentionally
// omitted: SKYPE is a token-pool crawler that go-you does not run, so it can
// never contribute a date. revocations is the phone_meta.revocations map (may be
// nil). now is injected for testability (pass time.Now()).
func PhoneDigitalAge(static map[string]any, revocations map[string]any, now time.Time) DigitalAge {
	dates := staticSeenDates(static)

	var latestRevocation *time.Time
	if revocations != nil {
		if total := toInt(revocations["total_revocations"]); total > 0 {
			y := toInt(revocations["year"])
			m := toInt(revocations["month"])
			if y > 0 && m >= 1 && m <= 12 {
				t := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
				latestRevocation = &t
			}
		}
	}
	return digitalAge(dates, latestRevocation, now)
}

// EmailDigitalAge ports get_email_age. Dates come from verified breach dates
// (IsVerified == true). SKYPE is omitted for the same reason as PhoneDigitalAge.
// breaches is the list of breach maps from breach_details.breaches (may be nil).
func EmailDigitalAge(breaches []map[string]any, now time.Time) DigitalAge {
	var dates []string
	for _, b := range breaches {
		if v, _ := b["IsVerified"].(bool); v {
			if d, ok := b["date"].(string); ok && d != "" {
				dates = append(dates, d)
			}
		}
	}
	return digitalAge(dates, nil, now)
}

// staticSeenDates ports get_required_static_features -> phone_seen_dates: every
// value under a date_keys_list key across all payloads.
func staticSeenDates(static map[string]any) []string {
	var out []string
	for _, v := range static {
		for _, pw := range asPayloadList(v) {
			payload, ok := pw["payload"].(map[string]any)
			if !ok {
				continue
			}
			for k, val := range payload {
				if _, isDate := dateKeys[k]; isDate {
					if s, ok := val.(string); ok && s != "" {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

// digitalAge ports get_digital_age: parse date strings, optionally clamp to the
// latest revocation date, take the earliest, and build the DigitalAge. No dates
// -> error.
func digitalAge(dates []string, latestRevocation *time.Time, now time.Time) DigitalAge {
	clean := make([]time.Time, 0, len(dates))
	for _, d := range dates {
		if t, ok := cleanDate(d); ok {
			clean = append(clean, t)
		}
	}
	if latestRevocation != nil {
		filtered := clean[:0:0]
		for _, t := range clean {
			if !t.Before(*latestRevocation) {
				filtered = append(filtered, t)
			}
		}
		filtered = append(filtered, *latestRevocation)
		clean = filtered
	}
	if len(clean) == 0 {
		return DigitalAge{Error: true}
	}
	earliest := clean[0]
	for _, t := range clean[1:] {
		if t.Before(earliest) {
			earliest = t
		}
	}
	return DigitalAge{
		Year:  earliest.Year(),
		Month: int(earliest.Month()),
		Age:   yearsBetween(earliest, now),
	}
}

// cleanDate ports intelligence/utils.clean_date (default format branch): a string
// >= 10 chars parsed as YYYY-MM-DD, falling back to DD-MM-YYYY. Whitespace around
// separators is stripped first.
func cleanDate(s string) (time.Time, bool) {
	s = collapseDashSpaces(strings.TrimSpace(s))
	if len(s) < 10 {
		return time.Time{}, false
	}
	head := s[:10]
	if t, err := time.Parse("2006-01-02", head); err == nil {
		return t, true
	}
	if t, err := time.Parse("02-01-2006", head); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// collapseDashSpaces mirrors re.sub(r"\s*-\s*", "-", s): remove spaces around any
// hyphen.
func collapseDashSpaces(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '-' {
			// trim trailing spaces already written
			out := strings.TrimRight(b.String(), " \t")
			b.Reset()
			b.WriteString(out)
			b.WriteRune('-')
			// skip following spaces
			for i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\t') {
				i++
			}
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// yearsBetween ports calculate_diff_in_years (relativedelta(end, start).years):
// whole years elapsed from start to end.
func yearsBetween(start, end time.Time) int {
	years := end.Year() - start.Year()
	// subtract one if the anniversary hasn't occurred yet this year
	if end.Month() < start.Month() || (end.Month() == start.Month() && end.Day() < start.Day()) {
		years--
	}
	if years < 0 {
		years = 0
	}
	return years
}

// LinkedIDs ports get_linked_id's id_sets extraction (the only part go-you needs:
// it has no Cashfree response and does not crawl linked data). For a PHONE
// primary it collects primary_email values; for EMAIL it collects primary_ph.
// It scans the first 10 payloads across all sources, strips a trailing ".0" from
// numeric-looking ids, and dedups preserving first-seen order.
func LinkedIDs(static map[string]any, kind Kind) []string {
	if static == nil {
		return nil
	}
	keyName := "primary_ph" // EMAIL primary -> linked phones
	if kind == KindPhone {
		keyName = "primary_email" // PHONE primary -> linked emails
	}

	// Iterate sources in a stable order so the first-10 window is deterministic
	// (Python iterates the doc's key order; go map order is random, so sort).
	srcKeys := make([]string, 0, len(static))
	for k := range static {
		srcKeys = append(srcKeys, k)
	}
	sort.Strings(srcKeys)

	var ids []string
	seen := map[string]struct{}{}
	iter := 0
	for _, src := range srcKeys {
		for _, pw := range asPayloadList(static[src]) {
			payload, ok := pw["payload"].(map[string]any)
			if !ok {
				continue
			}
			if v, present := payload[keyName]; present && v != nil {
				if iter < 10 {
					val := idString(v)
					if val != "" {
						if _, dup := seen[val]; !dup {
							seen[val] = struct{}{}
							ids = append(ids, val)
						}
					}
				}
				iter++
			}
		}
	}
	return ids
}

// idString renders a linked-id value as a string, stripping a trailing ".0"
// (Python: if ".0" in str(v): v.split(".0")[0]).
func idString(v any) string {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case float64:
		s = strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
	if idx := strings.Index(s, ".0"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// toInt coerces a JSON-decoded numeric/string value to int (0 on failure).
func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	}
	return 0
}

// asPayloadList coerces a static-doc source value into a list of maps, tolerating
// both []map[string]any and the []any-of-maps shape a JSON round-trip produces.
func asPayloadList(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}
