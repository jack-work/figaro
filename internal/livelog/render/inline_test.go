package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

const spin = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// testLiveIndex mirrors cli.liveNodeIndex: the first still-mutating node (a
// running tool, or the trailing node); everything before it is final.
func testLiveIndex(nodes []livedoc.Node) int {
	for idx, n := range nodes {
		final := true
		if n.Type == livedoc.NodeTool {
			final = n.Status != livedoc.StatusRunning
		} else {
			final = idx < len(nodes)-1
		}
		if !final {
			return idx
		}
	}
	return len(nodes)
}

// testStableForm mirrors cli.stableForm: the log-emitting "bash" tool collapses
// to its header row (no output); everything else renders in full, blank-joined.
func testStableForm(view NodeView) func([]livedoc.Node, int, int, int) []string {
	return func(nodes []livedoc.Node, from, to, width int) []string {
		var rows []string
		for idx := from; idx < to && idx < len(nodes); idx++ {
			r := view.Render(nodes[idx], width, 0)
			if nodes[idx].Type == livedoc.NodeTool && nodes[idx].Name == "bash" && len(r) > 0 {
				r = r[:1] // done-indication only
			}
			if idx > from {
				rows = append(rows, "")
			}
			for _, l := range r {
				rows = append(rows, clip(l, width))
			}
		}
		return rows
	}
}

// A turn far taller than the viewport must never duplicate a block: finalized
// nodes flush to scrollback, so the live region stays bounded and paint's
// cursor never has to cross the viewport top (the duplication vector).
func TestInline_FlushBoundsLiveRegion_NoDup(t *testing.T) {
	ft := NewFakeTerminal(40, 8)
	in := NewInline(ft, NodeText{})
	in.Header = func(role string) string { return "HDR-" + role }
	in.LiveIndex = testLiveIndex
	in.StableForm = testStableForm(NodeText{})

	const n = 12
	var nodes []livedoc.Node
	for k := 0; k < n; k++ {
		nodes = append(nodes, livedoc.Node{
			ID: fmt.Sprintf("n%d", k), Type: livedoc.NodeThinking,
			Markdown: fmt.Sprintf("blk%02dz", k),
		})
		in.Open(2, "assistant", nodes)
	}

	scr := strings.Join(ft.Screen(), "\n")
	for k := 0; k < n; k++ {
		if c := strings.Count(scr, fmt.Sprintf("blk%02dz", k)); c != 1 {
			t.Fatalf("blk%02dz appeared %dx (want 1):\n%s", k, c, scr)
		}
	}
	if c := strings.Count(scr, "HDR-assistant"); c != 1 {
		t.Fatalf("role header appeared %dx (want 1):\n%s", c, scr)
	}
	if in.vt != 0 {
		t.Fatalf("live region scrolled above viewport top (vt=%d) — not bounded", in.vt)
	}
	if len(in.live) > 8 {
		t.Fatalf("live region has %d rows (> viewport) — flush didn't bound it", len(in.live))
	}
}

// A log-emitting tool shows its streamed output live, but only a one-line
// done-indication ever reaches native scrollback.
func TestInline_LogEmitCollapsesInScrollback(t *testing.T) {
	ft := NewFakeTerminal(50, 20)
	in := NewInline(ft, NodeText{})
	in.LiveIndex = testLiveIndex
	in.StableForm = testStableForm(NodeText{})

	nodes := []livedoc.Node{
		{ID: "b", Type: livedoc.NodeTool, Name: "bash", Status: "running",
			Output: "LOGLINE-alpha\nLOGLINE-beta"},
	}
	in.Open(2, "assistant", nodes)
	if !strings.Contains(strings.Join(ft.Screen(), "\n"), "LOGLINE-alpha") {
		t.Fatalf("running bash output should be visible in the live region")
	}

	nodes[0].Status = "ok"
	nodes = append(nodes, livedoc.Node{ID: "t", Type: livedoc.NodeThinking, Markdown: "afterwards"})
	in.Open(2, "assistant", nodes)
	in.Seal(aria.Message{LT: 2, Role: "assistant", Nodes: nodes})

	scr := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(scr, "bash") {
		t.Fatalf("bash done-indication missing from scrollback:\n%s", scr)
	}
	if strings.Contains(scr, "LOGLINE") {
		t.Fatalf("bash output leaked into scrollback (want header-only):\n%s", scr)
	}
	if strings.Contains(scr, "afterwards") == false {
		t.Fatalf("trailing block missing:\n%s", scr)
	}
}

func TestInline_SealOnce_OpenLive(t *testing.T) {
	ft := NewFakeTerminal(60, 20)
	in := NewInline(ft, NodeText{})

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
func TestInline_ResizeKeepsSealed_RedrawsOpen(t *testing.T) {
	ft := NewFakeTerminal(70, 16)
	in := NewInline(ft, NodeText{})

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

func TestInline_NoTrailingBlanksAfterScrolledSeal(t *testing.T) {
	ft := NewFakeTerminal(40, 6) // short viewport so the message scrolls
	in := NewInline(ft, NodeText{})
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
