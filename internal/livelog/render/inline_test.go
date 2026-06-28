package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

const spin = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func TestInline_SealOnce_OpenLive(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewInline(ft, NodeText{})

	// a closed user message → scrollback once
	in.Seal(aria.Message{LT: 1, Role: "user", Nodes: []aria.Node{{ID: "u0", Type: "prose", Markdown: "hello?"}}})
	// open assistant message, streaming a tool
	nodes := []aria.Node{{ID: "n0", Type: "thinking", Markdown: "thinking"}}
	in.Open(2, "assistant", nodes)
	nodes = append(nodes, aria.Node{ID: "n1", Type: "tool", Name: "bash", Status: "running", Output: ""})
	in.Open(2, "assistant", nodes)
	nodes[1] = aria.Node{ID: "n1", Type: "tool", Name: "bash", Status: "running", Output: "x\ny"}
	in.Open(2, "assistant", nodes)
	nodes[1] = aria.Node{ID: "n1", Type: "tool", Name: "bash", Status: "ok", Output: "x\ny"}
	in.Open(2, "assistant", nodes)
	in.Seal(aria.Message{LT: 2, Role: "assistant", Nodes: nodes})

	scr := strings.Join(ft.Screen(), "\n")
	if strings.Count(scr, "hello?") != 1 {
		t.Fatalf("user msg should appear once:\n%s", scr)
	}
	if strings.Count(scr, "tool bash") != 1 {
		t.Fatalf("tool header should appear once:\n%s", scr)
	}
	if strings.ContainsAny(scr, spin) {
		t.Fatalf("no spinner after completion:\n%s", scr)
	}
}

// The point of the whole exercise: a resize mid-open-message repaints only the
// open message; the already-sealed message in scrollback is never touched, so it
// can't duplicate.
func TestInline_ResizeKeepsSealed_RedrawsOpen(t *testing.T) {
	ft := NewFakeTerminal(70, 16)
	in := NewInline(ft, NodeText{})

	in.Seal(aria.Message{LT: 1, Role: "user", Nodes: []aria.Node{{ID: "u0", Type: "prose", Markdown: "list the dir"}}})

	nodes := []aria.Node{
		{ID: "t", Type: "thinking", Markdown: "I'll run ls."},
		{ID: "b", Type: "tool", Name: "bash", Status: "running",
			Output: "l1\nl2\nl3\nl4\nl5\nl6"},
	}
	in.Open(2, "assistant", nodes)

	// SIGWINCH mid-open: shrink, repaint just the open message.
	ft.Resize(70, 8)
	in.Resize(nodes)

	// finish + seal
	nodes[1].Status = "ok"
	in.Open(2, "assistant", nodes)
	in.Seal(aria.Message{LT: 2, Role: "assistant", Nodes: nodes})

	scr := strings.Join(ft.Screen(), "\n")
	if strings.Count(scr, "list the dir") != 1 {
		t.Fatalf("sealed user msg duplicated across resize:\n%s", scr)
	}
	if strings.Count(scr, "tool bash") != 1 {
		t.Fatalf("open tool duplicated across resize:\n%s", scr)
	}
	if strings.ContainsAny(scr, spin) {
		t.Fatalf("stranded spinner after resize+complete:\n%s", scr)
	}
}
