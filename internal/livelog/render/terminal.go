// Package render draws a doc.Doc to a terminal as a live, append-only log using
// pi-style differential rendering: it holds the whole frame, line-diffs each
// update, rewrites only what changed, and falls back to a full redraw on resize
// or when an off-screen line changes. The Terminal and BlockRenderer interfaces
// keep it isolation-testable with no real tty.
package render

import "io"

// Terminal is the drawing surface. Abstracting it lets tests drive a
// deterministic in-memory VT (FakeTerminal) instead of a real tty, and lets the
// renderer stay oblivious to how size is discovered (SIGWINCH, queries, etc.).
type Terminal interface {
	// Write emits raw bytes (text + ANSI control sequences).
	Write(p []byte) (int, error)
	// Size reports the current viewport in (columns, rows).
	Size() (width, height int)
}

// ANSITerminal adapts an io.Writer (a real tty) to Terminal. Size is supplied by
// the caller, who updates it on SIGWINCH via SetSize.
type ANSITerminal struct {
	w             io.Writer
	width, height int
}

// NewANSITerminal wraps w with an initial size.
func NewANSITerminal(w io.Writer, width, height int) *ANSITerminal {
	return &ANSITerminal{w: w, width: width, height: height}
}

func (t *ANSITerminal) Write(p []byte) (int, error) { return t.w.Write(p) }
func (t *ANSITerminal) Size() (int, int)            { return t.width, t.height }

// SetSize updates the reported size; call it from a SIGWINCH handler before the
// next render so the renderer reconciles to the new viewport.
func (t *ANSITerminal) SetSize(width, height int) { t.width, t.height = width, height }
