package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// jumpWire holds history and hands it over a page at a time, so the walk
// does not terminate on the first fetch.
type jumpWire struct {
	turns []aria.Turn
	page  int
	clip  bool
	reads int
}

func (w *jumpWire) before(turn, node int) aria.Page {
	w.reads++
	end := -1
	for i, t := range w.turns {
		if int(t.ID) < turn || (int(t.ID) == turn && node > 0) {
			end = i
		}
	}
	if end < 0 {
		return aria.Page{}
	}
	start := end - w.page + 1
	if start < 0 {
		start = 0
	}
	parts := make([]aria.TurnPart, 0, end-start+1)
	for i, t := range w.turns[start : end+1] {
		// A byte-budgeted page can open mid-turn.
		if w.clip && i == 0 && start > 0 && len(t.Nodes) > 1 {
			cut := aria.TurnPart{Turn: t, From: 1, ClippedHead: true}
			cut.Turn.Nodes = t.Nodes[1:]
			parts = append(parts, cut)
			continue
		}
		parts = append(parts, aria.TurnPart{Turn: t})
	}
	return aria.Page{Parts: parts, More: aria.More{Before: start > 0}}
}

func (w *jumpWire) from(turn, node int) aria.Page {
	w.reads++
	start := -1
	for i, t := range w.turns {
		if int(t.ID) >= turn {
			start = i
			break
		}
	}
	if start < 0 {
		return aria.Page{}
	}
	end := min(start+w.page, len(w.turns))
	parts := make([]aria.TurnPart, 0, end-start)
	for i, t := range w.turns[start:end] {
		if i == 0 && node > 0 && node < len(t.Nodes) {
			cut := aria.TurnPart{Turn: t, From: uint64(node), ClippedHead: true}
			cut.Turn.Nodes = t.Nodes[node:]
			parts = append(parts, cut)
			continue
		}
		parts = append(parts, aria.TurnPart{Turn: t})
	}
	return aria.Page{Parts: parts, More: aria.More{Before: start > 0, After: end < len(w.turns)}}
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

func deepJumpFixture(tb testing.TB, all []aria.Turn, held int) (*transcript, *jumpWire) {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	parts := make([]aria.TurnPart, 0, held)
	for _, t := range all[len(all)-held:] {
		parts = append(parts, aria.TurnPart{Turn: t})
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	client.SetMoreBefore(true)
	ft := ldrender.NewFakeTerminal(60, 20)
	tr := newTranscript(ft, 60, 20, &ariaView{settings: &renderSettings{}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr, &jumpWire{turns: all, page: 3}
}

func serve(t *testing.T, tr *transcript, w *jumpWire) {
	t.Helper()
	for range 96 {
		req, need := tr.pageCursor()
		if !need {
			return
		}
		if req.fill != nil {
			tr.client.Apply(w.from(int(req.fill.From.Turn), int(req.fill.From.Node)), aria.Quiet)
			tr.invalidateWindow()
			tr.settle()
			tr.jumpAdvance()
			continue
		}
		if req.seek == seekForward {
			tr.applyPage(req, w.from(int(req.at.Turn), int(req.at.Node)))
			continue
		}
		tr.applyPage(req, w.before(int(req.at.Turn), int(req.at.Node)))
	}
	t.Fatal("the page cursor never stopped asking")
}

func TestJumpWalksThroughRealHistory(t *testing.T) {
	all := jumpTurns(1, 30)
	tr, w := deepJumpFixture(t, all, 4)
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

// Pages opening mid-turn, as a byte budget produces.
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
