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
	// First chunk triggers Freeze (mirrors MethodToolOutput) then
	// streams content through solo so newlines feed the tail buffer.
	s.Freeze()
	s.Write([]byte("line one\nline two\nline three\n"))
	s.Done(false)

	rendered := renderTermGrid(buf.String(), term.Width())
	assertSingleCheckedHeader(t, rendered)

	// The streamed lines must be in the rendered grid below the header.
	joined := strings.Join(rendered, "\n")
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(stripANSI(joined), want) {
			t.Fatalf("expected %q in rendered grid:\n%s", want, joined)
		}
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

// Regression: when streamed output is taller than the viewport, the
// pre-buffered design would scroll the spinner header into scrollback
// where CursorUp can't reach it; the ✓ header would land somewhere
// in the middle of the visible output. The rolling tail caps the
// live region at term.Height-3 so the header stays on screen.
func TestSoloRollingTailBoundedByViewport(t *testing.T) {
	// term.Height() returns 24 in tests, so maxRows = 21.
	// Stream 100 logical lines — way over the cap. Expect:
	//  - exactly one header (✓) in the grid
	//  - the live region (header + visible tail) is at most maxRows+1
	//  - the LAST committed line is visible (tail keeps the recent end)
	//  - the FIRST committed line has been evicted from the visible tail
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", "tall")
	s.Start()
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	s.Write([]byte(sb.String()))
	s.Done(false)

	rendered := renderTermGrid(buf.String(), term.Width())
	assertSingleCheckedHeader(t, rendered)

	joined := stripANSI(strings.Join(rendered, "\n"))
	if !strings.Contains(joined, "line 99") {
		t.Fatalf("tail should include latest line (line 99); grid:\n%s", joined)
	}
	if strings.Contains(joined, "line 0\n") || strings.HasPrefix(joined, "line 0") {
		t.Fatalf("tail should NOT include line 0 (evicted); grid:\n%s", joined)
	}
}

// Regression: ASCII HT (0x09) used to advance the model's col by 1
// instead of jumping to the next 8-col tab stop. For Go test output
// — which interleaves tabs between the "ok" prefix, the package
// path, and the timing — that underestimated post-tab col positions
// by up to 7. The terminal's REAL wraps fired at different points
// than the model expected, the rolling-tail row count drifted off
// the truth, and Done's CursorUp landed inside the live tail
// instead of on the spinner header.
//
// This test exercises a width where the fix flips a 1-row line into
// a 2-row line (i.e., where the post-tab col difference crosses the
// width threshold), so it caught the bug class directly.
func TestSoloIngestTabAdvancesToTabStop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		width    int
		content  string // no trailing \n; the test appends one
		wantRows int    // physical rows that the LOGICAL line occupies
	}{
		{
			// "ok" col 2, "\t" → col 8, 40 chars → col 48. width 45
			// → wraps mid-content. 2 rows.
			// Pre-fix: "\t" treated as 1 col → col 3, 40 chars → col 43.
			// No wrap. 1 row. Off by 1.
			name:     "tab_pushes_line_over_width",
			width:    45,
			content:  "ok\t" + strings.Repeat("a", 40),
			wantRows: 2,
		},
		{
			// Two tabs interleaved with text — mirrors `go test`
			// output shape. width=45, "ok\t" col 8, 37 chars → col
			// 45 pending wrap, next char (the 't') wraps, 'term' → col
			// 4, '\t' → col 8 (next 8-stop on new row), '0.002s'
			// → col 14, \n commits. Total 2 rows for the logical line.
			name:     "go_test_line_shape",
			width:    45,
			content:  "ok\t" + "github.com/jack-work/figaro/internal/term" + "\t" + "0.002s",
			wantRows: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf safeBuf
			s := newToolSoloState(&buf, "test", "")
			s.maxRows = 1000 // disable rolling-tail eviction for this test
			for _, b := range []byte(tc.content + "\n") {
				s.ingest(b, tc.width)
			}
			if s.committedRows != tc.wantRows {
				t.Fatalf("committedRows=%d, want %d (width=%d, content=%q)",
					s.committedRows, tc.wantRows, tc.width, tc.content)
			}
		})
	}
}

// Regression: a tool argument with embedded newlines (heredoc-style
// `git commit -m '...\n...'`) used to expand the header onto multiple
// terminal rows, breaking the 1-row-header assumption and desyncing
// every subsequent cursor walk. formatHeader sanitizes the detail
// (newlines → spaces) so this never happens regardless of the input.
func TestSoloMultilineDetailStaysOneRow(t *testing.T) {
	const multiline = `git commit -m 'jsonrpc: typed errors carry Data; pass *Error through verbatim
Handler returns of type *jsonrpc.Error are now passed through.'`
	var buf safeBuf
	s := newToolSoloState(&buf, "bash", multiline)
	s.Start()
	s.Write([]byte("[main abc1234] msg\n"))
	s.Done(false)

	raw := stripANSI(buf.String())
	if strings.Contains(raw, "verbatim\n") {
		t.Fatalf("multiline detail leaked literal '\\n' into output:\n%s", raw)
	}
}

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
	pending := false // xterm pending-wrap: cursor at col==width, no row break yet
	ensure := func(idx int) {
		for len(rows) <= idx {
			rows = append(rows, nil)
		}
	}
	put := func(b byte) {
		if pending {
			r++
			c = 0
			pending = false
			ensure(r)
		}
		ensure(r)
		row := rows[r]
		for len(row) <= c {
			row = append(row, ' ')
		}
		row[c] = b
		rows[r] = row
		c++
		if width > 0 && c >= width {
			pending = true
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
				pending = false
			case 'B':
				r += n
				ensure(r)
				pending = false
			case 'K':
				ensure(r)
				rows[r] = nil
				c = 0
				pending = false
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
			pending = false
			ensure(r)
		case '\r':
			c = 0
			pending = false
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

