package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// While the agent streams, the open suffix begins at the steer's own index: so
// a one-node steering interjection and the four-node streaming region BOTH
// report From=1. Identity by (turn, from) alone therefore let the steer claim
// the whole region: Freeze took the "already on screen" path, dropped the
// in-flight body into scrollback, and when the producer reopened past the steer
// the same body was frozen a second time.
//
// The observable damage was not only duplication: the second pass rendered a
// DIFFERENT node set, so the tool row vanished while its header survived.
func TestIncipit_SteerInsideTheLiveRegionDoesNotDuplicateIt(t *testing.T) {
	steer := livedoc.Node{ID: "n1", Type: "steering", Role: livedoc.RoleInput, Markdown: "MYSTEER"}
	body := []livedoc.Node{
		{ID: "n2", Type: "thinking", Markdown: "considering"},
		{ID: "n3", Type: "tool", Name: "bash", Status: "ok"},
		{ID: "n4", Type: "prose", Markdown: "It printed APRICOT."},
	}

	ft := NewFakeTerminal(90, 40)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	// The region streams from node 1 and GROWS to include the steer plus body.
	in.Open(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleOutput, Nodes: append([]livedoc.Node{steer}, body...)})

	// The steer finalizes as its own closed message: same start, smaller extent.
	in.Freeze(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{steer}})

	// The producer reopens PAST the steer, then the body finalizes normally.
	in.Open(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: body})
	in.Freeze(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: body})

	all := strings.Join(ft.Screen(), "\n")
	if n := strings.Count(all, "It printed APRICOT."); n != 1 {
		t.Errorf("post-steer body appears %d times, want exactly 1:\n%s", n, all)
	}
	if n := strings.Count(all, "MYSTEER"); n != 1 {
		t.Errorf("steer appears %d times, want exactly 1:\n%s", n, all)
	}
	// The tool row disappearing was part of the same defect, not a separate one.
	// A tool renders from its name/status, not from Markdown, assert the row the
	// renderer actually produces.
	if n := strings.Count(all, "tool bash"); n != 1 {
		t.Errorf("tool row appears %d times, want exactly 1:\n%s", n, all)
	}
}
