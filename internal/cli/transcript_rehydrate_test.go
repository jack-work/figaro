package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

func TestSelectionTextRehydratesBackwardWithoutPayloadCache(t *testing.T) {
	history := transcriptHistory(200)
	plan := selectionCopyPlan{
		lo: testSelectionPoint(1, 0, history[0].Nodes[0]),
		hi: testSelectionPoint(200, 0, history[199].Nodes[0]),
	}
	reads := 0
	text, err := selectionText(plan, transcriptPageSize, func(before, limit int) (aria.AriaRead, error) {
		reads++
		return readBefore(history, before, limit), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "message-001") || !strings.HasSuffix(text, "message-200") {
		t.Fatalf("rehydrated range boundaries missing")
	}
	if reads != 7 {
		t.Fatalf("selection rehydrate reads = %d, want 7", reads)
	}
}

func TestSelectionTextRejectsChangedEndpoint(t *testing.T) {
	history := transcriptHistory(3)
	plan := selectionCopyPlan{
		lo: testSelectionPoint(1, 0, history[0].Nodes[0]),
		hi: testSelectionPoint(3, 0, history[2].Nodes[0]),
	}
	history[2].Nodes[0].Markdown = "changed"
	_, err := selectionText(plan, transcriptPageSize, func(before, limit int) (aria.AriaRead, error) {
		return readBefore(history, before, limit), nil
	})
	if err == nil {
		t.Fatal("changed selection endpoint was accepted")
	}
}
