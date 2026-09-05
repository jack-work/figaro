package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// A cold Ctrl-N/Ctrl-P seeds from the VIEWPORT and does not scroll.
//
// It used to seed from the ends of the RETAINED WINDOW: len(refs)-1 for ^P,
// 0 for ^N: which is a different thing entirely: the window holds far more
// than the screen shows. ensureSelectionVisible then dragged the page to
// wherever that landed.
//
// Measured on the shipped binary (tmux 100x40, aria b4222044, 70 lines of line
// space, body 38, pager chrome asserted present):
//
//	^P parked at the TOP  (1-38/70): jumped to 32-69/70, cue nowhere on screen
//	^N parked at the TAIL (34-70/70): jumped to 3-40/70, cue on the FIRST node
//	^N parked at the TOP  (1-38/70): correct, and this is the trap. At the top
//	  of the window "first in window" and "topmost visible" are the same node,
//	  so it is the one starting position where the defect is invisible.
//
// The no-scroll assertion is the one that decides the test; which block gets the cue
// follows from it.
// ---------------------------------------------------------------------------

func seedFixture(t *testing.T) *transcript {
	t.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	parts := make([]aria.TurnPart, 0, 12)
	for i := 1; i <= 12; i++ {
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: uint64(i), Inquiry: "question", Sealed: true,
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: "answer one"},
				{Type: livedoc.NodeProse, Markdown: "answer two"},
			},
		}})
	}
	client.Apply(aria.Page{Parts: parts})
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr
}

// seededRow is the focus's screen row, or -1 when it is not on screen.
func seededRow(tr *transcript) int {
	tr.buildIndex()
	span, ok := tr.nodeSpanOf(tr.selection.focus.nodeRef)
	if !ok {
		return -1
	}
	body, _ := tr.layout(len(tr.footLines()))
	if span.first < tr.offset || span.first >= tr.offset+body {
		return -1
	}
	return span.first - tr.offset
}

func TestColdSelectSeedsFromViewportWithoutScrolling(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta int
		park  func(*transcript) // where the reader is looking
		want  func(tr *transcript, body int) int
	}{
		{
			name: "^P at the tail seeds the bottommost visible block", delta: -1,
			park: func(tr *transcript) {},
		},
		{
			name: "^P scrolled up seeds the bottommost visible block", delta: -1,
			park: func(tr *transcript) { tr.stopFollowing(); tr.offset = 4 },
		},
		{
			name: "^N at the tail seeds the topmost visible block", delta: 1,
			park: func(tr *transcript) {},
		},
		{
			name: "^N scrolled up seeds the topmost visible block", delta: 1,
			park: func(tr *transcript) { tr.stopFollowing(); tr.offset = 4 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := seedFixture(t)
			tc.park(tr)
			tr.buildIndex()
			// The viewport as the reader sees it, in CONTENT terms: follow mode
			// re-derives the offset on detach, so compare lines, not the raw
			// offset (see stopFollowing).
			beforeLines := append([]string(nil), tr.window(tr.offset, tr.offset+mustBody(tr), nil)...)

			tr.selectNode(tc.delta, false)

			if !tr.selection.active {
				t.Fatal("cold select did not select anything")
			}
			row := seededRow(tr)
			if row < 0 {
				t.Fatal("the seeded node is not on screen: the cue would be invisible")
			}
			afterLines := tr.window(tr.offset, tr.offset+mustBody(tr), nil)
			// COMPARE BOTTOM-ALIGNED. Detaching from follow hands back the live
			// padding row: the body grows by one and the offset drops by one, so
			// the window gains a line at the TOP and keeps the same last line (see
			// stopFollowing). Nothing the reader was looking at moved away from the
			// bottom edge, which is the property that matters and the one a real
			// scroll would break. Top-aligning would score that one-row hand-back
			// as a scroll and mark a correct frame wrong.
			n := min(len(beforeLines), len(afterLines))
			for i := range n {
				b := beforeLines[len(beforeLines)-1-i]
				a := afterLines[len(afterLines)-1-i]
				if stripCue(b) != stripCue(a) {
					t.Fatalf("entering a selection SCROLLED the page (%d rows up from the bottom):\n before %q\n after  %q",
						i, b, a)
				}
			}
			if len(afterLines) < len(beforeLines) {
				t.Fatalf("viewport lost rows: %d -> %d", len(beforeLines), len(afterLines))
			}

			// Direction: ^P must land at or below where ^N would, since one
			// seeds the bottommost visible block and the other the topmost.
			other := seedFixture(t)
			tc.park(other)
			other.buildIndex()
			other.selectNode(-tc.delta, false)
			otherRow := seededRow(other)
			if tc.delta < 0 && row < otherRow {
				t.Fatalf("^P seeded at screen row %d, above ^N's %d", row, otherRow)
			}
			if tc.delta > 0 && row > otherRow {
				t.Fatalf("^N seeded at screen row %d, below ^P's %d", row, otherRow)
			}
		})
	}
}

func mustBody(tr *transcript) int {
	body, _ := tr.layout(len(tr.footLines()))
	return body
}

// stripCue reduces a rendered row to the thing this test is about: WHERE
// ITS TEXT IS. A selection legitimately changes how the row is drawn -- the
// gutter cue appears, and the whole row is wrapped in a highlight -- and
// none of that is movement. Only a row whose TEXT differs has moved.
//
// It used to strip the cue alone, so the highlight made a stationary row
// compare unequal and the test reported a scroll of "0 rows up from the
// bottom" -- a scroll of nothing, which is what a scroll of nothing looks
// like when the comparison is too literal.
func stripCue(s string) string {
	r := []rune(stripSGRForTest(s))
	if len(r) > 0 && r[0] == '▎' {
		r[0] = ' '
	}
	return string(r)
}
