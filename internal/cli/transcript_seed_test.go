package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// A cold ^N/^P seeds from the viewport, not the retained window, and does not
// scroll. At the top of the window the two coincide, so that case proves nothing.

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
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr
}

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
		park  func(*transcript)
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
			// Detaching from follow re-derives the offset, so compare
			// lines.
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
			// Bottom-aligned: detaching hands back the live padding
			// row, so the window gains a line at the top and keeps its
			// last line.
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

// The selection cue is renditions alone; only a row whose text differs moved.
func stripCue(s string) string { return stripSGRForTest(s) }
