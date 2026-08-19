//go:build !windows

package term

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ProbeAmbiguousWide asks the TERMINAL how wide it draws an ambiguous glyph.
func ProbeAmbiguousWide(timeout time.Duration) (wide, ok bool) {
	if !IsTerminal(int(os.Stdin.Fd())) || !IsTerminal(int(os.Stdout.Fd())) {
		return false, false
	}
	restore, err := MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return false, false
	}
	defer restore()

	// \r puts us at column 1 so the reply's column IS the width drawn.
	if _, err := os.Stdout.WriteString("\r\u2500\x1b[6n"); err != nil {
		return false, false
	}
	col, got := readCursorCol(timeout)
	// Erase the probe glyph whatever happened: the user must not see it.
	os.Stdout.WriteString("\r\x1b[2K")
	if !got {
		return false, false
	}
	// col is 1-based and the cursor sits AFTER the glyph.
	return col-1 >= 2, true
}

// readCursorCol reads a CSI row;col R reply from stdin within timeout.
func readCursorCol(timeout time.Duration) (int, bool) {
	type result struct {
		col int
		ok  bool
	}
	ch := make(chan result, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 1)
		for b.Len() < 32 {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				ch <- result{0, false}
				return
			}
			b.WriteByte(buf[0])
			if buf[0] == 'R' {
				ch <- parseCursorReply(b.String())
				return
			}
		}
		ch <- result{0, false}
	}()
	select {
	case r := <-ch:
		return r.col, r.ok
	case <-time.After(timeout):
		return 0, false // a terminal that will not answer keeps its default
	}
}

func parseCursorReply(s string) (r struct {
	col int
	ok  bool
}) {
	i := strings.LastIndex(s, "[")
	if i < 0 || !strings.HasSuffix(s, "R") {
		return
	}
	parts := strings.Split(s[i+1:len(s)-1], ";")
	if len(parts) != 2 {
		return
	}
	col, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || col <= 0 {
		return
	}
	r.col, r.ok = col, true
	return
}

// MeasureDrawn reports how many columns the terminal actually advanced when it
// drew s: the ground truth figaro's own measurement is checked against.
func MeasureDrawn(s string, timeout time.Duration) (int, bool) {
	if !IsTerminal(int(os.Stdin.Fd())) || !IsTerminal(int(os.Stdout.Fd())) {
		return 0, false
	}
	restore, err := MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return 0, false
	}
	defer restore()
	if _, err := os.Stdout.WriteString("\r" + s + "\x1b[6n"); err != nil {
		return 0, false
	}
	col, ok := readCursorCol(timeout)
	os.Stdout.WriteString("\r\x1b[2K")
	if !ok {
		return 0, false
	}
	return col - 1, true
}
