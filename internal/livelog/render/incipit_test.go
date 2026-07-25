package render

import (
	"io"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

const spin = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func TestIncipit_FreezeOnce_OpenLive(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewIncipit(ft, NodeText{})

	// a closed user message → scrollback once
	in.Freeze(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "hello?"}}})
	// open assistant message, streaming a tool
	nodes := []livedoc.Node{{ID: "n0", Type: "thinking", Markdown: "thinking"}}
	in.Open(2, "assistant", nodes)
	nodes = append(nodes, livedoc.Node{ID: "n1", Type: "tool", Name: "bash", Status: "running", Output: ""})
	in.Open(2, "assistant", nodes)
	nodes[1] = livedoc.Node{ID: "n1", Type: "tool", Name: "bash", Status: "running", Output: "x\ny"}
	in.Open(2, "assistant", nodes)
	nodes[1] = livedoc.Node{ID: "n1", Type: "tool", Name: "bash", Status: "ok", Output: "x\ny"}
	in.Open(2, "assistant", nodes)
	in.Freeze(aria.Message{LT: 2, Nodes: nodes})

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
// open message; the already-frozen message in scrollback is never touched, so it
// can't duplicate.
func TestIncipit_ResizeKeepsFrozen_RedrawsOpen(t *testing.T) {
	ft := NewFakeTerminal(70, 16)
	in := NewIncipit(ft, NodeText{})

	in.Freeze(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "list the dir"}}})

	nodes := []livedoc.Node{
		{ID: "t", Type: "thinking", Markdown: "I'll run ls."},
		{ID: "b", Type: "tool", Name: "bash", Status: "running",
			Output: "l1\nl2\nl3\nl4\nl5\nl6"},
	}
	in.Open(2, "assistant", nodes)

	// SIGWINCH mid-open: shrink, repaint just the open message.
	ft.Resize(70, 8)
	in.Resize(nodes)

	// finish + freeze
	nodes[1].Status = "ok"
	in.Open(2, "assistant", nodes)
	in.Freeze(aria.Message{LT: 2, Nodes: nodes})

	scr := strings.Join(ft.Screen(), "\n")
	if strings.Count(scr, "list the dir") != 1 {
		t.Fatalf("frozen user msg duplicated across resize:\n%s", scr)
	}
	if strings.Count(scr, "tool bash") != 1 {
		t.Fatalf("open tool duplicated across resize:\n%s", scr)
	}
	if strings.ContainsAny(scr, spin) {
		t.Fatalf("stranded spinner after resize+complete:\n%s", scr)
	}
}

func TestIncipit_NoTrailingBlanksAfterScrolledFreeze(t *testing.T) {
	ft := NewFakeTerminal(40, 6) // short viewport so the message scrolls
	in := NewIncipit(ft, NodeText{})
	in.Bookend = func() []string { return []string{"=== bookend ==="} }
	var nodes []livedoc.Node
	for i := 0; i < 10; i++ {
		nodes = append(nodes, livedoc.Node{ID: "p" + string(rune('0'+i)), Type: livedoc.NodeProse, Markdown: "line"})
	}
	in.Open(2, "assistant", nodes)
	top := ft.Row() // cursor parked at the region's visible top
	in.Freeze(aria.Message{LT: 2, Nodes: nodes})
	// Freezing a scrolled region must move the cursor past only the VISIBLE rows
	// (<= viewport height); using the full region height leaves the scrolled-off
	// count as blank lines after the bookend.
	if adv := ft.Row() - top; adv > 6 {
		t.Fatalf("freeze advanced %d rows (> viewport 6) → trailing blank lines", adv)
	}
}

// OpenThinking pins the footer before any content; the assistant frame that
// follows adopts the same region in place — the header/footer must not orphan
// to scrollback (no duplicate "figaro" header, no duplicate footer rule).
func TestIncipit_ThinkingAdoptedInPlace(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewIncipit(ft, NodeText{})
	in.Header = func(role string) string {
		if role == "assistant" {
			return "‹ figaro"
		}
		return "❯ you"
	}
	in.Bookend = func() []string { return []string{"────rule────", "", "status"} }

	in.Freeze(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "hi"}}})
	in.OpenThinking("assistant") // footer appears now, before any token
	scr := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(scr, "status") || !strings.Contains(scr, "‹ figaro") {
		t.Fatalf("thinking footer + header should show immediately:\n%s", scr)
	}

	// content streams in; the real assistant frame adopts the region
	in.Open(2, "assistant", []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "answer"}})
	in.Freeze(aria.Message{LT: 2, Role: "assistant", Nodes: []livedoc.Node{{ID: "n0", Type: "prose", Markdown: "answer"}}})
	scr = strings.Join(ft.Screen(), "\n")
	if strings.Count(scr, "‹ figaro") != 1 {
		t.Fatalf("figaro header must appear once (no orphan):\n%s", scr)
	}
	if strings.Count(scr, "────rule────") != 1 {
		t.Fatalf("footer rule must appear once (no orphan):\n%s", scr)
	}
	if !strings.Contains(scr, "answer") {
		t.Fatalf("streamed answer should show:\n%s", scr)
	}
}

// A turn that errors before any content adopts the thinking placeholder must
// tear the footer region down (AbandonOpen) so an error hint printed straight
// to the terminal afterward lands on clean scrollback — not glued into the
// live footer rule. Regression for the no-provider scrollback artifact.
func TestIncipit_ThinkingAbandonedOnEarlyError(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewIncipit(ft, NodeText{})
	in.Header = func(role string) string { return "‹ figaro" }
	in.Bookend = func() []string { return []string{"──── aria xyz ────", "", "status"} }

	in.Freeze(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{ID: "u0", Type: "prose", Markdown: "quick test"}}})
	in.OpenThinking("assistant") // footer live, no content yet
	in.AbandonOpen("")           // turn errors immediately -> teardown

	// After teardown the region is released: a direct write (the error hint)
	// must not overlap the footer rule on any line.
	io.WriteString(ft, "\r\nNo provider connected\r\n")
	for _, line := range ft.Screen() {
		if strings.Contains(line, "aria xyz") && strings.Contains(line, "provider") {
			t.Fatalf("footer rule glued into the hint line: %q", line)
		}
	}
	scr := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(scr, "No provider connected") {
		t.Fatalf("hint should print cleanly:\n%s", scr)
	}
}
