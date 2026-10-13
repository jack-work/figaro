package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// A steer splits the agent's run, so one turn closes several output regions.
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

	lt.client.OnClosed(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleOutput, Nodes: pre})
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

// Output is deferred in pending while input freezes immediately, so a steer
// must not overtake the output region that preceded it.
func TestBridge_SteerDoesNotOvertakeThePendingOutputRegion(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 100, 40, &renderSettings{}, "aria1234", time.Now(),
		status, func() []string { return []string{"rule", "status"} }, func() string { return "rule" })

	lt.client.OnClosed(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{Type: "tool", Name: "bash", Status: "ok", Output: "EARLYTOOL"}}})
	lt.client.OnClosed(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{{Type: "steering", Role: livedoc.RoleInput, Markdown: "STEERTEXT"}}})
	lt.finishTurn("completed")

	got := out.String()
	iTool := strings.Index(got, "EARLYTOOL")
	iSteer := strings.Index(got, "STEERTEXT")
	if iTool < 0 || iSteer < 0 {
		t.Fatalf("both must render:\n%s", got)
	}
	if iTool > iSteer {
		t.Errorf("steer was hoisted above the tool that preceded it "+
			"(tool at %d, steer at %d): live order must match the projection:\n%s",
			iTool, iSteer, got)
	}
}
