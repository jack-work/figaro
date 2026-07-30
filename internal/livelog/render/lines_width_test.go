package render

import (
	"regexp"
	"testing"

	"github.com/mattn/go-runewidth"
)

// THE COMPLAINT: markdown thinking content — and bash tool output — rendered
// "one or two characters beyond the right edge". In nvim's terminal with
// nowrap the overflow is hidden and obscures the GUI; in tmux it wraps and
// breaks up the UI. Two terminal policies, one defect.
//
// MEASURED before the fix, in this package and then end to end in a real pty:
//
//	clip(width=40) on one line of Japanese ....... 56 cells  (+16)
//	clip(width=40) on a line with ONE wide rune .. 41 cells  (+1)
//	clip(width=20) on one line of Japanese ....... 40 cells  (+20)
//	inline stream, 60-column tmux pane ........... 72 cells  (+12)
//
// "One or two characters" is one or two WIDE RUNES: clip and hardWrap counted
// runes, and a CJK ideograph or emoji occupies two columns.
//
// NOTE ON MEASUREMENT, because it cost a wrong answer here first: measure the
// output with the ANSI stripped. runewidth.StringWidth counts the BYTES of an
// SGR run as columns, so measuring clip()'s return directly reports +3 on
// pure ASCII — the reset escape clip appends when it cuts. That is the
// instrument lying, not the code; the same trap the footer path hit.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]`)

func cellsOf(s string) int { return runewidth.StringWidth(ansiRe.ReplaceAllString(s, "")) }

func TestClipCountsCellsNotRunes(t *testing.T) {
	cases := []struct{ name, s string }{
		{"ascii", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz"},
		{"cjk", "日本語のテキストはここにあります日本語のテキストはここにあります"},
		{"emoji", "🙂🙂🙂 status ok 🙂🙂🙂 and some trailing prose to overrun the edge"},
		{"one-wide-rune", "plain ascii text with one 日 wide rune in the middle of it all"},
		{"wide-at-boundary", "12345678901234567890123456789012345678日 trailing"},
		{"box-drawing", "│ ─────────────────────── thinking gutter ────────────────────"},
		{"styled", "\x1b[2m日本語 dim text that runs well past any narrow width\x1b[0m"},
	}
	for _, w := range []int{8, 20, 40, 60, 80} {
		for _, c := range cases {
			got := cellsOf(clip(c.s, w))
			if got > w {
				t.Errorf("clip(%s, %d) emitted %d cells (+%d) — it must never exceed the viewport",
					c.name, w, got, got-w)
			}
			// And it must still fill the row when there is content for it: a
			// clip that under-fills would be the opposite bug (wrapping early),
			// which is what counting escape bytes as columns would cause.
			// A wide rune straddling the boundary legitimately leaves one cell.
			if full := cellsOf(c.s); full >= w && got < w-1 {
				t.Errorf("clip(%s, %d) emitted only %d cells — clipping too early", c.name, w, got)
			}
		}
	}
}

func TestHardWrapCountsCellsNotRunes(t *testing.T) {
	for _, w := range []int{8, 20, 40, 60} {
		for _, s := range []string{
			"日本語のテキストはここにあります日本語のテキストはここにあります日本語",
			"mixed 日本語 and ascii 混在 text that keeps going past the edge",
			"🙂 emoji lead then a long ascii tail to force several wrapped rows here",
			"plain ascii with no wide runes at all, wrapped across several rows",
		} {
			for i, row := range hardWrap(s, w) {
				if got := cellsOf(row); got > w {
					t.Errorf("hardWrap(%d) row %d emitted %d cells (+%d): %q", w, i, got, got-w, row)
				}
			}
		}
	}
}

// Explicit newlines are paragraph boundaries and must survive the change.
func TestHardWrapKeepsExplicitNewlines(t *testing.T) {
	rows := hardWrap("alpha\n\nbeta", 40)
	if len(rows) != 3 || rows[0] != "alpha" || rows[1] != "" || rows[2] != "beta" {
		t.Fatalf("paragraph structure lost: %q", rows)
	}
}

// A zero-width combining mark must not consume a column, or accented text
// would clip early — the mirror of the bug being fixed.
func TestClipCombiningMarksCostNothing(t *testing.T) {
	// "e" + combining acute, ten times: ten columns, twenty runes.
	s := ""
	for range 10 {
		s += "e\u0301"
	}
	if got := cellsOf(clip(s, 10)); got != 10 {
		t.Fatalf("clip of ten combined glyphs at width 10 emitted %d cells, want 10", got)
	}
}
