package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

const spin = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func TestIncipit_SealOnce_OpenLive(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewIncipit(ft, NodeText{})

	// a closed user message → scrollback once
	in.Seal(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "hello?"}}})
	// open assistant message, streaming a tool
	nodes := []livedoc.Node{{ID: "n0", Type: "thinking", Markdown: "thinking"}}
	in.Open(2, "assistant", nodes)
	nodes = append(nodes, livedoc.Node{ID: "n1", Type: "tool", Name: "bash", Status: "running", Output: ""})
	in.Open(2, "assistant", nodes)
	nodes[1] = livedoc.Node{ID: "n1", Type: "tool", Name: "bash", Status: "running", Output: "x\ny"}
	in.Open(2, "assistant", nodes)
	nodes[1] = livedoc.Node{ID: "n1", Type: "tool", Name: "bash", Status: "ok", Output: "x\ny"}
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
func TestIncipit_ResizeKeepsSealed_RedrawsOpen(t *testing.T) {
	ft := NewFakeTerminal(70, 16)
	in := NewIncipit(ft, NodeText{})

	in.Seal(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "list the dir"}}})

	nodes := []livedoc.Node{
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

func TestIncipit_NoTrailingBlanksAfterScrolledSeal(t *testing.T) {
	ft := NewFakeTerminal(40, 6) // short viewport so the message scrolls
	in := NewIncipit(ft, NodeText{})
	in.Bookend = func() string { return "=== bookend ===" }
	var nodes []livedoc.Node
	for i := 0; i < 10; i++ {
		nodes = append(nodes, livedoc.Node{ID: "p" + string(rune('0'+i)), Type: livedoc.NodeProse, Markdown: "line"})
	}
	in.Open(2, "assistant", nodes)
	top := ft.Row() // cursor parked at the region's visible top
	in.Seal(aria.Message{LT: 2, Role: "assistant", Nodes: nodes})
	// Sealing a scrolled region must move the cursor past only the VISIBLE rows
	// (<= viewport height); using the full region height leaves the scrolled-off
	// count as blank lines after the bookend.
	if adv := ft.Row() - top; adv > 6 {
		t.Fatalf("seal advanced %d rows (> viewport 6) → trailing blank lines", adv)
	}
}
