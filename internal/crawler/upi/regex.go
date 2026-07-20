package upi

import "regexp"

// upiFormatRe mirrors is_upi_format_correct: `\d{10}(-[^@]+)?@.*` — 10 digits,
// an optional "-<affix>" before '@', then a handle.
var upiFormatRe = regexp.MustCompile(`^\d{10}(-[^@]+)?@.*`)
