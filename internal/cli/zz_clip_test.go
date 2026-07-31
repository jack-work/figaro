package cli

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

// The consequence: does a CLIPPED row actually exceed the width it was clipped to?
func TestProbeClipOverflows(t *testing.T) {
	cases := map[string]string{
		"charset":  "\x1b(B abcdefghijklmnopqrstuvwxyz",
		"bare-esc": "\x1b abcdefghijklmnopqrstuvwxyz",
		"plain":    "abcdefghijklmnopqrstuvwxyz",
	}
	for name, s := range cases {
		for _, w := range []int{10, 20} {
			got := clipToWidth(s, w)
			real := runewidth.StringWidth(stripAllEsc(got))
			verdict := "ok"
			if real > w {
				verdict = "PAST THE EDGE"
			}
			t.Logf("%-9s clip to %2d -> %2d visible cells  %s  %q", name, w, real, verdict, stripAllEsc(got))
		}
	}
}
