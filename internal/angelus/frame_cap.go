package angelus

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

var maxEndpointFrame = 64 << 20

var errFrameTooLarge = errors.New("door frame too large")

type frameCapped struct {
	io.ReadWriteCloser
	max     int
	size    int
	depth   int
	inStr   bool
	esc     bool
	tripped error
}

func capFrames(rwc io.ReadWriteCloser, max int) *frameCapped {
	return &frameCapped{ReadWriteCloser: rwc, max: max}
}

func (f *frameCapped) Read(p []byte) (int, error) {
	if f.tripped != nil {
		return 0, f.tripped
	}
	n, err := f.ReadWriteCloser.Read(p)
	if f.scan(p[:n]) {
		f.tripped = fmt.Errorf("%w: over %d bytes", errFrameTooLarge, f.max)
		f.ReadWriteCloser.Close()
		return 0, f.tripped
	}
	return n, err
}

func (f *frameCapped) scan(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if f.inStr && !f.esc {
			j := bytes.IndexAny(b[i:], "\"\\")
			if j < 0 {
				f.size += len(b) - i
				return f.size > f.max
			}
			f.size += j
			i += j
		}
		f.size++
		if f.size > f.max {
			return true
		}
		c := b[i]
		switch {
		case f.esc:
			f.esc = false
		case f.inStr:
			if c == '\\' {
				f.esc = true
			} else if c == '"' {
				f.inStr = false
			}
		case c == '"':
			f.inStr = true
		case c == '{' || c == '[':
			f.depth++
		case (c == '}' || c == ']') && f.depth > 0:
			f.depth--
			if f.depth == 0 {
				f.size = 0
			}
		}
	}
	return false
}
