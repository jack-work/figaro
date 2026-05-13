package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

// toolSoloState animates the header of a single tool call.
// Start -> Freeze (on first output) -> Done. All state under mu.
//

type toolSoloState struct {
	out    io.Writer
	name   string
	detail string

	mu     sync.Mutex
	frame  int
	state  toolRowState // running / OK / err
	frozen bool         // true once cursor is no longer above the header

	// headerRows: how many terminal rows the header spans (wrapping).
	headerRows int

	// linesBelow + lastWasNL let Done find the header for rewrite.
	linesBelow int
	lastWasNL  bool

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

// RepaintWrappedHeader rewrites the header from another ticker's
// context when this solo wraps a batch. No-op once frozen.
func (s *toolSoloState) RepaintWrappedHeader(rowsBelow, frame int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return
	}
	s.frame = frame
	fmt.Fprintf(s.out, "%s%s%s\r",
		term.CursorUp(rowsBelow+1)+term.EraseLine,
		s.formatHeader(),
		term.CursorDown(rowsBelow+1))
}

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
		lastWasNL: true,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
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

// FinalizeWithRowsBelow stops the ticker, walks up past rowsBelow
// rows to rewrite the header, then walks back down.
//
// TODO: this and RepaintWrappedHeader still use bespoke cursor math
// because rowsBelow comes from a wrapping batch, not solo's own
// linesBelow. Migrate to repaintHeaderLocked once the batch path is
// touched (likely by teaching the primitive to accept an explicit
// rowsBelow override).
func (s *toolSoloState) FinalizeWithRowsBelow(rowsBelow int, isError bool) {
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
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "%s%s%s\r",
		term.CursorUp(rowsBelow+1)+term.EraseLine,
		s.formatHeader(),
		term.CursorDown(rowsBelow+1))
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

// Start prints the header and launches the spinner.
func (s *toolSoloState) Start() {
	s.mu.Lock()
	header := s.formatHeader()
	fmt.Fprintln(s.out)
	fmt.Fprintln(s.out, header)
	s.headerRows = term.WrapCount(term.VisibleLen(header), term.Width())
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

// repaintHeaderLocked is the single header-repaint primitive. It
// walks the cursor up past any streamed output (linesBelow rows)
// plus the header's own row count, erases the old header (handling
// wrap), reprints, then walks back to the original position. Caller
// holds mu.
//
// Invariant the callers must uphold: when no output has streamed,
// linesBelow==0 and lastWasNL==true; the cursor sits one row below
// the header. After Write streams chunks, linesBelow tracks the
// number of \n bytes emitted and lastWasNL tracks the trailing
// byte.
func (s *toolSoloState) repaintHeaderLocked() {
	extraDown := 0
	if !s.lastWasNL {
		fmt.Fprint(s.out, "\n")
		s.lastWasNL = true
		extraDown = 1
	}
	oldRows := s.headerRows
	if oldRows < 1 {
		oldRows = 1
	}
	totalDown := s.linesBelow + extraDown

	// Walk up to the first row of the (old) header.
	fmt.Fprint(s.out, term.CursorUp(totalDown+oldRows))
	// Erase each old header row. Cursor is at col 0 after the up
	// (terminal cursor moves preserve column, and we entered this
	// path from col 0 — either after a \n or after the \r below).
	for i := 0; i < oldRows; i++ {
		fmt.Fprint(s.out, term.EraseLine)
		if i < oldRows-1 {
			fmt.Fprint(s.out, "\n")
		}
	}
	// Cursor is on the LAST old header row; move back to the first.
	if oldRows > 1 {
		fmt.Fprint(s.out, term.CursorUp(oldRows-1))
	}
	header := s.formatHeader()
	fmt.Fprint(s.out, header)
	newRows := term.WrapCount(term.VisibleLen(header), term.Width())
	s.headerRows = newRows
	// After printing, cursor sits on the last header row. Return to
	// col 0 on the row strictly below all output.
	fmt.Fprintf(s.out, "\r%s", term.CursorDown(totalDown+1))
}

// Write streams tool output, tracking newlines for cursor math.
func (s *toolSoloState) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.out.Write(p)
	for i := 0; i < n; i++ {
		if p[i] == '\n' {
			s.linesBelow++
			s.lastWasNL = true
		} else {
			s.lastWasNL = false
		}
	}
	return n, err
}

// formatHeader returns the header line without trailing newline.
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
	// Truncate detail to terminal width. Skeleton: "─── X ▶ name · detail ───"
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
