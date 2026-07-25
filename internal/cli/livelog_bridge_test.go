package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

func TestIncipitFreezesAssistantAfterTerminalStatusArrives(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() []string { return []string{status.ruleLine(80, ""), status.statusLine(80, false)} }
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, bookend, func() string { return "rule" })

	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID:  0,
		Set: map[string]any{"type": "prose", "markdown": "answer"},
	}}}}}}})
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Sealed: true}}}})
	if lt.pending == nil {
		t.Fatal("assistant close must wait for turn status")
	}

	lt.finishTurn("end_turn")
	if lt.pending != nil {
		t.Fatal("terminal status must freeze pending assistant output")
	}
	if !strings.Contains(out.String(), "completed ✓") {
		t.Fatalf("incipit did not freeze completed status:\n%s", out.String())
	}
}

func TestIncipitFreezesAssistantCloseAfterTurnDone(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() []string { return []string{status.ruleLine(80, ""), status.statusLine(80, false)} }
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, bookend, func() string { return "rule" })

	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID:  0,
		Set: map[string]any{"type": "prose", "markdown": "answer"},
	}}}}}}})
	lt.finishTurn("end_turn")
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Sealed: true}}}})

	if lt.pending != nil {
		t.Fatal("late assistant close must freeze after turn completion")
	}
	if !strings.Contains(out.String(), "completed ✓") {
		t.Fatalf("late close did not freeze completed status:\n%s", out.String())
	}
}

func TestIncipitRepaintsDiscardedAssistantWithErrorStatus(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() []string { return []string{status.ruleLine(80, ""), status.statusLine(80, false)} }
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, bookend, func() string { return "rule" })

	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID:  0,
		Set: map[string]any{"type": "prose", "markdown": "partial"},
	}}}}}}})
	lt.finishTurn("error: provider failed")

	if !strings.Contains(out.String(), "error ✗") {
		t.Fatalf("discarded assistant retained thinking status:\n%s", out.String())
	}
}

func TestTranscriptExitFlushesClosuresBeyondClientTail(t *testing.T) {
	var out bytes.Buffer
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "", time.Now(), newSessionStatus("", time.Now()), nil, nil)
	lt.lastFrozenLT = 1
	lt.enterTranscript()
	for i := 2; i <= transcriptTailLimit+10; i++ {
		lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("message-%03d", i)}},
		}}}})
	}
	lt.leaveTranscript()
	rendered := out.String()
	for _, want := range []string{"message-002", fmt.Sprintf("message-%03d", transcriptTailLimit+10)} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("flushed transcript missing %q", want)
		}
	}
	if lt.lastFrozenLT != transcriptTailLimit+10 || len(lt.pagerClosed) != 0 {
		t.Fatalf("flush state = LT %d, queued %d", lt.lastFrozenLT, len(lt.pagerClosed))
	}
}
