package breach

import (
	"sort"
	"strconv"
	"strings"

	"github.com/sign3labs/go-you/internal/model"
)

// This file ports the phone-breach path from pawn/pawn_service.py
// (get_breach_details + get_leaks_by_type) and its lookup tables
// (pawn/source_mapping.py). Under the no-cloud constraint the static persona
// document comes from MySQL (internal/staticdata) rather than DynamoDB, but the
// matching + attribute-mapping logic is identical.

// sourceMapping maps a raw static-data source key -> the client-facing breach
// name. Copied verbatim from pawn/source_mapping.py source_mapping.
var sourceMapping = map[string]string{
	"amazon":                           "Amazon",
	"Apollo":                           "Apollo",
	"book_my_show":                     "BookMyShow",
	"car_owners":                       "CarOwner",
	"club_mahindra":                    "ClubMahindra",
	"commodity_traders":                "EquityTrader",
	"cred":                             "Cred",
	"demat_account_holders":            "EquityTrader",
	"demat_holders_chandigarh":         "EquityTrader",
	"demat_holders":                    "EquityTrader",
	"demat_holders_mobile_and_city":    "EquityTrader",
	"demat_holders_mobile_numbers":     "EquityTrader",
	"dream11":                          "Dream11",
	"dunzo":                            "Dunzo",
	"facebook":                         "Facebook",
	"flipkart":                         "Flipkart",
	"frequent_flyers":                  "FrequentFlyers",
	"fun88":                            "Fun88",
	"gujarat_traders":                  "EquityTrader",
	"hni_database":                     "HNI",
	"icici_credit_card_holders":        "CreditCardHolder",
	"india_infoline_traders":           "EquityTrader",
	"india_mart_2":                     "IndiaMart",
	"india_mart":                       "IndiaMart",
	"instagram":                        "Instagram",
	"linkedin_data":                    "Linkedin",
	"livpure":                          "Livpure",
	"money_control":                    "MoneyControl",
	"mutual_funds_data":                "EquityTrader",
	"netflix":                          "Netflix",
	"pan_card_holders":                 "PanCardHolders",
	"paytm":                            "Paytm",
	"people_data_labs":                 "PeopleDataLabs",
	"phonepe":                          "Phonepe",
	"prop_tiger":                       "PropTiger",
	"rca_delhi":                        "RCA",
	"sharekhan_demat":                  "ShareKhan",
	"shein":                            "Shein",
	"stock_traders":                    "EquityTrader",
	"traders_database":                 "EquityTrader",
	"truecaller_2_andhra_pradesh":      "Truecaller",
	"truecaller_2_assam":               "Truecaller",
	"truecaller_2_bihar_and_jharkhand": "Truecaller",
	"truecaller_2_bihar":               "Truecaller",
	"truecaller_2_chennai":             "Truecaller",
	"truecaller_2_delhi":               "Truecaller",
	"truecaller_2_gujarat":             "Truecaller",
	"truecaller_2_haryana":             "Truecaller",
	"truecaller_2_himachal_pradesh":    "Truecaller",
	"truecaller_2_jammu_and_kashmir":   "Truecaller",
	"truecaller_2_karnataka":           "Truecaller",
	"truecaller_2_kerala":              "Truecaller",
	"truecaller_2_kolkata":             "Truecaller",
	"truecaller_2_madhya_pradesh_and_chhattisgarh": "Truecaller",
	"truecaller_2_madhya_pradesh":                  "Truecaller",
	"truecaller_2_maharashtra":                     "Truecaller",
	"truecaller_2_north_east":                      "Truecaller",
	"truecaller_2_orissa":                          "Truecaller",
	"truecaller_2_punjab":                          "Truecaller",
	"truecaller_2_rajasthan":                       "Truecaller",
	"truecaller_2_tamilnadu":                       "Truecaller",
	"truecaller_2_unknown":                         "Truecaller",
	"truecaller_andhra_pradesh":                    "Truecaller",
	"truecaller_assam":                             "Truecaller",
	"truecaller_bihar":                             "Truecaller",
	"truecaller_chennai":                           "Truecaller",
	"truecaller_delhi":                             "Truecaller",
	"truecaller_gujarat":                           "Truecaller",
	"truecaller_haryana":                           "Truecaller",
	"truecaller_himachal_pradesh":                  "Truecaller",
	"truecaller_jammu_and_kashmir":                 "Truecaller",
	"truecaller_karnataka":                         "Truecaller",
	"truecaller_kerala":                            "Truecaller",
	"truecaller_kolkata":                           "Truecaller",
	"truecaller_kolkata_metro":                     "Truecaller",
	"truecaller_madhya_pradesh":                    "Truecaller",
	"truecaller_maharashtra":                       "Truecaller",
	"truecaller_mumbai":                            "Truecaller",
	"truecaller_mumbai_metro":                      "Truecaller",
	"truecaller_north_east":                        "Truecaller",
	"truecaller_orissa":                            "Truecaller",
	"truecaller_punjab":                            "Truecaller",
	"truecaller_rajasthan":                         "Truecaller",
	"truecaller_tamilnadu":                         "Truecaller",
	"truecaller_unknown":                           "Truecaller",
	"youtube":                                      "Youtube",
	"zomato_2":                                     "Zomato",
	"zomato":                                       "Zomato",
	"zoomcar":                                      "Zoomcar",
	"linkedin":                                     "Linkedin",
	"ixigo":                                        "Ixigo",
}

// attributesMapping maps a raw leaked attribute key -> the client-facing data
// label. Copied verbatim from pawn/source_mapping.py attributes_mapping.
var attributesMapping = map[string]string{
	"name":    "Name",
	"gender":  "Gender",
	"email":   "Email",
	"address": "Address",
}

// phoneBreachFromStatic ports PawnService.get_breach_details for PHONE.
// static is the decoded static persona document ({source: [{"payload": {...}}]}),
// loginID is country_code+national_number (e.g. "916265257963"). breachDates is
// the tenant/global breach_date_per_source config (source-lower -> "YYYY-MM").
// Returns (breaches, status) where status is "found" iff any breach matched.
func phoneBreachFromStatic(static map[string]any, loginID string, breachDates map[string]string) ([]model.Breach, string) {
	matched := leaksByType(static, loginID, "primary_ph")
	breaches := buildBreaches(matched, breachDates)
	if len(breaches) > 0 {
		return breaches, "found"
	}
	return breaches, "not-found"
}

// leaksByType ports get_leaks_by_type: for each source present in both the
// static doc and source_mapping, find the first payload whose matchKey
// (primary_ph / primary_email) equals loginID, and keep {mappedSource -> payload}.
// The mapped source is the *value* from source_mapping; matches with the same
// mapped name collapse (last-write-wins, mirroring the Python dict assignment).
func leaksByType(static map[string]any, loginID, matchKey string) map[string]map[string]any {
	out := map[string]map[string]any{}
	if static == nil {
		return out
	}
	for src, v := range static {
		mapped, ok := sourceMapping[src]
		if !ok {
			continue
		}
		payloads := asPayloadList(v)
		for _, pw := range payloads {
			payload, ok := pw["payload"].(map[string]any)
			if !ok {
				continue
			}
			if payloadStr(payload[matchKey]) == loginID {
				out[mapped] = payload
				break
			}
		}
	}
	return out
}

// buildBreaches ports the breach-assembly half of get_breach_details: per matched
// source, collect the mapped attribute labels present in its payload; if any,
// emit {name, data, date}. Output is sorted by name for deterministic responses
// (Python iterates a dict; go map order is random, so we stabilise it).
func buildBreaches(matched map[string]map[string]any, breachDates map[string]string) []model.Breach {
	breaches := make([]model.Breach, 0, len(matched))
	for src, payload := range matched {
		labels := leakedAttrLabels(payload)
		if len(labels) == 0 {
			continue
		}
		date := "NA"
		if breachDates != nil {
			if d, ok := breachDates[strings.ToLower(src)]; ok {
				date = d
			}
		}
		breaches = append(breaches, model.Breach{
			Name: src,
			Data: strings.Join(labels, " ,"),
			Date: date,
		})
	}
	sort.Slice(breaches, func(i, j int) bool { return breaches[i].Name < breaches[j].Name })
	return breaches
}

// leakedAttrLabels returns the deduplicated, sorted client labels for the leaked
// attributes present in a payload (Python uses a set; we sort for determinism).
func leakedAttrLabels(payload map[string]any) []string {
	seen := map[string]struct{}{}
	for attr := range payload {
		if label, ok := attributesMapping[attr]; ok {
			seen[label] = struct{}{}
		}
	}
	labels := make([]string, 0, len(seen))
	for l := range seen {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels
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

// payloadStr renders a payload id value as the string used for matching. JSON
// numbers decode to float64; a phone stored as a number must compare as its
// integer string (e.g. 916265257963.0 -> "916265257963").
func payloadStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// A whole number decoded from JSON prints without a fractional part, the
		// way Python's str(int(...)) path would (e.g. 916265257963 not ...963.0).
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return ""
	}
}
