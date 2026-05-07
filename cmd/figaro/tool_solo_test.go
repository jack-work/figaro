package main

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
