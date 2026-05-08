package cli

import (
	"strings"
	"testing"
	"time"
)

func TestSoloStartPaintsHeader(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "pwd")
	s.Start()
	// Give the ticker a beat so the header definitely lands.
	time.Sleep(10 * time.Millisecond)
	defer s.Freeze()

	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "▶ bash") || !strings.Contains(visible, "pwd") {
		t.Fatalf("expected header with name and detail in:\n%s", visible)
	}
}

func TestSoloFreezeIsIdempotent(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "ls")
	s.Start()
	s.Freeze()
	s.Freeze() // must not panic / hang
}

func TestSoloDoneShowsCheckOrCross(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "ok")
	s.Start()
	s.Done(false)
	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "✓") {
		t.Fatalf("expected ✓ after Done(false), got:\n%s", visible)
	}

	var buf2 safeBuf
	s2 := newToolSoloState(&buf2, "bash", "boom")
	s2.Start()
	s2.Done(true)
	visible2 := stripANSI(buf2.String())
	if !strings.Contains(visible2, "✗") {
		t.Fatalf("expected ✗ after Done(true), got:\n%s", visible2)
	}
}

func TestSoloFreezeStopsSpinner(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "x")
	s.Start()
	time.Sleep(50 * time.Millisecond)
	s.Freeze()
	beforeLen := len(buf.String())
	// After freeze, no further paints should occur.
	time.Sleep(3 * spinnerTick)
	afterLen := len(buf.String())
	if afterLen != beforeLen {
		t.Fatalf("spinner kept painting after Freeze: before=%d after=%d", beforeLen, afterLen)
	}
}

func TestSoloDoneAfterStreamedOutputErasesSpinner(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "head -30 file.md")
	s.Start()
	// Simulate live tool output arriving — first chunk triggers
	// Freeze (mirrors what MethodToolOutput does) and then writes
	// through solo so newlines are counted.
	s.Freeze()
	s.Write([]byte("line one\nline two\nline three\n"))
	s.Done(false)

	raw := buf.String()
	// On a real TTY the cursor-up sequence relocates the cursor to
	// the original header row before the rewrite. We can't render a
	// VT in a unit test, but we can assert the byte stream contains
	// the up-by-N+1 cursor-move where N is the streamed newline
	// count (3 here, so N+1 = 4) and that the byte directly after
	// the move is the erase-line + new ✓ header.
	want := "\033[4A\r\033[2K"
	if !strings.Contains(raw, want) {
		t.Fatalf("expected cursor-up sequence %q in raw output:\n%q", want, raw)
	}
	idx := strings.Index(raw, want)
	after := stripANSI(raw[idx+len(want):])
	if !strings.Contains(after, "✓") {
		t.Fatalf("expected ✓ header after cursor-up; got:\n%q", after)
	}
	// And the matching down-by-4 should also appear.
	if !strings.Contains(raw, "\033[4B\r") {
		t.Fatalf("expected matching cursor-down sequence in raw output:\n%q", raw)
	}
}

func TestSoloDoneNoOutputStillRewritesInPlace(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "true")
	s.Start()
	s.Done(false) // No Freeze-from-output, no streamed bytes.
	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "✓") {
		t.Fatalf("expected ✓ in:\n%s", visible)
	}
}

func TestSoloDoneAfterFreezeWithoutOutput(t *testing.T) {
	// Edge: Freeze fires (e.g. via resumeIfSuspended) before any
	// chunk arrives. Done must still rewrite cleanly without
	// mis-positioned ANSI cursor moves.
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "x")
	s.Start()
	s.Freeze()
	s.Done(true)
	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "✗") {
		t.Fatalf("expected ✗ after error Done, got:\n%s", visible)
	}
}

