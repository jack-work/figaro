package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/term"
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
	if !strings.Contains(raw, "\r\033[4B") {
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

// TestSoloDoneAfterStreamedOutputWithoutFreeze covers the regression
// the unified repaint primitive was introduced to fix: a tool that
// streams output between Start and Done with no intermediate Freeze.
// The previous Done shortcut delegated to Freeze in that case, which
// repainted at the wrong cursor location and left the original
// running-spinner header on screen alongside the new ✓ header.
func TestSoloDoneAfterStreamedOutputWithoutFreeze(t *testing.T) {
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "echo hi")
	s.Start()
	s.Write([]byte("alpha\nbeta\ngamma\n"))
	s.Done(false)

	rendered := renderTermGrid(buf.String(), 0)
	assertSingleCheckedHeader(t, rendered)
}

// Regression: a tool output line longer than the terminal width
// wraps onto multiple physical rows. Before the wrap-aware cursor
// tracker, rowsBelow only counted '\n' bytes, so the header repaint
// walked too few rows up — the original spinner header survived on
// screen and the ✓ header was painted somewhere lower.
func TestSoloDoneAfterWrappedStreamErasesSpinner(t *testing.T) {
	width := term.Width()
	// Wrap deterministically across three rows: width + width + half.
	long := strings.Repeat("A", width*2+width/2)

	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "ls")
	s.Start()
	s.Write([]byte(long))
	s.Write([]byte("\n"))
	s.Done(false)

	rendered := renderTermGrid(buf.String(), width)
	assertSingleCheckedHeader(t, rendered)
}

func assertSingleCheckedHeader(t *testing.T, rendered []string) {
	t.Helper()
	headerRows := 0
	for _, row := range rendered {
		if strings.Contains(row, "▶ bash") {
			headerRows++
		}
	}
	if headerRows != 1 {
		t.Fatalf("expected exactly one header row in rendered grid, got %d:\n%s",
			headerRows, strings.Join(rendered, "\n"))
	}
	for _, row := range rendered {
		if strings.Contains(row, "▶ bash") && strings.Contains(row, "✓") {
			return
		}
	}
	t.Fatalf("expected ✓ in surviving header row, grid was:\n%s",
		strings.Join(rendered, "\n"))
}

// renderTermGrid replays a byte stream into a tiny VT100 emulator
// (CUU/CUD/EL only — enough for solo header repaints) and returns the
// resulting rows with ANSI stripped. Lets us assert what a real
// terminal would actually display. Operates on bytes, not runes:
// good enough for our header-byte-equality assertions and avoids
// having to handle multi-byte UTF-8 column accounting.
//
// width > 0 makes printable bytes auto-wrap at that column (mirroring
// what a real terminal does); width == 0 disables wrap.
func renderTermGrid(s string, width int) []string {
	rows := [][]byte{nil}
	r, c := 0, 0
	ensure := func(idx int) {
		for len(rows) <= idx {
			rows = append(rows, nil)
		}
	}
	put := func(b byte) {
		ensure(r)
		row := rows[r]
		for len(row) <= c {
			row = append(row, ' ')
		}
		row[c] = b
		rows[r] = row
		c++
		if width > 0 && c >= width {
			r++
			c = 0
			ensure(r)
		}
	}
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ';') {
				j++
			}
			if j >= len(s) {
				break
			}
			param := s[i+2 : j]
			cmd := s[j]
			n := 1
			if param != "" {
				fmt.Sscanf(param, "%d", &n)
			}
			switch cmd {
			case 'A':
				r -= n
				if r < 0 {
					r = 0
				}
			case 'B':
				r += n
				ensure(r)
			case 'K':
				ensure(r)
				rows[r] = nil
				c = 0
			case 'm', 'J':
				// SGR / erase display — ignore.
			}
			i = j + 1
			continue
		}
		switch s[i] {
		case '\n':
			r++
			c = 0
			ensure(r)
		case '\r':
			c = 0
		default:
			put(s[i])
		}
		i++
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = stripANSI(string(row))
	}
	return out
}

