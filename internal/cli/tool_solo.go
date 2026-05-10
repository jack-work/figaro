package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

// toolSoloState animates the header of a single in-flight tool call.
//
// The lifecycle:
//
//  1. Start() prints "⠋ ▶ bash · pwd" on its own line and launches a
//     ticker goroutine that rewrites that one line on each tick to
//     advance the spinner glyph.
//  2. Freeze() stops the ticker, ensures the line is left in a
//     stable rendered state, and drops a trailing newline so any
//     subsequent tool stdout streams cleanly *below* the header
//     instead of overwriting it. Called the moment the first
//     stream.tool_output chunk arrives — once output is appearing,
//     the user can clearly see the tool is running, and continuing
//     to spin the header would race the cursor against streaming
//     content.
//  3. Done(isError) freezes the spinner if it's still spinning and
//     replaces the running glyph with ✓ or ✗ so the completed
//     state is visible at a glance even after the tool exits.
//
// All state is protected by mu. Only the goroutine that calls Start
// owns the underlying writer, but Freeze/Done can be called from
// the same RPC reader goroutine that originally invoked Start —
// they synchronize via mu and via stop/done channels so the ticker
// has fully exited before Freeze returns.
type toolSoloState struct {
	out    io.Writer
	name   string
	detail string

	mu     sync.Mutex
	frame  int
	state  toolRowState // running / OK / err
	frozen bool         // true once cursor is no longer above the header

	// linesBelow tracks how many newlines of streamed tool output
	// have been written since Freeze. Together with lastWasNL it
	// lets Done compute the exact cursor-up distance to reach the
	// header row and rewrite it in place (replacing the running
	// spinner glyph with ✓ / ✗) instead of stranding a stale
	// spinner above and a duplicate header below.
	linesBelow int
	lastWasNL  bool

	stopCh chan struct{}
	doneCh chan struct{}
}

// newToolSoloState prepares a solo state. Nothing is written until
// Start.
func newToolSoloState(out io.Writer, name, detail string) *toolSoloState {
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}
	return &toolSoloState{
		out:    out,
		name:   name,
		detail: detail,
		state:  toolRowRunning,
		// After Start prints the header and trailing newline the
		// cursor is at column 0 of a fresh line — equivalent state
		// to having just emitted a '\n'.
		lastWasNL: true,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// UpdateDetail replaces the right-hand detail string and forces a
// repaint. Used when an early ToolUseStart created the spinner with
// no detail and a later ToolStart arrives with parsed args. No-op
// once the spinner has been frozen — the header is no longer above
// the cursor at that point and rewriting it would race streamed
// output.
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
	s.rewriteHeaderLocked()
}

// Start prints the initial header line and launches the spinner
// ticker. Call exactly once.
func (s *toolSoloState) Start() {
	s.mu.Lock()
	// Leading blank to match the static header style and breathe.
	fmt.Fprintln(s.out)
	fmt.Fprintln(s.out, s.formatHeader())
	s.mu.Unlock()
	go s.tick()
}

// Freeze stops the spinner and emits a final newline so subsequent
// stdout streams below the header. Idempotent. After Freeze, the
// underlying writer is owned again by the caller.
func (s *toolSoloState) Freeze() {
	s.mu.Lock()
	if s.frozen {
		s.mu.Unlock()
		return
	}
	s.frozen = true
	s.mu.Unlock()

	close(s.stopCh)
	<-s.doneCh

	// Last paint to leave the header in a stable, clean state.
	s.mu.Lock()
	s.rewriteHeaderLocked()
	s.mu.Unlock()
}

// Done marks the tool as completed (success or error). Calling Done
// while the spinner is still running freezes it first.
//
// When the tool produced no output, the header line is the line
// directly above the cursor and rewriteHeaderLocked replaces it in
// place. When output has streamed below the header, Done walks the
// cursor back up linesBelow+1 rows, rewrites the header with the
// completion glyph, then walks the cursor back down so the trailer
// the caller is about to write lands at the right spot. The net
// effect is: the running spinner is *erased* and replaced by ✓ / ✗
// at its original location, even after a chatty tool.
func (s *toolSoloState) Done(isError bool) {
	s.mu.Lock()
	if isError {
		s.state = toolRowErr
	} else {
		s.state = toolRowOK
	}
	wasFrozen := s.frozen
	s.mu.Unlock()
	if !wasFrozen {
		s.Freeze()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.linesBelow == 0 && s.lastWasNL {
		// No output streamed: classic one-line in-place rewrite.
		s.rewriteHeaderLocked()
		return
	}
	// Output streamed. Make sure we start from column 0 on a clean
	// line; if the last byte wasn't a newline we have to push one
	// out ourselves (and account for it in the up-distance).
	up := s.linesBelow
	if !s.lastWasNL {
		fmt.Fprint(s.out, "\n")
		up++
		s.lastWasNL = true
	}
	// up is the count of streamed newlines from the cursor back to
	// the line directly under the header. The header itself sits
	// one row above that, so we go up up+1.
	fmt.Fprintf(s.out, "%s%s%s\r", term.CursorUp(up+1)+term.EraseLine, s.formatHeader(), term.CursorDown(up+1))
}

// tick is the spinner animation goroutine. It advances the frame
// every spinnerTick and rewrites the header line in place. Exits
// when stopCh closes.
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
			s.rewriteHeaderLocked()
			s.mu.Unlock()
		}
	}
}

// rewriteHeaderLocked moves the cursor up one line, clears it, and
// writes a fresh header. Caller holds mu. The cursor returns to the
// row immediately after the header so subsequent streamed output
// flows naturally below it.
func (s *toolSoloState) rewriteHeaderLocked() {
	// Up one line, carriage return, erase to end of line.
	fmt.Fprint(s.out, term.CursorUp(1)+term.EraseLine)
	fmt.Fprintln(s.out, s.formatHeader())
}

// Write streams a chunk of tool output through the underlying
// writer while tracking how many newlines have appeared since
// Freeze. The CLI calls this on every stream.tool_output for the
// solo path; Done relies on the counter to find the header row.
//
// Implements io.Writer. Safe to call concurrently with the ticker
// (mu serializes), but in practice all calls come from the single
// RPC reader goroutine after Freeze has already stopped the ticker.
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

// formatHeader returns the header line *without* a trailing newline.
// Animated glyph for the running state; ✓/✗ for completed.
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
	header := term.Dim("─── ") + colorFn(icon) + term.Dim(" ▶ "+s.name)
	if s.detail != "" {
		header += term.Dim(" · " + s.detail)
	}
	header += term.Dim(" ───")
	return header
}
