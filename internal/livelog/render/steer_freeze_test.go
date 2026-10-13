package render

import (
	"strings"
	"testing"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// A steer finalizes while the agent is still streaming: a different region of
// the same turn, so freezing it must not disturb the live region.
func TestIncipit_FreezingASteerLeavesTheLiveRegionAlone(t *testing.T) {
	const body = "It printed APRICOT after a six-second pause."

	ft := NewFakeTerminal(90, 40)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)

	in.Open(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "n2", Type: "thinking", Markdown: "considering"},
		{ID: "n3", Type: "prose", Markdown: body},
	}})

	in.Freeze(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{{ID: "n1", Type: "steering", Role: livedoc.RoleInput,
			Markdown: "MYSTEER: also set your mantra"}}})

	in.Open(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "n2", Type: "thinking", Markdown: "considering"},
		{ID: "n3", Type: "prose", Markdown: body},
	}})
	in.Freeze(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput,
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
