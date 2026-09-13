package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

func TestForkSeedIncludesUnretainedLiveTurn(t *testing.T) {
	var out bytes.Buffer
	lt := newLivelogTurn(&out, 80, 24, &renderSettings{}, "parent", time.Time{}, newSessionStatus("parent", time.Time{}), nil, dimRule)
	lt.enterTranscript()
	lt.apply(aria.Page{Parts: partsFor(1, 5, "PARENT")})
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 6, Inquiry: "parent is running a tool", Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{
			{ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "work in progress"}},
			{ID: 1, Set: map[string]any{"type": string(livedoc.NodeTool), "name": "bash", "status": string(livedoc.StatusRunning)}},
		}},
	}}}})
	plan := lt.retarget("child", newSessionStatus("child", time.Time{}), 7)
	// The clone discarded turn6's live region. The seed must fetch it again.
	var suffix []aria.TurnPart
	for _, part := range partsFor(6, 2, "CHILD") {
		if int(part.ID) >= plan.from {
			suffix = append(suffix, part)
		}
	}
	lt.apply(aria.Page{Parts: suffix})
	lt.tr.buildIndex()
	for _, s := range lt.client.Query(aria.Anchor{Turn: 1}, aria.Anchor{Turn: 7}) {
		if s.Gap != nil {
			t.Fatalf("seed %+v skipped discarded live turn: gap %v..%v", plan, s.Gap.From, s.Gap.To)
		}
	}
}
