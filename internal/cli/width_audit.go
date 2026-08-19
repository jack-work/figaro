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
	// cursor under us, so a column carried across one is meaningless, and
	// reporting it produced empty-ink "overruns" on every resize, which is a
	// detector crying wolf at the exact moment the user is looking.
	lastW int
	// col is the cursor column CARRIED ACROSS WRITES. A row is not one write:
	// the incipit appends streaming deltas, so resetting the column per call
	// measured a long row in innocent-looking pieces and reported nothing.
	col int
}

// auditWriter wraps out unless the audit is switched off. See the type's doc
// for why it is on by default.
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

// auditRows reports rows a NON-painter surface is about to print. `figaro show`
// writes straight to stdout rather than through the Terminal, so the writer
// wrapper never sees it, and it renders the same nodes the pager does, which
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
