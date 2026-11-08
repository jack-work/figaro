//go:build !windows

package term

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ProbeAmbiguousWide asks the terminal how wide it draws an ambiguous glyph.
func ProbeAmbiguousWide(timeout time.Duration) (wide, ok bool) {
	if !IsTerminal(int(os.Stdin.Fd())) || !IsTerminal(int(os.Stdout.Fd())) {
		return false, false
	}
	restore, err := MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return false, false
	}
	defer restore()

	if _, err := os.Stdout.WriteString("\r\u2500\x1b[6n"); err != nil {
		return false, false
	}
	col, got := readCursorCol(timeout)
	os.Stdout.WriteString("\r\x1b[2K")
	if !got {
		return false, false
	}
	return col-1 >= 2, true
}

func readCursorCol(timeout time.Duration) (int, bool) {
	fd := int(os.Stdin.Fd())
	deadline := time.Now().Add(timeout)
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < 32 {
		left := time.Until(deadline)
		if left <= 0 {
			return 0, false
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 || fds[0].Revents&unix.POLLIN == 0 {
			return 0, false
		}
		if n, err := unix.Read(fd, buf); err != nil || n == 0 {
			return 0, false
		}
		b.WriteByte(buf[0])
		if buf[0] == 'R' {
			r := parseCursorReply(b.String())
			return r.col, r.ok
		}
	}
	return 0, false
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

// MeasureDrawn reports how many columns the terminal advanced drawing s.
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
