package cli

import (
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
)

// On an ambiguous-wide terminal ─ and │ are two cells; every measurement must see
// the switch.
func TestAmbiguousWideChangesEveryMeasurement(t *testing.T) {
	const rule = "─────"  // U+2500 x5, what every rule is made of
	const gutter = "  │ " // the thinking gutter

	if got := displayWidth(rule); got != 5 {
		t.Fatalf("default: rule should measure 5 cells, got %d", got)
	}
	if got := displayWidth(gutter); got != 4 {
		t.Fatalf("default: gutter should measure 4 cells, got %d", got)
	}

	setAmbiguousWide(t, true)
	if got := displayWidth(rule); got != 10 {
		t.Fatalf("ambiguous-wide: rule should measure 10 cells, got %d", got)
	}
	if got := displayWidth(gutter); got != 5 {
		t.Fatalf("ambiguous-wide: gutter should measure 5 cells, got %d", got)
	}
}

func TestAmbiguousWideReachesTheRenderer(t *testing.T) {
	setAmbiguousWide(t, true)
	md := "a sentence long enough to wrap a few times at these widths, plus a rule ───── inside it"
	for w := 30; w <= 120; w++ {
		for i, r := range nodeProseRows(thinkingNode(md), w) {
			if got := displayWidth(r); got > w {
				t.Fatalf("ambiguous-wide w=%d row %d: %d cells: %q", w, i, got, stripSGRForTest(r))
			}
		}
	}
}

func setAmbiguousWide(t *testing.T, on bool) {
	t.Helper()
	prev := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = on
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = prev })
}

func thinkingNode(md string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
}
