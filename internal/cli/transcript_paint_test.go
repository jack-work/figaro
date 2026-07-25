package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// paintReference is the pre-optimization painter: a fresh strings.Builder per
// frame, rows formatted through fmt.
func paintReference(out io.Writer, prev, screen []string) {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for r := 0; r < len(screen); r++ {
		var old string
		if r < len(prev) {
			old = prev[r]
		}
		if screen[r] != old {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, screen[r])
		}
	}
	b.WriteString("\x1b[?2026l")
	io.WriteString(out, b.String())
}

// TestPaintMatchesReference walks a sequence of frames through both painters
// and requires the emitted bytes to be identical, including the buffer reuse
// across frames (which is where an aliasing bug would show up).
func TestPaintMatchesReference(t *testing.T) {
	frames := [][]string{
		{"one", "two", "three"},
		{"one", "CHANGED", "three"},
		{"one", "CHANGED", "three"},
		{"", "", ""},
		{"\x1b[2mdim\x1b[0m", "日本語", ""},
		{"\x1b[2mdim\x1b[0m", "日本語", "tail"},
		{strings.Repeat("x", 200), "", "\x1b[7mhl\x1b[27m"},
	}
	var gotBuf, wantBuf bytes.Buffer
	tr := &transcript{out: &gotBuf, active: true, h: 3}
	var prev []string
	for i, f := range frames {
		screen := append([]string(nil), f...)
		tr.paint(append([]string(nil), f...))
		paintReference(&wantBuf, prev, screen)
		prev = screen
		if gotBuf.String() != wantBuf.String() {
			t.Fatalf("frame %d: paint emitted\n%q\nwant\n%q", i, gotBuf.String(), wantBuf.String())
		}
	}
}

// TestPaintReusesBuffers pins the recycling: a steady stream of frames must
// not allocate a new output buffer or frame slice every time.
func TestPaintReusesBuffers(t *testing.T) {
	tr := &transcript{out: io.Discard, active: true, h: 4}
	frames := [][]string{
		{"a", "b", "c", "d"},
		{"a", "b", "c", "e"},
	}
	i := 0
	got := testing.AllocsPerRun(200, func() {
		screen := tr.nextScreen()
		copy(screen, frames[i%2])
		tr.paint(screen)
		i++
	})
	if got > 0 {
		t.Errorf("steady-state paint allocated %v times per frame, want 0", got)
	}
}
