package cli

import (
	"strings"
	"testing"
	"time"
)

// The alt screen has no scrollback, so a stray "\n"+text write on the last
// row scrolls the whole grid and t.prev describes a screen that no longer
// exists; only screenMoved or the resync interval repairs it.

func strayWrite(v *vtScreen, text string) {
	v.row, v.col = v.h-1, 0
	v.scroll(1)
	for _, r := range text {
		v.put(r)
	}
}

func TestScreenMovedRepaintsInFull(t *testing.T) {
	const w, h = 40, 6
	frame := func(status string) []string {
		return []string{"alpha row", "beta row", "gamma row", "delta row", "───── rule", status}
	}
	screen := newVT(w, h)
	tr := &transcript{out: screen, w: w, h: h, active: true, now: func() time.Time { return time.Time{} }}

	tr.paint(append([]string(nil), frame("status ⠋ · ctx 1k")...))
	if got := screen.text()[0]; !strings.HasPrefix(got, "alpha row") {
		t.Fatalf("setup: row 0 = %q", got)
	}

	strayWrite(screen, "STRAY WRITE")

	// Pin the hazard so the test cannot pass for the wrong reason.
	tr.paint(append([]string(nil), frame("status ⠙ · ctx 1k")...))
	if got := screen.text()[0]; strings.HasPrefix(got, "alpha row") {
		t.Fatal("hazard gone: the painter repaired an unannounced scroll by itself: " +
			"if that is now true, this test is measuring nothing and must be rewritten")
	}

	tr.screenMoved()
	tr.paint(append([]string(nil), frame("status ⠹ · ctx 1k")...))
	want := frame("status ⠹ · ctx 1k")
	for r, w := range want {
		if got := screen.text()[r]; got != w {
			t.Errorf("row %d after screenMoved: got %q, want %q", r, got, w)
		}
	}
}

// The bound for writers that never call screenMoved.
func TestResyncRepaintsInFullOnInterval(t *testing.T) {
	const w, h = 40, 6
	frame := func(status string) []string {
		return []string{"alpha row", "beta row", "gamma row", "delta row", "───── rule", status}
	}
	screen := newVT(w, h)
	now := time.Unix(1000, 0)
	tr := &transcript{out: screen, w: w, h: h, active: true, now: func() time.Time { return now }}

	tr.paint(append([]string(nil), frame("s0")...))
	strayWrite(screen, "STRAY WRITE")

	now = now.Add(transcriptResyncInterval / 2)
	tr.paint(append([]string(nil), frame("s1")...))
	if got := screen.text()[0]; got == "alpha row" {
		t.Fatalf("resynced too early: row 0 = %q", got)
	}

	now = now.Add(transcriptResyncInterval)
	tr.paint(append([]string(nil), frame("s2")...))
	for r, w := range frame("s2") {
		if got := screen.text()[r]; got != w {
			t.Errorf("row %d after resync: got %q, want %q", r, got, w)
		}
	}
}

func TestResyncDoesNotFireOnEveryFrame(t *testing.T) {
	const h = 6
	rows := []string{"a", "b", "c", "d", "e", "f"}
	now := time.Unix(1000, 0)
	out := &countingWriter{}
	tr := &transcript{out: out, w: 40, h: h, active: true, now: func() time.Time { return now }}

	tr.paint(append([]string(nil), rows...))
	first := out.bytes.Load()
	out.bytes.Store(0)

	changed := append([]string(nil), rows...)
	changed[5] = "F"
	now = now.Add(transcriptFrameInterval)
	tr.paint(changed)
	second := out.bytes.Load()
	if second == 0 {
		t.Fatal("second frame wrote nothing")
	}
	if second >= first {
		t.Errorf("second frame wrote %d bytes, as much as the full first frame (%d): "+
			"the diff is not being used between resyncs", second, first)
	}
}
