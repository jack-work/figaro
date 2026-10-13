package cli

import (
	"strings"
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"

	"github.com/jack-work/figaro/sdk/aria"
)

// The question is text on the turn, not a node, and y must still copy it.
func TestYankTheQuestion(t *testing.T) {
	var parts []aria.TurnPart
	for i := range 6 {
		parts = append(parts, richTurn(uint64(i+1), 4))
	}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	view := &ariaView{settings: &renderSettings{}}
	tr := newTranscript(ldrender.NewFakeTerminal(64, 24), 64, 24, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	ref := nodeRef{turn: 4, index: inquiryNode}
	if !tr.selectRef(ref, false) {
		t.Fatal("the question would not take the selection")
	}
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("no copy plan for a selected question")
	}
	reads := 0
	got, err := selectionText(plan, transcriptPageSize,
		func(at aria.Anchor, n int) (aria.Page, error) {
			reads++
			return readBeforeAt(parts, at, n), nil
		})
	if err != nil {
		t.Fatalf("copying the question: %v", err)
	}
	if reads != 0 {
		t.Fatalf("copying what is on screen read the wire %d times", reads)
	}
	if !strings.Contains(got, "please commit 4") {
		t.Fatalf("the yank held %q, want turn 4's question", got)
	}
	if strings.Contains(got, "NODE4-0") {
		t.Fatalf("the yank held the answer as well: %q", got)
	}
}

// A selection past what the window retains copies by reading the pages it
// needs; only the slice that starts a turn carries the question.
func TestYankTheQuestionOfAnUnheldTurn(t *testing.T) {
	var parts []aria.TurnPart
	for i := range 12 {
		parts = append(parts, richTurn(uint64(i+1), 4))
	}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	view := &ariaView{settings: &renderSettings{}}
	tr := newTranscript(ldrender.NewFakeTerminal(64, 24), 64, 24, view, client, "aria1234", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()

	ref := nodeRef{turn: 3, index: inquiryNode}
	if !tr.selectRef(ref, false) {
		t.Fatal("the question would not take the selection")
	}
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("no copy plan")
	}
	client.EvictBefore(aria.Anchor{Turn: 8})
	tr.from = aria.Anchor{Turn: 8}
	tr.invalidateWindow()
	tr.buildIndex()

	got, err := selectionText(plan, transcriptPageSize,
		func(at aria.Anchor, n int) (aria.Page, error) {
			return readBeforeAt(parts, at, n), nil
		})
	if err != nil {
		t.Fatalf("copying a question the window forgot: %v", err)
	}
	if !strings.Contains(got, "please commit 3") {
		t.Fatalf("the yank held %q, want turn 3's question", got)
	}
}
