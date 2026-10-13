// Package render draws an aria to a terminal as a live, append-only log:
// it line-diffs each update and rewrites only what changed.
package render

import "io"

// Terminal is the drawing surface.
type Terminal interface {
	Write(p []byte) (int, error)
	Size() (width, height int)
}

// ANSITerminal adapts an io.Writer to Terminal; the caller supplies the size.
type ANSITerminal struct {
	w             io.Writer
	width, height int
}

func NewANSITerminal(w io.Writer, width, height int) *ANSITerminal {
	return &ANSITerminal{w: w, width: width, height: height}
}

func (t *ANSITerminal) Write(p []byte) (int, error) { return t.w.Write(p) }
func (t *ANSITerminal) Size() (int, int)            { return t.width, t.height }

func (t *ANSITerminal) SetSize(width, height int) { t.width, t.height = width, height }

func (t *ANSITerminal) SetWriter(w io.Writer) { t.w = w }
