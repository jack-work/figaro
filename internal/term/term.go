// Package term provides terminal color, width, and TTY detection.
// Respects NO_COLOR and FORCE_COLOR.
package term

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

// ColorMode describes whether color output is enabled.
type ColorMode int

const (
	ColorAuto  ColorMode = iota // detect from TTY + env
	ColorNever                  // NO_COLOR or explicit disable
	ColorAlways                 // FORCE_COLOR or explicit enable
)

var (
	initOnce sync.Once
	mode     ColorMode
	isTTY    bool
)

func init() {
	initOnce.Do(detect)
}

func detect() {
	isTTY = term.IsTerminal(int(os.Stdout.Fd()))

	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		mode = ColorNever
		return
	}
	if v, ok := os.LookupEnv("FORCE_COLOR"); ok && v != "0" {
		mode = ColorAlways
		return
	}
	mode = ColorAuto
}

// Enabled reports whether color output should be used.
func Enabled() bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default:
		return isTTY
	}
}

// IsTTY reports whether stdout is a terminal.
func IsTTY() bool {
	return isTTY
}

// IsTerminal reports whether the given fd is a terminal.
func IsTerminal(fd int) bool {
	return term.IsTerminal(fd)
}

// ReadPassword reads a password from the terminal without echo.
func ReadPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

// Width returns the terminal width, defaulting to 80 if not a TTY
// or if detection fails.
func Width() int {
	if !isTTY {
		return 80
	}
	if c, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 20 {
		return c
	}
	return 80
}

// Height returns the terminal height (rows), defaulting to 24 if
// not a TTY or if detection fails. Mirrors Width's 80-col default so
// height-dependent layout has something to work with in pipes/tests.
func Height() int {
	if !isTTY {
		return 24
	}
	if _, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && r > 0 {
		return r
	}
	return 24
}

// WidthFd returns the terminal width for a specific fd.
func WidthFd(fd int) int {
	if !term.IsTerminal(fd) {
		return 80
	}
	if c, _, err := term.GetSize(fd); err == nil && c > 20 {
		return c
	}
	return 80
}



const (
	reset = "\033[0m"

	codeDim   = "\033[2m"
	codeRed   = "\033[31m"
	codeGreen = "\033[32m"
	codeCyan  = "\033[36m"
)

// Dim wraps s in dim (faint) ANSI if color is enabled.
func Dim(s string) string {
	if !Enabled() {
		return s
	}
	return codeDim + s + reset
}

// Red wraps s in red ANSI if color is enabled.
func Red(s string) string {
	if !Enabled() {
		return s
	}
	return codeRed + s + reset
}

// Green wraps s in green ANSI if color is enabled.
func Green(s string) string {
	if !Enabled() {
		return s
	}
	return codeGreen + s + reset
}

// Cyan wraps s in cyan ANSI if color is enabled.
func Cyan(s string) string {
	if !Enabled() {
		return s
	}
	return codeCyan + s + reset
}

// VisibleLen returns visible columns ignoring ANSI escapes.
func VisibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if r == '\033' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}

// TruncateVisible truncates to maxCols visible width.
func TruncateVisible(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	vis := 0
	inEsc := false
	var out []rune
	for _, r := range s {
		if inEsc {
			out = append(out, r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if r == '\033' {
			inEsc = true
			out = append(out, r)
			continue
		}
		if vis >= maxCols-1 { // -1 to leave room for "…"
			out = append(out, '…')

			out = append(out, []rune(reset)...)
			return string(out)
		}
		out = append(out, r)
		vis++
	}
	return string(out) // no truncation needed
}

// WrapCount returns how many rows visLen occupies at termWidth.
func WrapCount(visLen, termWidth int) int {
	if termWidth <= 0 || visLen <= 0 {
		return 1
	}
	lines := (visLen + termWidth - 1) / termWidth
	if lines < 1 {
		lines = 1
	}
	return lines
}

// CursorUp returns the ANSI cursor-up sequence.
func CursorUp(n int) string {
	return fmt.Sprintf("\033[%dA", n)
}

// CursorDown returns the ANSI sequence to move the cursor down n lines.
func CursorDown(n int) string {
	return fmt.Sprintf("\033[%dB", n)
}

// EraseLine returns the ANSI sequence to erase the current line.
const EraseLine = "\r\033[2K"
