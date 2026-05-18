package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

// toolSoloState renders one tool call as a fixed-size live region:
// a spinner header on top, followed by an in-place rolling tail of
// the most recent N physical lines of streamed output. Once the tail
// fills, older lines drop off as new ones arrive — so the region's
// total height never exceeds the viewport. Without that cap, output
// taller than the viewport would scroll the spinner header into
// scrollback where CursorUp can no longer reach it.
//
// Lifecycle: Start → (UpdateDetail|tick)* → Freeze → Write* → Done.
// All state under mu.
//
// In wrapped mode (a parallel batch reuses solo as its header anchor)
// solo doesn't own a tail — the batch paints rows directly below the
// header and tells solo via AddRowsBelow. Solo's repaint stays out of
// the batch's row range and just rewrites the header in place.

type toolSoloState struct {
	out    io.Writer
	name   string
	detail string

	mu     sync.Mutex
	frame  int
	state  toolRowState
	frozen bool

	// Rolling tail (standalone mode only).
	maxRows int      // tail capacity; min(viewport-3, 0) capped to 0+
	tail    []string // committed physical lines, len ≤ maxRows
	partial []byte   // current in-progress line (no terminator yet)
	cursor  vtCursor // tracks col / inEsc for wrap + ANSI handling

	// liveRows: physical rows below the header currently painted on
	// screen. Standalone: derived from the last repaint. Wrapped:
	// incremented by AddRowsBelow from the batch.
	liveRows int

	// wrapped: a batch owns the rows below the header. Repaints only
	// touch the header row; the rest is the batch's domain.
	wrapped bool

	// Stats chip: elapsed + size live in the header. Ticker repaint
	// keeps the elapsed counter advancing once per spinnerTick.
	startedAt     time.Time // set in Start
	endedAt       time.Time // set in Done
	totalBytes    int       // bytes ingested via Write (pre-truncation)
	committedRows int       // physical lines committed (total, not just visible)

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// newToolSoloState prepares a solo state. maxRows is derived from
// term.Height(): the live region is capped to leave the header and at
// least one row of cursor breathing room below the tail. A tiny
// viewport collapses to header-only (maxRows = 0).
func newToolSoloState(out io.Writer, name, detail string) *toolSoloState {
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}
	h := term.Height()
	maxRows := h - 3
	if maxRows < 0 {
		maxRows = 0
	}
	return &toolSoloState{
		out:     out,
		name:    name,
		detail:  detail,
		state:   toolRowRunning,
		maxRows: maxRows,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

func (s *toolSoloState) stopTicker() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh
	})
}

// StopTicker pauses the spinner without finalizing.
func (s *toolSoloState) StopTicker() { s.stopTicker() }

// UpdateHeader replaces name and detail, forces repaint. Used by
// stream.go to repurpose the solo as a batch wrapper: this also
// flips solo into wrapped mode so subsequent repaints don't trample
// the batch's rows.
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
	s.wrapped = true
	s.repaintLocked()
}

// UpdateDetail replaces the detail string and repaints.
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
	s.repaintLocked()
}

// AddRowsBelow tells solo that n rows of foreign content were just
// rendered below the header (by a wrapping batch). Used so solo's
// header repaint walks the right distance back up.
func (s *toolSoloState) AddRowsBelow(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveRows += n
}

// RepaintAtFrame is called by a foreign ticker (wrapping batch) to
// drive the spinner animation while solo's own ticker is stopped.
func (s *toolSoloState) RepaintAtFrame(frame int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return
	}
	s.frame = frame
	s.repaintLocked()
}

// Start prints the initial header. Cursor lands one row below.
func (s *toolSoloState) Start() {
	s.mu.Lock()
	s.startedAt = time.Now()
	fmt.Fprintln(s.out)
	fmt.Fprintln(s.out, s.formatHeader())
	s.mu.Unlock()
	go s.tick()
}

// Freeze stops the spinner and locks the header in place. Idempotent.
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
	s.repaintLocked()
	s.mu.Unlock()
}

// Done marks the tool complete and repaints with the final glyph.
func (s *toolSoloState) Done(isError bool) {
	s.mu.Lock()
	if isError {
		s.state = toolRowErr
	} else {
		s.state = toolRowOK
	}
	s.endedAt = time.Now()
	s.frozen = true
	s.mu.Unlock()
	s.stopTicker()
	s.mu.Lock()
	s.repaintLocked()
	s.mu.Unlock()
}

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
			// Header-only repaint: the spinner glyph changes but the
			// tail content (if any) is whatever the last Write left
			// on screen, so there's no need to redraw it every tick.
			s.repaintHeaderOnlyLocked()
			s.mu.Unlock()
		}
	}
}

// Write streams tool output. Each byte feeds the line buffer via a
// VT-aware cursor; \n and width-driven wraps both commit a line. The
// committed-lines ring drops the oldest when full. Repaints the live
// region in place at end of call.
func (s *toolSoloState) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalBytes += len(p)
	width := term.Width()
	for _, b := range p {
		s.ingest(b, width)
	}
	s.repaintLocked()
	return len(p), nil
}

// ingest feeds one byte through the line buffer. Handles wrap, ANSI
// escapes, \n, \r, and UTF-8 continuation bytes.
func (s *toolSoloState) ingest(b byte, width int) {
	if s.cursor.inEsc {
		s.partial = append(s.partial, b)
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			s.cursor.inEsc = false
		}
		return
	}
	switch b {
	case 0x1b:
		s.cursor.inEsc = true
		s.partial = append(s.partial, b)
		return
	case '\n':
		s.commitLine()
		return
	case '\r':
		// Cursor visually resets to col 0; keep partial bytes so far.
		// Real terminals would overwrite on subsequent chars; for our
		// live tail that's close enough.
		s.cursor.col = 0
		return
	}
	if b&0xc0 == 0x80 {
		s.partial = append(s.partial, b)
		return
	}
	if s.cursor.col >= width {
		s.commitLine()
	}
	s.partial = append(s.partial, b)
	s.cursor.col++
}

// commitLine pushes the current partial as a finished line onto the
// tail ring (dropping oldest if at capacity), and resets state for
// the next line.
func (s *toolSoloState) commitLine() {
	line := string(s.partial)
	s.partial = s.partial[:0]
	s.cursor.col = 0
	s.committedRows++
	if s.maxRows == 0 {
		return
	}
	if len(s.tail) >= s.maxRows {
		copy(s.tail, s.tail[1:])
		s.tail[len(s.tail)-1] = line
	} else {
		s.tail = append(s.tail, line)
	}
}

// repaintLocked is the canonical paint. In wrapped mode it only
// rewrites the header. In standalone mode it rewrites the header
// followed by the visible tail (committed lines + the current
// partial as a bottom row). The cursor returns to "below tail" =
// 1 row below all painted rows. Caller holds mu.
func (s *toolSoloState) repaintLocked() {
	if s.wrapped {
		s.repaintHeaderOnlyLocked()
		return
	}
	visible := s.visibleTail()
	newLiveRows := len(visible)

	// Walk up past the live region to the header row.
	fmt.Fprint(s.out, term.CursorUp(s.liveRows+1))
	fmt.Fprintf(s.out, "%s%s\r", term.EraseLine, s.formatHeader())

	for _, line := range visible {
		fmt.Fprintf(s.out, "\n%s%s", term.EraseLine, line)
	}
	// If the tail shrunk (rare; e.g., \r reset partial), erase any
	// leftover row from the previous paint so it doesn't ghost on
	// screen.
	for i := newLiveRows; i < s.liveRows; i++ {
		fmt.Fprintf(s.out, "\n%s", term.EraseLine)
	}
	paintedRows := newLiveRows
	if s.liveRows > paintedRows {
		paintedRows = s.liveRows
	}
	// Cursor sits at end of last printed row. Move to col 0 of the
	// row strictly below all painted rows. \r + \n works whether or
	// not the row ended in a pending-wrap.
	fmt.Fprint(s.out, "\r\n")
	_ = paintedRows
	s.liveRows = newLiveRows
}

// repaintHeaderOnlyLocked walks up past the live region, rewrites
// the header in place, and returns the cursor without touching any
// content rows. Used for the wrapped-batch case.
func (s *toolSoloState) repaintHeaderOnlyLocked() {
	up := s.liveRows + 1
	fmt.Fprintf(s.out, "%s%s%s\r%s",
		term.CursorUp(up),
		term.EraseLine,
		s.formatHeader(),
		term.CursorDown(up))
}

// visibleTail returns the lines to paint below the header. The
// in-progress partial appears as the bottom row when non-empty,
// displacing the oldest line if the ring is already full.
func (s *toolSoloState) visibleTail() []string {
	if s.maxRows == 0 {
		return nil
	}
	if len(s.partial) == 0 {
		return s.tail
	}
	if len(s.tail) < s.maxRows {
		out := make([]string, len(s.tail)+1)
		copy(out, s.tail)
		out[len(s.tail)] = string(s.partial)
		return out
	}
	out := make([]string, s.maxRows)
	copy(out, s.tail[1:])
	out[s.maxRows-1] = string(s.partial)
	return out
}

// formatHeader returns the header line without trailing newline.
// Truncates detail to the current terminal width so the result
// always renders in one row. Format:
//
//	─── X ▶ name · detail · (elapsed, size) ───
//
// where the stats chip is omitted before Start, and the detail
// is truncated to whatever room is left after reserving space for
// name + chip + box-drawing chrome.
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
	chip := s.statChipLocked()

	// Baseline overhead: 4 ("─── ") + 1 (icon) + 3 (" ▶ ") + 3
	// (" · " before detail) + 4 (" ───") = 15. Chip adds another
	// 3 (" · ") plus its own length.
	const baseOverhead = 15
	width := term.Width()
	nameRunes := len([]rune(s.name))
	chipRunes := len([]rune(chip))
	chipOverhead := 0
	if chip != "" {
		chipOverhead = 3 + chipRunes
	}
	avail := width - baseOverhead - chipOverhead - nameRunes

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
	if chip != "" {
		header += term.Dim(" · " + chip)
	}
	header += term.Dim(" ───")
	return header
}

// statChipLocked returns the stats chip rendered into the header:
// running tools get "(elapsed, bytes)", completed tools get
// "(elapsed, lines)". Empty string before Start (when startedAt is
// zero). Caller holds mu.
func (s *toolSoloState) statChipLocked() string {
	if s.startedAt.IsZero() {
		return ""
	}
	switch s.state {
	case toolRowRunning:
		elapsed := time.Since(s.startedAt)
		return fmt.Sprintf("(%s, %s)", formatRowElapsed(elapsed), formatBytes(s.totalBytes))
	case toolRowOK, toolRowErr:
		end := s.endedAt
		if end.IsZero() {
			end = time.Now()
		}
		lines := s.committedRows
		if len(s.partial) > 0 {
			lines++
		}
		return fmt.Sprintf("(%s, %s)", formatRowElapsed(end.Sub(s.startedAt)), pluralLines(lines))
	}
	return ""
}

// vtCursor: minimal byte-level VT cursor used by the line-buffer
// ingest. Tracks col with pending-wrap semantics (filling the last
// column leaves the cursor at col == width; the wrap-event fires on
// the next visible byte). ANSI escape sequences and UTF-8
// continuation bytes are visible-width zero.
type vtCursor struct {
	col   int
	inEsc bool
}
