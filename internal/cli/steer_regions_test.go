package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A steer SPLITS the agent's run, so one turn closes SEVERAL output regions.
// The bridge held exactly one in t.pending and overwrote it, so every region
// but the last was discarded without ever being frozen.
//
// Observed against a real model: a turn with four bash calls and a steer landing
// mid-sequence rendered ONE tool row while show --json listed FOUR. The work was
// done, recorded in the IR, and invisible to the user.
func TestBridge_EveryOutputRegionOfASteeredTurnReachesScrollback(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 100, 40, &renderSettings{}, "aria1234", time.Now(),
		status, func() []string { return []string{"rule", "status"} }, func() string { return "rule" })

	pre := []livedoc.Node{
		{Type: "tool", Name: "bash", Status: "ok", Output: "PRETOOL"},
	}
	post := []livedoc.Node{
		{Type: "tool", Name: "bash", Status: "ok", Output: "POSTTOOL"},
	}

	// The pre-steer output region closes...
	lt.client.OnClosed(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleOutput, Nodes: pre})
	// ...then the steer, then a SECOND output region closes.
	lt.client.OnClosed(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{{Type: "steering", Role: livedoc.RoleInput, Markdown: "STEERTEXT"}}})
	lt.client.OnClosed(aria.Message{Turn: 2, From: 3, Role: livedoc.RoleOutput, Nodes: post})
	lt.finishTurn("completed")

	got := out.String()
	for _, want := range []string{"PRETOOL", "POSTTOOL", "STEERTEXT"} {
		if n := strings.Count(got, want); n != 1 {
			t.Errorf("%q appears %d times in scrollback, want exactly 1:\n%s", want, n, got)
		}
	}
}
