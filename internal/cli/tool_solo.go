package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

// soloDebug writes one line to /tmp/figaro_solo.log per significant
// solo state event. Enabled when FIGARO_SOLO_DEBUG=1. No locking —
// solo's caller already holds s.mu.
func soloDebug(format string, args ...any) {
	if os.Getenv("FIGARO_SOLO_DEBUG") != "1" {
		return
	}
	f, err := os.OpenFile("/tmp/figaro_solo.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

// toolSoloState animates the header of a single tool call.
// Start -> Freeze (on first output) -> Done. All state under mu.
//
// The header is always one terminal row by construction: formatHeader
// truncates its detail string so the painted line fits within
// term.Width(). That lets repaint use a single CursorUp/EraseLine
// rather than walking a variable number of rows.
//
// Position tracking: rowsBelow + cursor.col describe where the cursor
// sits relative to the header. Write feeds streamed bytes through a
// VT-aware cursor so wrapped lines bump rowsBelow correctly — without
// that, terminal-driven wrap silently desynced the up-walk and left
// the running spinner header on screen alongside the final ✓ header.

type toolSoloState struct {
	out    io.Writer
	name   string
	detail string

	mu     sync.Mutex
	frame  int
	state  toolRowState // running / OK / err
	frozen bool         // true once cursor is no longer above the header

	// rowsBelow + cursor track physical rows / column relative to the
	// row just below the header. Mutated by Write; consulted by repaint
	// to compute the up-walk distance.
	rowsBelow int
	cursor    vtCursor

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// stopTicker stops the spinner goroutine. Idempotent.
func (s *toolSoloState) stopTicker() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh
	})
}

// StopTicker pauses the spinner without finalizing.
func (s *toolSoloState) StopTicker() { s.stopTicker() }

// newToolSoloState prepares a solo state.
func newToolSoloState(out io.Writer, name, detail string) *toolSoloState {
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}
	return &toolSoloState{
		out:    out,
		name:   name,
		detail: detail,
		state:  toolRowRunning,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// UpdateHeader replaces name and detail, forces repaint. No-op
// once frozen.
func (s *toolSoloState) UpdateHeader(name, detail string) {
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return
	}
	s.name = name
	s.detail = detail
	s.repaintHeaderLocked()
}

// UpdateDetail replaces the detail string and repaints. No-op
// once frozen.
func (s *toolSoloState) UpdateDetail(detail string) {
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return
	}
	s.detail = detail
	s.repaintHeaderLocked()
}

// AddRowsBelow tells solo that n rows of foreign output were rendered
// below its header. Used by a wrapping batch so its rows participate
// in solo's repaint cursor math without duplicating the bookkeeping.
func (s *toolSoloState) AddRowsBelow(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowsBelow += n
}

// RepaintAtFrame advances the spinner frame and repaints. Used by a
// foreign ticker (wrapping batch) that has taken over solo's
// animation. No-op once frozen.
func (s *toolSoloState) RepaintAtFrame(frame int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return
	}
	s.frame = frame
	s.repaintHeaderLocked()
}

// Start prints the header and launches the spinner.
func (s *toolSoloState) Start() {
	s.mu.Lock()
	fmt.Fprintln(s.out)
	fmt.Fprintln(s.out, s.formatHeader())
	s.mu.Unlock()
	go s.tick()
}

// Freeze stops the spinner and locks the header in place.
// Idempotent.
func (s *toolSoloState) Freeze() {
	s.mu.Lock()
	if s.frozen {
		s.mu.Unlock()
		return
	}
	s.frozen = true
	s.mu.Unlock()

	s.stopTicker()

	s.mu.Lock()
	s.repaintHeaderLocked()
	s.mu.Unlock()
}

// Done marks the tool as completed and repaints the header with the
// final glyph (✓ / ✗). Safe whether or not output streamed first,
// and whether or not Freeze was called explicitly.
func (s *toolSoloState) Done(isError bool) {
	s.mu.Lock()
	if isError {
		s.state = toolRowErr
	} else {
		s.state = toolRowOK
	}
	s.frozen = true
	s.mu.Unlock()

	s.stopTicker()

	s.mu.Lock()
	s.repaintHeaderLocked()
	s.mu.Unlock()
}

// tick is the spinner goroutine.
func (s *toolSoloState) tick() {
	defer close(s.doneCh)
	t := time.NewTicker(spinnerTick)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.mu.Lock()
			if s.frozen {
				s.mu.Unlock()
				return
			}
			s.frame++
			s.repaintHeaderLocked()
			s.mu.Unlock()
		}
	}
}

// repaintHeaderLocked is the single header-repaint primitive. Walks
// the cursor up to the header row, erases it, prints the current
// header, then returns the cursor to col 0 on the row strictly below
// all output. Caller holds mu.
//
// Invariant: the header is one terminal row (formatHeader truncates).
// If cursor is mid-row when called, an extra '\n' is emitted first so
// the up-walk starts from col 0 — that newline counts as an output
// row, so rowsBelow is bumped to match.
func (s *toolSoloState) repaintHeaderLocked() {
	if s.cursor.col != 0 {
		fmt.Fprint(s.out, "\n")
		s.rowsBelow++
		s.cursor.col = 0
	}
	up := s.rowsBelow + 1
	soloDebug("repaint state=%d frozen=%v rowsBelow=%d col=%d width=%d up=%d",
		s.state, s.frozen, s.rowsBelow, s.cursor.col, term.Width(), up)
	fmt.Fprintf(s.out, "%s%s%s\r%s",
		term.CursorUp(up),
		term.EraseLine,
		s.formatHeader(),
		term.CursorDown(up))
}

// Write streams tool output. Each byte is fed through a VT-aware
// cursor so wrap, ANSI escape sequences, and UTF-8 continuation
// bytes are accounted for when bumping rowsBelow. term.Width is
// re-read on every call so a mid-stream resize at least gets the
// next chunk's wrap math right.
func (s *toolSoloState) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	beforeRows := s.rowsBelow
	beforeCol := s.cursor.col
	n, err := s.out.Write(p)
	width := term.Width()
	for i := 0; i < n; i++ {
		s.rowsBelow += s.cursor.advance(p[i], width)
	}
	soloDebug("write len=%d width=%d rowsBelow %d->%d col %d->%d",
		n, width, beforeRows, s.rowsBelow, beforeCol, s.cursor.col)
	return n, err
}

// formatHeader returns the header line without trailing newline.
// Truncates detail to the current terminal width so the result
// always renders in one row.
func (s *toolSoloState) formatHeader() string {
	var icon string
	var colorFn func(string) string
	switch s.state {
	case toolRowRunning:
		icon = string(spinnerFrames[s.frame%len(spinnerFrames)])
		colorFn = term.Cyan
	case toolRowOK:
		icon = "✓"
		colorFn = term.Green
	case toolRowErr:
		icon = "✗"
		colorFn = term.Red
	default:
		icon = "▶"
		colorFn = term.Dim
	}
	// Skeleton: "─── X ▶ name · detail ───"
	// Overhead: 4 (lead) + 1 (icon) + 3 (" ▶ ") + 3 (" · ") + 4 (trail) = 15
	const overhead = 15
	width := term.Width()
	nameRunes := len([]rune(s.name))
	avail := width - overhead - nameRunes

	detail := s.detail
	if avail < 4 {
		detail = ""
	} else if len([]rune(detail)) > avail {
		detail = string([]rune(detail)[:avail-1]) + "…"
	}

	header := term.Dim("─── ") + colorFn(icon) + term.Dim(" ▶ "+s.name)
	if detail != "" {
		header += term.Dim(" · " + detail)
	}
	header += term.Dim(" ───")
	return header
}

// vtCursor tracks the column position of a stream of bytes as they
// would render on a terminal of a given width. Used by toolSoloState
// to count physical rows (including terminal-driven wrap) so the
// header repaint walks the right number of rows up.
//
// The state machine is byte-oriented and matches xterm's pending-wrap
// behavior: filling the last column of a row leaves the cursor in a
// "pending wrap" state at col == width; the row-break only fires on
// the next *visible* byte (not on '\n', which just moves down and
// clears pending). Without this, a line whose length is exactly a
// multiple of width would over-count by one row per wrap point.
//
// Other minimal rules:
//   - ESC starts an escape sequence; the first ASCII letter ends it.
//     (Same heuristic as term.VisibleLen.)
//   - '\r' resets col and clears pending wrap.
//   - '\n' resets col, clears pending wrap, produces one row break.
//   - UTF-8 continuation bytes (10xxxxxx) don't advance col — only
//     the leading byte of a rune does. Ignores east-asian-wide runes;
//     close-enough for header math.
type vtCursor struct {
	col   int
	inEsc bool
}

// advance ingests one byte and returns the number of physical row
// breaks it produced (0 or 1).
func (c *vtCursor) advance(b byte, width int) int {
	if c.inEsc {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			c.inEsc = false
		}
		return 0
	}
	switch b {
	case 0x1b:
		c.inEsc = true
		return 0
	case '\n':
		c.col = 0
		return 1
	case '\r':
		c.col = 0
		return 0
	}
	if b&0xc0 == 0x80 {
		return 0
	}
	rows := 0
	if c.col >= width {
		c.col = 0
		rows = 1
	}
	c.col++
	return rows
}
