package handler

import "testing"

// TestParsePhone pins the flat-string phone parsing to hey-you's isValidPhone
// semantics (phonenumbers.parse(phone, "IN") over regex_phone): accept the
// Indian-mobile forms, normalize to +91<national>, reject everything else.
func TestParsePhone(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// bare 10-digit national numbers (6/7/8/9 start)
		{"9607639515", "+919607639515"},
		{"6265257963", "+916265257963"},
		{"7000000000", "+917000000000"},
		{"8000000000", "+918000000000"},
		// with country-code prefixes
		{"+919607639515", "+919607639515"},
		{"919607639515", "+919607639515"},
		{"0091-9607639515", "+919607639515"},  // hyphen separator allowed
		{"091 - 9607639515", "+919607639515"}, // spaces AROUND the hyphen allowed
		{"091-9607639515", "+919607639515"},
		{"09607639515", "+919607639515"}, // lone leading 0
		// leading/trailing whitespace tolerated (TrimSpace)
		{"  9607639515  ", "+919607639515"},
		// a BARE space after the prefix is NOT allowed (hey-you regex needs a
		// hyphen for the separator; only \s AROUND a hyphen is permitted).
		{"0091 9607639515", ""},
		// invalid -> "" (caller drops the phone section)
		{"", ""},
		{"999", ""},           // too short
		{"1234567890", ""},    // starts with 1
		{"5607639515", ""},    // starts with 5
		{"96076395150", ""},   // 11 digits
		{"+1 4155550100", ""}, // non-India
		{"abcdefghij", ""},    // non-numeric
		{"+91960763951a", ""}, // trailing junk
	}
	for _, tc := range cases {
		if got := parsePhone(tc.in); got != tc.want {
			t.Errorf("parsePhone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
