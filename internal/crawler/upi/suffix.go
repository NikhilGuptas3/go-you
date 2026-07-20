package upi

import "strings"

// commonSuffixConstant and otherAppName mirror upi_util.py.
const (
	commonSuffixConstant = "COMMON"
	otherAppName         = "UNKNOWN"
)

// upiApp is one entry of the UPIApps enum (upi_util.py): an app name, the bank
// suffixes it owns, and its analytics encoding.
type upiApp struct {
	appName  string
	suffixes []string
	encoding string
}

// upiApps mirrors upi_util.py UPIApps verbatim (order preserved). supported_suffix
// and app_name_encoding are derived from it below.
var upiApps = []upiApp{
	{"PHONEPE", []string{"ybl", "axl", "ibl"}, "a1"},
	{"PAYTM", []string{"paytm", "ptyes", "ptaxis", "pthdfc", "ptsbi"}, "a2"},
	{"BHIM", []string{"bhim"}, "a3"},
	{"GPAY", []string{"okicici", "okhdfcbank", "oksbi", "okaxis"}, "a4"},
	{"AMAZON_PAY", []string{"apl", "yapl", "rapl", "amazonpay"}, "a5"},
	{"SLICE_PAY", []string{"sliceaxis"}, "a6"},
	{"WHATSAPP_UPI", []string{"waicici", "icici", "waaxis", "wahdfcbank", "wasbi"}, "a7"},
	{"CRED_UPI", []string{"axisb", "yescred"}, "a8"},
	{"ADITYA_BIRLA_PAY", []string{"abcdicici"}, "a9"},
	{"BAJAJ_FINSERV", []string{"abfspay"}, "a10"},
	{"BHARAT_PAY", []string{"bpunity"}, "a11"},
	{"CURIE_MONEY", []string{"yescurie"}, "a12"},
	{"FAMAPP", []string{"yesfam"}, "a13"},
	{"FI_MONEY", []string{"fifederal"}, "a14"},
	{"FLIPKART_UPI", []string{"fkaxis"}, "a15"},
	{"FREO", []string{"freoicici"}, "a16"},
	{"GENWISE", []string{"gwaxis"}, "a17"},
	{"GROW", []string{"yesg"}, "a18"},
	{"IND_MONEY", []string{"inhdfc"}, "a19"},
	{"JUPITER_MONEY", []string{"jupiteraxis"}, "a20"},
	{"KIWI", []string{"goaxb"}, "a21"},
	{"KREDIT_PE", []string{"kphdfc"}, "a22"},
	{"MOBIKWIK", []string{"ikwik"}, "a23"},
	{"MONEY_VIEW", []string{"mvhdfc"}, "a24"},
	{"NAVI", []string{"naviaxis"}, "a25"},
	{"NIYO", []string{"niyoicici"}, "a26"},
	{"ONE_CARD", []string{"oneyes"}, "a27"},
	{"OK_CREDIT", []string{"axb"}, "a28"},
	{"POP_UPI", []string{"yespop"}, "a29"},
	{"RIO_MONEY", []string{"rmrbl"}, "a30"},
	{"SAMSUNG_PAY", []string{"pingpay"}, "a31"},
	{"SALARY_SE", []string{"seyes"}, "a32"},
	{"SHRIRAM_ONE", []string{"shriramhdfcbank"}, "a33"},
	{"SLICE", []string{"sliceaxis"}, "a34"},
	{"SUPER_MONEY", []string{"superyes"}, "a35"},
	{"TATA_NEU", []string{"tapicici"}, "a36"},
	{"TIME_PAY", []string{"timecosmos"}, "a37"},
	{"T_WALLET", []string{"axisbank"}, "a38"},
	{"TWID_PAY", []string{"yestp"}, "a39"},
	{"ULTRACASH", []string{"idfcbank"}, "a40"},
}

// supportedSuffix maps a bank suffix -> app name (upi_util.py supported_suffix).
// Includes the COMMON->COMMON identity entry. Later apps overwrite earlier ones
// on suffix collision, matching the Python dict-build order (e.g. "sliceaxis"
// resolves to SLICE, the later entry).
var supportedSuffix = func() map[string]string {
	m := map[string]string{commonSuffixConstant: commonSuffixConstant}
	for _, a := range upiApps {
		for _, s := range a.suffixes {
			m[s] = a.appName
		}
	}
	return m
}()

// appNameEncoding maps app name -> analytics encoding (app_name_encoding).
var appNameEncoding = func() map[string]string {
	m := map[string]string{commonSuffixConstant: otherAppName}
	for _, a := range upiApps {
		m[a.appName] = a.encoding
	}
	return m
}()

// appNameForSuffix returns the app name for a suffix, defaulting to UNKNOWN
// (other_app_name), matching supported_suffix.get(suffix, other_app_name).
func appNameForSuffix(suffix string) string {
	if suffix == commonSuffixConstant {
		return commonSuffixConstant
	}
	if n, ok := supportedSuffix[suffix]; ok {
		return n
	}
	return otherAppName
}

// isSupportedSuffix reports whether a suffix is known (used to intersect the
// requested suffix_list with supported ones, as upi_base does).
func isSupportedSuffix(suffix string) bool {
	_, ok := supportedSuffix[suffix]
	return ok
}

// flowForSuffix mirrors get_flow_for_suffix.
func flowForSuffix(suffix string) string {
	if suffix == commonSuffixConstant {
		return commonSuffixConstant
	}
	return "NON_COMMON"
}

// suffixFromVPA mirrors get_suffix_from_vpa.
func suffixFromVPA(vpa string) string {
	if vpa == "" {
		return ""
	}
	if i := strings.Index(vpa, "@"); i >= 0 {
		return vpa[i+1:]
	}
	return ""
}

// splitList mirrors upi_util.split_list: split a into n contiguous chunks whose
// sizes differ by at most one (numpy array_split semantics). Returns n slices
// (some possibly empty when len(a) < n).
func splitList(a []string, n int) [][]string {
	out := make([][]string, 0, n)
	if n <= 0 {
		return out
	}
	k := len(a) / n
	m := len(a) % n
	for i := 0; i < n; i++ {
		start := i*k + min(i, m)
		end := (i+1)*k + min(i+1, m)
		out = append(out, a[start:end])
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
