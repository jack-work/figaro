package render

import (
	"strings"
	"testing"

	aria "github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// A one-node steer and the streaming region can share (turn, from); identity
// by those alone let the steer claim the whole region.
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

	in.Open(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleOutput, Nodes: append([]livedoc.Node{steer}, body...)})

	in.Freeze(aria.Message{Turn: 2, From: 1, Role: livedoc.RoleInput,
		Nodes: []livedoc.Node{steer}})

	in.Open(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: body})
	in.Freeze(aria.Message{Turn: 2, From: 2, Role: livedoc.RoleOutput, Nodes: body})

	all := strings.Join(ft.Screen(), "\n")
	if n := strings.Count(all, "It printed APRICOT."); n != 1 {
		t.Errorf("post-steer body appears %d times, want exactly 1:\n%s", n, all)
	}
	if n := strings.Count(all, "MYSTEER"); n != 1 {
		t.Errorf("steer appears %d times, want exactly 1:\n%s", n, all)
	}
	// A tool renders from its name/status, not from Markdown.
	if n := strings.Count(all, "tool bash"); n != 1 {
		t.Errorf("tool row appears %d times, want exactly 1:\n%s", n, all)
	}
}
