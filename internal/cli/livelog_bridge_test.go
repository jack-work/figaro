package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

func TestIncipitSealsAssistantAfterTerminalStatusArrives(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() string { return sessionStatusRule(status, 80, "") }
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, bookend, func() string { return "rule" })

	lt.apply(aria.AriaRead{Live: &aria.Live{
		LT: 2, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{
			ID:  "n0",
			Set: map[string]any{"type": "prose", "markdown": "answer"},
		}},
	}})
	lt.apply(aria.AriaRead{Committed: []aria.Committed{{LT: 2, V: 0}}})
	if lt.pending == nil {
		t.Fatal("assistant close must wait for turn status")
	}

	lt.finishTurn("end_turn")
	if lt.pending != nil {
		t.Fatal("terminal status must seal pending assistant output")
	}
	if !strings.Contains(out.String(), "completed ✓") {
		t.Fatalf("incipit did not seal completed status:\n%s", out.String())
	}
}

func TestIncipitSealsAssistantCloseAfterTurnDone(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() string { return sessionStatusRule(status, 80, "") }
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, bookend, func() string { return "rule" })

	lt.apply(aria.AriaRead{Live: &aria.Live{
		LT: 2, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{
			ID:  "n0",
			Set: map[string]any{"type": "prose", "markdown": "answer"},
		}},
	}})
	lt.finishTurn("end_turn")
	lt.apply(aria.AriaRead{Committed: []aria.Committed{{LT: 2, V: 0}}})

	if lt.pending != nil {
		t.Fatal("late assistant close must seal after turn completion")
	}
	if !strings.Contains(out.String(), "completed ✓") {
		t.Fatalf("late close did not seal completed status:\n%s", out.String())
	}
}

func TestIncipitRepaintsDiscardedAssistantWithErrorStatus(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() string { return sessionStatusRule(status, 80, "") }
	lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, bookend, func() string { return "rule" })

	lt.apply(aria.AriaRead{Live: &aria.Live{
		LT: 2, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{
			ID:  "n0",
			Set: map[string]any{"type": "prose", "markdown": "partial"},
		}},
	}})
	lt.finishTurn("error: provider failed")

	if !strings.Contains(out.String(), "error ✗") {
		t.Fatalf("discarded assistant retained thinking status:\n%s", out.String())
	}
}
