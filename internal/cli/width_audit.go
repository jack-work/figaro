package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// The width audit: figaro tells us when it writes past the edge, in the
// reporter's own terminal.
//
// WHY THIS EXISTS. A right-edge overflow was reported three times and could not
// be reproduced from outside. Every external instrument tried first answered a
// blurrier question than the one asked:
//
//   - a tmux sweep of `show`, the pager and a live turn was clean at every
//     width from 20 to 200, at ~30 seconds per width;
//   - `capture-pane -J` rejoins a wrapped line, so its "worst offender" turned
//     out to be 120 cells of trailing PADDING, not text;
//   - a captured pane also holds rows frozen at a PREVIOUS width and the
//     shell's own echo, neither of which figaro wrote at the current width.
//
// An instrument that cannot separate what figaro wrote from what the terminal
// remembers cannot convict figaro. So the detector moves inside: it sees the
// bytes at the moment they are written, and it knows the width they were
// written for. It costs one width measurement per row and runs only when asked.
//
//	FIGARO_WIDTH_AUDIT=1                 report to stderr
//	FIGARO_WIDTH_AUDIT=/tmp/audit.log    report to a file (recommended: stderr
//	                                     is inside the region being painted)
//
// Every report names the width, the overrun, and the row verbatim, so the next
// question is "which surface produced THAT" rather than "does it happen".
type widthAudit struct {
	inner   interface{ Write([]byte) (int, error) }
	size    func() (int, int)
	mu      sync.Mutex
	sink    *os.File
	seen    map[string]bool
	nreport int
}

// auditWriter wraps out when FIGARO_WIDTH_AUDIT is set, and returns out
// untouched otherwise — the audit must cost nothing when it is off.
func auditWriter(out interface{ Write([]byte) (int, error) }, size func() (int, int)) interface {
	Write([]byte) (int, error)
} {
	dest := strings.TrimSpace(os.Getenv("FIGARO_WIDTH_AUDIT"))
	if dest == "" {
		return out
	}
	sink := os.Stderr
	if dest != "1" && dest != "true" {
		if f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			sink = f
		}
	}
	fmt.Fprintf(sink, "\n=== figaro width audit armed %s ===\n", time.Now().Format(time.RFC3339))
	return &widthAudit{inner: out, size: size, sink: sink, seen: map[string]bool{}}
}

func (a *widthAudit) Write(p []byte) (int, error) {
	a.check(string(p))
	return a.inner.Write(p)
}

// check splits a write into the rows it paints and measures each one.
//
// SPLITTING IS THE WHOLE DIFFICULTY. The incipit separates rows with CR-LF, but
// the pager positions each row with CUP (ESC [ row ; col H) and never emits a
// newline at all — so splitting on CR-LF alone measured an entire FRAME as one
// row and reported 1,634 cells in a 100-column terminal. That is a broken
// instrument reporting a spectacular bug, which is worse than reporting
// nothing: it buries the one real hit (a 128-cell status rule) under noise.
//
// So a row ends at CR-LF, at a bare CR, or at any cursor-positioning escape.
// Everything between those is what lands on one line of the screen.
func (a *widthAudit) check(s string) {
	w, _ := a.size()
	if w <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.nreport > 200 {
		return // a broken frame would otherwise report forever
	}
	for _, row := range splitPaintedRows(s) {
		if row == "" {
			continue
		}
		ink := strings.TrimRight(stripEsc(row), " ")
		inkW := displayWidth(ink)
		padW := displayWidth(stripEsc(row))
		if inkW <= w && padW <= w {
			continue
		}
		key := fmt.Sprintf("%d:%d:%.40s", w, inkW, ink)
		if a.seen[key] {
			continue
		}
		a.seen[key] = true
		a.nreport++
		kind := "PADDING"
		if inkW > w {
			kind = "INK"
		}
		fmt.Fprintf(a.sink, "OVER %s: width=%d ink=%d pad=%d (+%d)\n  %q\n",
			kind, w, inkW, padW, max(inkW, padW)-w, ink)
	}
}

// stripEsc removes ANSI escapes so what remains is what occupies columns.
func stripEsc(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j, _ := escapeEnd(s, i)
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// splitPaintedRows cuts a write into the pieces that land on separate screen
// lines: CR-LF, a bare CR, and the cursor-motion escapes the pager uses to
// place each row (CUP/HVP `H`/`f`, and vertical moves `A`/`B`/`E`/`F`).
func splitPaintedRows(s string) []string {
	var rows []string
	var cur strings.Builder
	flush := func() {
		rows = append(rows, cur.String())
		cur.Reset()
	}
	for i := 0; i < len(s); {
		if s[i] == '\r' {
			flush()
			i++
			if i < len(s) && s[i] == '\n' {
				i++
			}
			continue
		}
		if s[i] == '\n' {
			flush()
			i++
			continue
		}
		if s[i] == 0x1b {
			j, _ := escapeEnd(s, i)
			seq := s[i:j]
			if n := len(seq); n > 0 {
				switch seq[n-1] {
				case 'H', 'f', 'A', 'B', 'E', 'F':
					flush()
					i = j
					continue
				}
			}
			cur.WriteString(seq)
			i = j
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	flush()
	return rows
}

// auditRows reports rows a NON-painter surface is about to print. `figaro show`
// writes straight to stdout rather than through the Terminal, so the writer
// wrapper never sees it — and it renders the same nodes the pager does, which
// makes it exactly the kind of gap an audit is supposed not to have.
func auditRows(rows []string, width int, surface string) {
	dest := strings.TrimSpace(os.Getenv("FIGARO_WIDTH_AUDIT"))
	if dest == "" || width <= 0 {
		return
	}
	sink := os.Stderr
	if dest != "1" && dest != "true" {
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		sink = f
	}
	for i, row := range rows {
		ink := strings.TrimRight(stripEsc(row), " ")
		if n := displayWidth(ink); n > width {
			fmt.Fprintf(sink, "OVER INK [%s]: width=%d ink=%d (+%d) row=%d\n  %q\n",
				surface, width, n, n-width, i, ink)
		}
	}
}
