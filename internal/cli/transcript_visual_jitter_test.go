package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

// FRAME JITTER. A highlight is a claim about SOURCE TEXT, and the rows that
// text is painted on are rebuilt on every live frame, every resize, and every
// page landing. These tests hold a highlight still and shake everything
// around it, asserting that the wash and the cursor stay on the same source
// runes and that no frame paints a stale wash or a second cursor.

// jitterHistory is a sealed conversation with real Src coordinates, so a
// highlight can be resolved to a coordinate and compared across frames.
func jitterHistory(n int) []aria.TurnPart {
	out := make([]aria.TurnPart, n)
	for i := range out {
		lt := uint64(10 + 3*i)
		out[i] = aria.TurnPart{Turn: aria.Turn{
			ID: uint64(i + 1), Sealed: true, LTs: []uint64{lt, lt + 1},
			Inquiry: fmt.Sprintf("question %03d", i+1),
			Nodes: []livedoc.Node{{
				Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: lt + 1, Block: 0}},
				Markdown: fmt.Sprintf("answer %03d: the quick brown fox jumps over the lazy dog, and then a second sentence to make the row wrap at narrow widths", i+1),
			}},
		}}
	}
	return out
}

func jitterFixture(t *testing.T, n, w, h int) (*transcript, *aria.Client, []aria.TurnPart) {
	t.Helper()
	history := jitterHistory(n)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(w, h), w, h, ldrender.NodeText{}, client, "aria1234", time.Time{})
	tr.enter()
	tr.render()
	return tr, client, history
}

// paintCounts is what one frame carries: how many cursor cells and how many
// rows with a wash.
func paintCounts(tr *transcript) (cursors, washed int) {
	wash, cur := term.SelectWash(), term.Cursor()
	for _, row := range tr.window(0, tr.index.total, nil) {
		cursors += strings.Count(row, cur)
		if strings.Contains(row, wash) {
			washed++
		}
	}
	return cursors, washed
}

func mustRange(t *testing.T, tr *transcript) quote.Range {
	t.Helper()
	r, _, err := tr.visualRange()
	if err != "" {
		t.Fatalf("visualRange: %s", err)
	}
	return r
}

// (a) A highlight held while the aria streams: live frames land on an open
// turn below it, rows are re-rendered, and the wash must name the same
// source runes on every frame, with exactly one cursor.
func TestVisualJitter_HighlightHoldsWhileStreaming(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, client, _ := jitterFixture(t, 30, 60, 20)
	tr.key('v')
	tr.key('k')
	tr.key('k')
	tr.key('v')
	tr.key('k')
	tr.key('k')
	tr.key('k')
	want := mustRange(t, tr)
	wantText := tr.visualText()
	c0, w0 := paintCounts(tr)
	if c0 != 1 || w0 < 2 {
		t.Fatalf("precondition: cursors=%d washed=%d", c0, w0)
	}
	var grown strings.Builder
	for i := 0; i < 40; i++ {
		grown.WriteString(fmt.Sprintf("streamed token %d ", i))
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 31, Live: &aria.Live{From: 0, V: i, Nodes: []aria.NodeDelta{{
			ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": grown.String()},
		}}}}}}}, aria.Notify)
		tr.render()
		if got := mustRange(t, tr); got != want {
			t.Fatalf("frame %d moved the highlight: %+v -> %+v", i, want, got)
		}
		if got := tr.visualText(); got != wantText {
			t.Fatalf("frame %d changed the highlighted text:\n%q\n%q", i, wantText, got)
		}
		c, w := paintCounts(tr)
		if c != 1 || w != w0 {
			t.Fatalf("frame %d painted cursors=%d washed=%d, want 1 and %d", i, c, w, w0)
		}
	}
}

// (b) Resize mid-selection: rows reflow at the new width, and the wash
// follows the text.
func TestVisualJitter_ResizeReflowsUnderTheHighlight(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, _, _ := jitterFixture(t, 12, 80, 24)
	tr.key('v')
	tr.key('k')
	tr.key('^')
	for range 12 {
		tr.key('l')
	}
	tr.key('v')
	tr.key('k')
	tr.key('$')
	want := mustRange(t, tr)
	for _, w := range []int{50, 36, 100, 60} {
		tr.resize(w, 24)
		got, _, err := tr.visualRange()
		if err != "" {
			t.Fatalf("at width %d: %s", w, err)
		}
		if got != want {
			t.Fatalf("resize to %d moved the highlight: %+v -> %+v", w, want, got)
		}
		if c, _ := paintCounts(tr); c != 1 {
			t.Fatalf("at width %d: %d cursor cells", w, c)
		}
	}
}

// (c) The cursor on the open (live) message while it grows: the point is
// (node, row, col) and the node's rows are re-rendered on every frame; the
// cursor stays on its row and column, and the wash below it grows with the
// text only when the cursor is the lower end.
func TestVisualJitter_CursorOnTheOpenMessage(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, client, _ := jitterFixture(t, 5, 60, 20)
	open := func(v int, md string) {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 6, Live: &aria.Live{From: 0, V: v, Nodes: []aria.NodeDelta{{
			ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": md},
		}}}}}}}, aria.Notify)
		tr.render()
	}
	open(0, "live one two")
	tr.key('v')
	if tr.visual.cursor.ref.turn != 6 {
		t.Fatalf("the cursor did not seed on the open turn: %+v", tr.visual.cursor)
	}
	tr.key('w')
	tr.key('v')
	before := tr.visual.cursor
	text := "live one two"
	for i := 0; i < 20; i++ {
		text += fmt.Sprintf(" more%d", i)
		open(i+1, text)
		if tr.visual.cursor != before {
			t.Fatalf("frame %d moved the cursor: %+v -> %+v", i, before, tr.visual.cursor)
		}
		if got, _ := paintCounts(tr); got != 1 {
			t.Fatalf("frame %d painted %d cursors", i, got)
		}
		if _, ok := tr.visualCursorLine(); !ok {
			t.Fatalf("frame %d lost the cursor's line", i)
		}
	}
}

// (d) The start of the selection phases out of the retained window: the
// wash keeps painting to the window's edge, and spending the highlight is
// refused with the memory sentence.
func TestVisualJitter_StartPhasedOutRefusesToSpend(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, client, _ := jitterFixture(t, 40, 60, 12)
	tr.key('v')
	for range 30 {
		tr.key('k')
	}
	tr.key('v') // anchor high up
	tr.key('G') // cursor at the tail
	if _, _, err := tr.visualRange(); err != "" {
		t.Fatalf("precondition: %s", err)
	}
	// Evict everything before the cursor's turn: what the pager does to
	// messages far behind the viewport.
	client.EvictBefore(aria.Anchor{Turn: uint64(tr.visual.cursor.ref.turn - 2)})
	tr.invalidateWindow()
	tr.buildIndex()
	tr.render()
	if _, ok := tr.visualLineOf(tr.visual.anchor); ok {
		t.Fatal("precondition: the anchor's message is still held")
	}
	s := tr.visualSpan()
	if !s.active() || s.loLine != 0 {
		t.Fatalf("the wash did not widen to the window's edge: %+v", s)
	}
	_, _, err := tr.visualRange()
	if !strings.Contains(err, "no longer in memory") {
		t.Fatalf("spending a phased-out start: %q", err)
	}
	if _, _, err := tr.visualCoordinate(); !strings.Contains(err, "no longer in memory") {
		t.Fatalf("Y: %q", err)
	}
}
