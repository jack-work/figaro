package render

import "testing"

// '?' is a CSI parameter byte: "\x1b[?25l" must not leak "25l".
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
