package main

import (
	"fmt"
	"io"
	"sync"
	"time"
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
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
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
	// Already frozen → just repaint the header with the new glyph.
	s.mu.Lock()
	s.rewriteHeaderLocked()
	s.mu.Unlock()
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
	fmt.Fprint(s.out, "\033[1A\r\033[2K")
	fmt.Fprintln(s.out, s.formatHeader())
}

// formatHeader returns the header line *without* a trailing newline.
// Animated glyph for the running state; ✓/✗ for completed.
func (s *toolSoloState) formatHeader() string {
	var icon, color string
	switch s.state {
	case toolRowRunning:
		icon = string(spinnerFrames[s.frame%len(spinnerFrames)])
		color = "\033[36m" // cyan
	case toolRowOK:
		icon = "✓"
		color = "\033[32m" // green
	case toolRowErr:
		icon = "✗"
		color = "\033[31m" // red
	default:
		icon = "▶"
		color = "\033[2m"
	}
	const reset = "\033[0m"
	header := fmt.Sprintf("\033[2m─── %s%s%s \033[2m▶ %s", color, icon, reset, s.name)
	if s.detail != "" {
		header += " · " + s.detail
	}
	header += " ───\033[0m"
	return header
}
