package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/term"
)

// A bare Escape is one byte alone at the end of a read, and `\x1b` is a prefix
// of the SGR mouse introducer `\x1b[<`: with mouse reporting on, consume must
// not hold it back as an incomplete report.

// cueRows reads the frame the terminal holds (t.prev), not the model.
func cueRows(rows []string) int {
	n := 0
	for _, r := range rows {
		if washed(r) {
			n++
		}
	}
	return n
}

func TestEscapeClearsSelectionCueOnNextFrame(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	p := newInputProbe(t, true)

	feed(t, p.in, "\x0e")
	if !p.lt.tr.selection.active {
		t.Fatal("Ctrl-N did not select a node; the fixture cannot show the bug")
	}
	if got := cueRows(p.lt.tr.prev); got == 0 {
		t.Fatal("no selection cue painted after Ctrl-N; the fixture cannot show the bug")
	}

	rest, stop := p.in.consume([]byte{0x1b})
	if stop {
		t.Fatal("bare Esc stopped the input loop")
	}
	if len(rest) != 0 {
		t.Fatalf("bare Esc was held back as %q: it will not dispatch until the next keystroke", rest)
	}
	if p.lt.tr.selection.active {
		t.Fatal("Esc did not clear the selection")
	}
	if got := cueRows(p.lt.tr.prev); got != 0 {
		t.Fatalf("selection cue still painted on %d row(s) after Esc; "+
			"the frame the terminal is holding still shows the highlight", got)
	}
}

// The incipit case (mouse reporting off) is the control the pager case must match.
func TestEscapeDispatchesWithoutASecondKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		open bool
	}{
		{"incipit", false},
		{"transcript", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newInputProbe(t, tc.open)
			rest, stop := p.in.consume([]byte{0x1b})
			if stop {
				t.Fatal("bare Esc stopped the input loop")
			}
			if len(rest) != 0 {
				t.Fatalf("bare Esc held back as %q, want it dispatched on this read", rest)
			}
		})
	}
}

// Refusing to wait on a lone `\x1b` must not break a mouse report split after
// the introducer.
func TestMouseReportStillParsesWhenSplitAfterIntroducer(t *testing.T) {
	const report = "\x1b[<64;10;3M"
	for cut := 2; cut < len(report); cut++ {
		p := newInputProbe(t, true)
		before := p.lt.tr.offset
		rest, stop := p.in.consume([]byte(report[:cut]))
		if stop {
			t.Fatalf("cut=%d: split mouse report stopped the input loop", cut)
		}
		if len(rest) == 0 {
			t.Fatalf("cut=%d: split mouse report %q was not held for the rest of the read",
				cut, report[:cut])
		}
		if _, stop := p.in.consume(append(rest, report[cut:]...)); stop {
			t.Fatalf("cut=%d: completed mouse report stopped the input loop", cut)
		}
		settle(t, p.in)
		if p.lt.tr.offset == before {
			t.Fatalf("cut=%d: wheel-up report split at %d did not scroll the pager", cut, cut)
		}
	}
}
