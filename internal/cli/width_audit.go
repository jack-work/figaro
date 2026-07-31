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
// Rows are separated by CR-LF in everything the renderer emits. A row may also
// carry cursor motion, which is not ink and does not advance the column — so
// the measurement strips escapes and counts CELLS, the mistake that has been
// made three separate ways on this branch (bytes, runes, and StringWidth over
// an SGR run).
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
	for _, row := range strings.Split(s, "\r\n") {
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
