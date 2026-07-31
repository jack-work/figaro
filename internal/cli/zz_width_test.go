package cli

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

// Does any width function UNDERCOUNT? An undercount is the only arithmetic that
// can put text past the edge: the clip believes the row fits when it does not.
func TestProbeWidthHonesty(t *testing.T) {
	cases := map[string]string{
		"plain":        "hello world",
		"sgr":          "\x1b[31mred\x1b[0m text",
		"truncated":    "\x1b[38;5; text after truncated csi",
		"bare-esc":     "\x1b text after a bare esc",
		"osc":          "\x1b]0;title\x07 text after osc",
		"osc-st":       "\x1b]0;title\x1b\\ text after osc st",
		"dcs":          "\x1bPq payload \x1b\\ text after dcs",
		"ss3":          "\x1bOP text after ss3",
		"charset":      "\x1b(B text after charset",
		"private":      "\x1b[?25l text after private mode",
		"intermediate": "\x1b[?25$p text after intermediate",
		"wide":         "日本語 text",
		"emoji":        "🎉 text",
	}
	for name, s := range cases {
		// truth: strip escapes with the render package's scanner, then measure
		truth := runewidth.StringWidth(stripAllEsc(s))
		got := displayWidth(s)
		flag := ""
		if got < truth {
			flag = "  <-- UNDERCOUNT: a clip will let this past the edge"
		} else if got > truth {
			flag = "  (overcount: clips early, loses text)"
		}
		t.Logf("%-13s displayWidth=%3d truth=%3d%s", name, got, truth, flag)
	}
}

func stripAllEsc(s string) string {
	out := make([]rune, 0, len(s))
	r := []rune(s)
	for i := 0; i < len(r); {
		if r[i] == 0x1b {
			// consume conservatively: to the first alpha for CSI-ish, else 2
			if i+1 < len(r) && r[i+1] == '[' {
				i += 2
				for i < len(r) && !((r[i] >= 'A' && r[i] <= 'Z') || (r[i] >= 'a' && r[i] <= 'z')) {
					i++
				}
				i++
				continue
			}
			if i+1 < len(r) && (r[i+1] == ']' || r[i+1] == 'P') {
				i += 2
				for i < len(r) && r[i] != 0x07 && !(r[i] == 0x1b && i+1 < len(r) && r[i+1] == '\\') {
					i++
				}
				if i < len(r) && r[i] == 0x07 {
					i++
				} else {
					i += 2
				}
				continue
			}
			i += 2
			continue
		}
		out = append(out, r[i])
		i++
	}
	return string(out)
}
