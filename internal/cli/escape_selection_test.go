package cli

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Escape clears the selection cue on the VERY NEXT FRAME.
//
// The shipped bug: Esc dropped the selection but the highlight stayed painted
// until the user scrolled. The cause was not in the pager at all — dispatch()
// always renders, and the cue is applied at paint time (entryLine ->
// decorateNodeRow), so a composed frame would have dropped it. The Esc BYTE
// never reached dispatch.
//
// With the pager up, mouse reporting is enabled and consume() runs
// ldmouse.Parse first. `\x1b` is a prefix of the SGR mouse introducer
// `\x1b[<`, so Parse claimed the read was incomplete; consume stashed the byte
// in `pending` and ended the frame. A bare Escape is always exactly one byte
// at the end of a read, so Esc NEVER dispatched on its own frame — it
// dispatched on whatever key came next, which is why "navigate the page" was
// the thing that appeared to clear the cue.
//
// These tests pin the byte-level guarantee (nothing held back) AND the pixel
// the user complained about (no cue in the painted frame).
// ---------------------------------------------------------------------------

// cueRows counts painted rows carrying the selection gutter bar. It reads the
// frame the terminal is actually holding (t.prev), not the model, because the
// bug was invisible in the model: t.selection was already empty.
func cueRows(rows []string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r, "▎") {
			n++
		}
	}
	return n
}

func TestEscapeClearsSelectionCueOnNextFrame(t *testing.T) {
	p := newInputProbe(t, true) // pager up: mouse reporting on, Esc bound

	// Ctrl-N selects the first node.
	feed(t, p.in, "\x0e")
	if !p.lt.tr.selection.active {
		t.Fatal("Ctrl-N did not select a node; the fixture cannot show the bug")
	}
	if got := cueRows(p.lt.tr.prev); got == 0 {
		t.Fatal("no selection cue painted after Ctrl-N; the fixture cannot show the bug")
	}

	// A bare Escape, delivered the way a terminal delivers one: a single byte,
	// alone, at the end of a read.
	rest, stop := p.in.consume([]byte{0x1b})
	if stop {
		t.Fatal("bare Esc stopped the input loop")
	}
	if len(rest) != 0 {
		t.Fatalf("bare Esc was held back as %q — it will not dispatch until the next keystroke", rest)
	}
	if p.lt.tr.selection.active {
		t.Fatal("Esc did not clear the selection")
	}
	if got := cueRows(p.lt.tr.prev); got != 0 {
		t.Fatalf("selection cue still painted on %d row(s) after Esc; "+
			"the frame the terminal is holding still shows the highlight", got)
	}
}

// TestEscapeDispatchesWithoutASecondKey is the same guarantee stated at the
// byte level, in both pager states. The incipit case never regressed (mouse
// reporting is off there, so ldmouse.Parse was not consulted) and is kept as
// the control: it is what the pager case is supposed to match.
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

// TestMouseReportStillParsesWhenSplitAfterIntroducer guards the trade the fix
// makes: refusing to wait on a lone `\x1b` must not break a genuine mouse
// report that arrives split anywhere from the introducer onward.
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
