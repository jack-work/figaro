package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// lazy opens the default report file on the FIRST overrun, so a healthy
	// session never creates one.
	lazy func() *os.File
	// lastW is the width the carried column belongs to. A resize moves the
	// cursor under us, so a column carried across one is meaningless — and
	// reporting it produced empty-ink "overruns" on every resize, which is a
	// detector crying wolf at the exact moment the user is looking.
	lastW int
	// col is the cursor column CARRIED ACROSS WRITES. A row is not one write:
	// the incipit appends streaming deltas, so resetting the column per call
	// measured a long row in innocent-looking pieces and reported nothing.
	col int
}

// auditWriter wraps out whenever there is somewhere to report to.
//
// ALWAYS ON, BY DEFAULT, AND THIS IS THE POINT. A right-edge overflow has been
// reported four times and reproduced from another machine exactly once. Asking
// the reporter to set an env var, reproduce on demand and send a log is three
// chances to lose the evidence; figaro can simply keep the receipt itself.
//
// The cost is one cell-count per emitted row, capped at 20 reports per process
// and de-duplicated, written to <cache>/width-overruns.log. FIGARO_WIDTH_AUDIT
// still redirects it to stderr or a chosen file for a deliberate hunt, and
// FIGARO_WIDTH_AUDIT=off disables it outright.
func auditWriter(out interface{ Write([]byte) (int, error) }, size func() (int, int)) interface {
	Write([]byte) (int, error)
} {
	dest := strings.TrimSpace(os.Getenv("FIGARO_WIDTH_AUDIT"))
	if dest == "off" || dest == "0" {
		return out
	}
	var sink *os.File
	switch {
	case dest == "":
		// The default: a quiet receipt beside the cache, opened lazily so an
		// ordinary run touches no file at all.
		return &widthAudit{inner: out, size: size, seen: map[string]bool{}, lazy: defaultOverrunLog}
	case dest == "1" || dest == "true":
		sink = os.Stderr
	default:
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return out
		}
		sink = f
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
	if w != a.lastW {
		a.lastW, a.col = w, 0 // the terminal clamped the cursor; so do we
	}
	cap := 20
	if a.lazy == nil {
		cap = 200 // a deliberate hunt wants everything
	}
	if a.nreport > cap {
		return // a broken frame would otherwise report forever
	}
	// THE COLUMN A ROW STARTS AT IS PART OF ITS WIDTH.
	//
	// This audit began by measuring rows as though each started at column 1,
	// which is a second way to be blind: a row emitted from a stale cursor
	// column overflows by exactly the offset, and both the row and the width
	// look innocent on their own. So the write is replayed as a cursor —
	// CR/CUP set the column, text advances it — and what is reported is where
	// the row ENDS.
	for _, row := range splitPaintedRowsAt(s, &a.col) {
		if row.text == "" {
			continue
		}
		ink := strings.TrimRight(stripEsc(row.text), " ")
		if ink == "" {
			continue // nothing visible: an empty row cannot be past the edge
		}
		inkW := row.col + displayWidth(ink)
		padW := row.col + displayWidth(stripEsc(row.text))
		if inkW <= w && padW <= w {
			continue
		}
		key := fmt.Sprintf("%d:%d:%d:%.40s", w, row.col, inkW, ink)
		if a.seen[key] {
			continue
		}
		if a.sink == nil {
			if a.lazy == nil {
				return
			}
			f := a.lazy()
			if f == nil {
				return
			}
			a.sink = f
			fmt.Fprintf(a.sink, "\n=== figaro %s, %s ===\n", buildRevision(), time.Now().Format(time.RFC3339))
		}
		a.seen[key] = true
		a.nreport++
		kind := "PADDING"
		if inkW > w {
			kind = "INK"
		}
		fmt.Fprintf(a.sink, "OVER %s: width=%d ioctl=%d startcol=%d ends=%d pad=%d (+%d)\n  %q\n  from: %s\n",
			kind, w, termWidth(), row.col, inkW, padW, max(inkW, padW)-w, ink, auditCallers())
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

// paintedRow is a run of text plus the column it began at.
type paintedRow struct {
	col  int
	text string
}

// splitPaintedRowsAt replays a write as a cursor would see it: CR returns to
// column 0, CUP/HVP set it, a vertical move starts a new row at the current
// column, and text advances it. *col carries the position across writes,
// because a frame is not emitted in one call.
func splitPaintedRowsAt(s string, col *int) []paintedRow {
	var rows []paintedRow
	cur := paintedRow{col: *col}
	var b strings.Builder
	flush := func(next int) {
		cur.text = b.String()
		rows = append(rows, cur)
		b.Reset()
		cur = paintedRow{col: next}
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\r':
			flush(0)
			i++
			if i < len(s) && s[i] == '\n' {
				i++
			}
		case s[i] == '\n':
			flush(0)
			i++
		case s[i] == 0x1b:
			j, _ := escapeEnd(s, i)
			seq := s[i:j]
			if n := len(seq); n > 1 {
				switch seq[n-1] {
				case 'H', 'f':
					flush(cupColumn(seq))
					i = j
					continue
				case 'A', 'B', 'E', 'F':
					// E/F return to column 1; A/B keep the column.
					next := *col + displayWidth(stripEsc(b.String()))
					if seq[n-1] == 'E' || seq[n-1] == 'F' {
						next = 0
					}
					flush(next)
					i = j
					continue
				}
			}
			b.WriteString(seq)
			i = j
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	cur.text = b.String()
	rows = append(rows, cur)
	*col = cur.col + displayWidth(stripEsc(cur.text))
	return rows
}

// cupColumn reads the column out of ESC [ row ; col H (defaulting to 1).
func cupColumn(seq string) int {
	body := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), string(seq[len(seq)-1]))
	parts := strings.Split(body, ";")
	if len(parts) < 2 {
		return 0
	}
	n := 0
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	if n > 0 {
		return n - 1
	}
	return 0
}

// auditCallers names who emitted the row. Reading the renderer to guess which
// surface produced an over-wide row went three rounds and found nothing; the
// stack answers it outright.
func auditCallers() string {
	pcs := make([]uintptr, 12)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		f, more := frames.Next()
		name := f.Function
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if !strings.Contains(name, "width_audit") {
			out = append(out, fmt.Sprintf("%s:%d", name, f.Line))
		}
		if !more || len(out) >= 6 {
			break
		}
	}
	return strings.Join(out, " <- ")
}

// defaultOverrunLog is where figaro leaves the receipt when nobody asked: a
// single file beside the cache, named so it is obvious what it is and safe to
// delete.
func defaultOverrunLog() *os.File {
	dir := cacheDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "width-overruns.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}
