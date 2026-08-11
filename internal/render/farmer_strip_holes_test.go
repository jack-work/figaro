package render

import "testing"

// TestStripEscapesLeavesNoBody: an escape's BODY is what makes this a rendering
// defect and not merely a safety one: glamour prints the body, having wrapped
// as though the whole sequence were nothing, so a row comes back wider than the
// width it was given with "[31m" (or "1049h") on it.
//
// Every case here FAILS at b583dfa. The private-mode ones are the important
// ones: '?' is not in isCSIParamByte, so the param loop stops at once and the
// body leaks, and "\x1b[?25l" / "\x1b[?1049h" are the two escapes any pasted
// tool transcript carries. SanitizeForTerminal handles '?' explicitly; this
// function forgot it.
func TestStripEscapesLeavesNoBody(t *testing.T) {
	cases := map[string]string{
		"private mode (cursor hide)": "a\x1b[?25lb",
		"private mode (alt screen)":  "a\x1b[?1049hb",
		"private mode (paste mode)":  "a\x1b[?2004lb",
		"CSI with intermediate":      "a\x1b[?25$pb",
		"DCS to ST":                  "a\x1bPq#0;1;0\x1b\\b",
		"APC to ST":                  "a\x1b_payload here\x1b\\b",
		"PM to ST":                   "a\x1b^privmsg\x1b\\b",
		"SOS to ST":                  "a\x1bXstring\x1b\\b",
		"SS3":                        "a\x1bOPb",
		"charset designator":         "a\x1b(Bb",
	}
	for name, in := range cases {
		if got := StripEscapes(in); got != "ab" {
			t.Errorf("%s: StripEscapes(%q) = %q, want %q", name, in, got, "ab")
		}
	}
}
