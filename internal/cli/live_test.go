package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

var liveAnsiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func liveStrip(s string) string {
	return strings.ReplaceAll(liveAnsiRE.ReplaceAllString(s, ""), "\r", "")
}

func eraseCount(s string) int { return strings.Count(s, term.EraseLine) }

// capture runs fn and returns the bytes written during it.
func capture(buf *bytes.Buffer, fn func()) string {
	start := buf.Len()
	fn()
	return buf.String()[start:]
}

func prose(md string) livedoc.Node { return livedoc.Node{Type: livedoc.NodeProse, Markdown: md} }
func toolNode(name, status, output string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeTool, Name: name, Status: status, Output: output}
}

// patchOutput builds the op that splices a tool node's output old→new.
func patchOutput(idx int, old, next string) livedoc.Op {
	d, _ := livedoc.Diff(old, next)
	return livedoc.Op{Kind: livedoc.OpPatch, Index: idx, Field: "output", At: d.At, Del: d.Del, Ins: d.Ins}
}

func TestLive_SnapshotRendersContent(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot([]livedoc.Node{prose("Hello, **world**.")})
	if !strings.Contains(liveStrip(buf.String()), "Hello, world.") {
		t.Fatalf("snapshot did not render content; got %q", liveStrip(buf.String()))
	}
}

func TestLive_ToolOutputAppendRewritesOnlyTail(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 50)
	lr.snapshot([]livedoc.Node{toolNode("bash", livedoc.StatusRunning, "alpha\nbravo")})
	paint := capture(&buf, func() {
		lr.applyOp(patchOutput(0, "alpha\nbravo", "alpha\nbravo\ncharlie"))
	})

	// Only the new output row is written; the header + alpha/bravo rows are
	// untouched (same spinner frame, same content).
	if got := eraseCount(paint); got != 1 {
		t.Fatalf("append should rewrite only the new row (1), got %d:\n%q", got, paint)
	}
	if strings.Contains(liveStrip(paint), "alpha") || strings.Contains(liveStrip(paint), "bravo") {
		t.Fatalf("untouched rows were rewritten:\n%q", liveStrip(paint))
	}
	if !strings.Contains(liveStrip(paint), "charlie") {
		t.Fatalf("new line missing from paint:\n%q", liveStrip(paint))
	}
}

func TestLive_SpinnerTickRewritesHeaderOnly(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot([]livedoc.Node{toolNode("bash", livedoc.StatusRunning, "line\nstays put")})
	if !lr.running() {
		t.Fatal("expected a running spinner")
	}
	paint := capture(&buf, func() { lr.tickSpin() })

	if got := eraseCount(paint); got != 1 {
		t.Fatalf("spinner tick must rewrite exactly the header row (1), got %d:\n%q", got, paint)
	}
	if !strings.ContainsRune(liveStrip(paint), render.SpinnerFrames[1]) {
		t.Fatalf("spinner did not advance to frame 1:\n%q", liveStrip(paint))
	}
	if strings.Contains(liveStrip(paint), "stays put") {
		t.Fatal("an output row was rewritten by a spinner tick")
	}
}

func TestLive_FinalizedPrefixFlushedOnceNeverRewritten(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 50)
	// A completed tool (stable), then a running tool (live tail).
	nodes := []livedoc.Node{
		toolNode("bash", livedoc.StatusOK, "first done"),
		toolNode("bash", livedoc.StatusRunning, "out1"),
	}
	snap := capture(&buf, func() { lr.snapshot(nodes) })
	if !strings.Contains(liveStrip(snap), "first done") {
		t.Fatalf("stable block missing from initial flush:\n%q", liveStrip(snap))
	}

	paint := capture(&buf, func() { lr.applyOp(patchOutput(1, "out1", "out1\nout2")) })
	if strings.Contains(liveStrip(paint), "first done") {
		t.Fatalf("flushed stable block was rewritten on a tail op:\n%q", liveStrip(paint))
	}
	if !strings.Contains(liveStrip(paint), "out2") {
		t.Fatalf("tail update missing:\n%q", liveStrip(paint))
	}
}

func TestLive_CommitDropsBelowAndResets(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot([]livedoc.Node{prose("some content")})
	out := capture(&buf, func() { lr.commit() })
	if !strings.Contains(out, "\n") { // real newline, not CursorDown (scroll-safe)
		t.Fatalf("commit should move below the region with a newline; got %q", out)
	}
	if lr.nodes != nil || lr.live != nil || lr.flushed != 0 {
		t.Fatalf("commit did not reset state: nodes=%v live=%v flushed=%d", lr.nodes, lr.live, lr.flushed)
	}
}

func TestLive_SnapshotResyncClearsAndRepaints(t *testing.T) {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 80, 10)
	lr.snapshot([]livedoc.Node{prose("first version")})
	out := capture(&buf, func() { lr.snapshot([]livedoc.Node{prose("second version")}) })
	if !strings.Contains(out, eraseToEnd) {
		t.Fatal("resync snapshot should clear the prior live region")
	}
	if !strings.Contains(liveStrip(out), "second version") {
		t.Fatalf("resync did not render new content:\n%q", liveStrip(out))
	}
}
