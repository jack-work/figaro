package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// A store that HOLDS history and hands it over a page at a time.
//
// Every other jump fixture in this package answers a page request with the
// empty page — the floor — so the walk terminates on the first fetch and the
// only paths exercised are "already here" and "does not exist". The reported
// failure is neither: `:5` on a large aria showed "jumping to turn 5…", pulled
// one round of history, and then stopped, silently, at the tail. That needs a
// wire with something on it.
type jumpWire struct {
	turns []aria.Turn // oldest first; the whole aria as the daemon holds it
	page  int         // turns per fetch
	clip  bool        // cut the oldest part mid-turn, as a byte budget does
	reads int
}

func (w *jumpWire) before(turn, node int) historyPage {
	w.reads++
	end := -1
	for i, t := range w.turns {
		if int(t.ID) < turn || (int(t.ID) == turn && node > 0) {
			end = i
		}
	}
	if end < 0 {
		return historyPage{} // nothing older: the empty read that proves the floor
	}
	start := end - w.page + 1
	if start < 0 {
		start = 0
	}
	parts := make([]aria.TurnPart, 0, end-start+1)
	for i, t := range w.turns[start : end+1] {
		// A real page is a BYTE window, so its oldest part can open mid-turn.
		// That is the shape that produced the ghost header, and it reaches
		// jumpReachOf through a different branch than a whole turn does.
		if w.clip && i == 0 && start > 0 && len(t.Nodes) > 1 {
			cut := aria.TurnPart{Turn: t, From: 1, ClippedHead: true}
			cut.Turn.Nodes = t.Nodes[1:]
			parts = append(parts, cut)
			continue
		}
		parts = append(parts, aria.TurnPart{Turn: t})
	}
	return committedPage(aria.Page{Parts: parts, More: aria.More{Before: start > 0}})
}

func jumpTurns(first, n int) []aria.Turn {
	out := make([]aria.Turn, 0, n)
	for i := range n {
		id := uint64(first + i)
		out = append(out, aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("QUESTION%d", id), Sealed: true,
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-0", id)},
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-1", id)},
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-2", id)},
			},
		})
	}
	return out
}

// deepJumpFixture loads only the newest tail turns, with the rest on the wire.
func deepJumpFixture(tb testing.TB, all []aria.Turn, held int) (*transcript, *jumpWire) {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	parts := make([]aria.TurnPart, 0, held)
	for _, t := range all[len(all)-held:] {
		parts = append(parts, aria.TurnPart{Turn: t})
	}
	client.Apply(aria.Page{Parts: parts})
	client.SetMoreBefore(true)
	ft := ldrender.NewFakeTerminal(60, 20)
	tr := newTranscript(ft, 60, 20, &ariaView{settings: &renderSettings{}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr, &jumpWire{turns: all, page: 3}
}

// serve plays prefetchTranscriptPages against the wire until the pager stops
// asking, bounded so a spin fails instead of hanging.
func serve(t *testing.T, tr *transcript, w *jumpWire) {
	t.Helper()
	for range jumpBudget * 4 {
		req, need := tr.pageCursor()
		if !need {
			return
		}
		if req.fill != nil {
			// A hole inside the window; Ensure closes it. Nothing in this
			// fixture makes one, so treat it as a bug in the test.
			t.Fatalf("unexpected fill request for %v", *req.fill)
		}
		tr.applyPage(req, w.before(req.before, req.beforeNode))
	}
	t.Fatal("the page cursor never stopped asking")
}

// TestJumpWalksThroughRealHistory is the reported failure, as a unit:
// a target several pages below the window, with a store that actually has it.
func TestJumpWalksThroughRealHistory(t *testing.T) {
	all := jumpTurns(1, 30)
	tr, w := deepJumpFixture(t, all, 4) // holding turns 27..30
	typeJump(tr, "5")
	serve(t, tr, w)

	if tr.jump != nil {
		t.Fatalf(":5 never resolved; still walking after %d reads (note %q)", w.reads, tr.jumpNote)
	}
	if tr.jumpNote != "" {
		t.Fatalf(":5 gave up: %q (after %d reads)", tr.jumpNote, w.reads)
	}
	if rows := viewportRows(tr, 8); !containsRow(rows, "QUESTION5") {
		t.Fatalf(":5 landed at %q, want turn 5 (after %d reads):\n%s",
			topRow(tr), w.reads, strings.Join(rows, "\n"))
	}
	if got := tr.selection.focus.nodeRef.turn; got != 5 {
		t.Fatalf(":5 selected turn %d, want 5", got)
	}
}

// The same, addressed to a node: `:5.2`.
func TestJumpToANodeThroughRealHistory(t *testing.T) {
	all := jumpTurns(1, 30)
	tr, w := deepJumpFixture(t, all, 4)
	typeJump(tr, "5.2")
	serve(t, tr, w)

	if tr.jump != nil || tr.jumpNote != "" {
		t.Fatalf(":5.2 did not land: jump=%v note=%q", tr.jump != nil, tr.jumpNote)
	}
	if got := tr.selection.focus.nodeRef; got != (nodeRef{turn: 5, index: 2}) {
		t.Fatalf(":5.2 selected %+v, want turn 5 node 2", got)
	}
}

// And `:0` over the same wire, so the sentinel and an ordinary target are
// proven against one fixture rather than two.
func TestJumpZeroThroughRealHistory(t *testing.T) {
	all := jumpTurns(1, 30)
	tr, w := deepJumpFixture(t, all, 4)
	typeJump(tr, "0")
	serve(t, tr, w)

	if tr.jump != nil || tr.jumpNote != "" {
		t.Fatalf(":0 did not land: jump=%v note=%q", tr.jump != nil, tr.jumpNote)
	}
	if rows := viewportRows(tr, 8); !containsRow(rows, "QUESTION1") {
		t.Fatalf(":0 landed at %q, want turn 1:\n%s", topRow(tr), strings.Join(rows, "\n"))
	}
}

// The same walk over a wire whose pages open MID-TURN, which is what a byte
// budget produces and what the whole-turn fixtures above never exercise.
func TestJumpWalksThroughClippedHistory(t *testing.T) {
	for _, target := range []string{"5", "0"} {
		t.Run(":"+target, func(t *testing.T) {
			all := jumpTurns(1, 30)
			tr, w := deepJumpFixture(t, all, 4)
			w.clip = true
			typeJump(tr, target)
			serve(t, tr, w)
			if tr.jump != nil {
				t.Fatalf(":%s never resolved after %d reads (note %q)", target, w.reads, tr.jumpNote)
			}
			if tr.jumpNote != "" {
				t.Fatalf(":%s gave up: %q (after %d reads)", target, tr.jumpNote, w.reads)
			}
			want := "QUESTION5"
			if target == "0" {
				want = "QUESTION1"
			}
			if rows := viewportRows(tr, 8); !containsRow(rows, want) {
				t.Fatalf(":%s landed at %q, want %s:\n%s", target, topRow(tr), want, strings.Join(rows, "\n"))
			}
		})
	}
}
