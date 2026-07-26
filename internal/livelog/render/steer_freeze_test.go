package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A steering interjection finalizes WHILE the agent is still streaming. It is
// a different region of the same turn, so freezing it must not disturb the live
// region.
//
// aria.Message.LT carries the TURN id, so every message in a turn shares it.
// Identifying the live region by LT alone made this one-node steer claim the
// whole streaming region: Freeze took the "its rows are already on screen" path,
// dropped seventeen rows of in-flight output into scrollback, and the rest of
// the turn was frozen again afterwards — so the entire post-steer block printed
// twice at ordinary heights.
func TestIncipit_FreezingASteerLeavesTheLiveRegionAlone(t *testing.T) {
	const body = "It printed APRICOT after a six-second pause."

	ft := NewFakeTerminal(90, 40)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	// The agent is streaming the open suffix, which starts at node 2.
	in.Open(2, 2, livedoc.RoleOutput, []livedoc.Node{
		{ID: "n2", Type: "thinking", Markdown: "considering"},
		{ID: "n3", Type: "prose", Markdown: body},
	})

	// A steer finalizes mid-stream. Same turn (LT 2), different region (node 1).
	in.Freeze(aria.Message{LT: 2, From: 1, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{{ID: "n1", Type: "steering", Role: livedoc.RoleInput,
			Markdown: "MYSTEER: also set your mantra"}}})

	// Streaming continues, then the output region finalizes normally.
	in.Open(2, 2, livedoc.RoleOutput, []livedoc.Node{
		{ID: "n2", Type: "thinking", Markdown: "considering"},
		{ID: "n3", Type: "prose", Markdown: body},
	})
	in.Freeze(aria.Message{LT: 2, From: 2, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{
			{ID: "n2", Type: "thinking", Markdown: "considering"},
			{ID: "n3", Type: "prose", Markdown: body},
		}})

	all := strings.Join(ft.Screen(), "\n")
	if n := strings.Count(all, "APRICOT"); n != 1 {
		t.Errorf("post-steer output appears %d times, want exactly 1:\n%s", n, all)
	}
	if n := strings.Count(all, "MYSTEER"); n != 1 {
		t.Errorf("steer appears %d times, want exactly 1:\n%s", n, all)
	}
}
