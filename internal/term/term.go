// Package term provides terminal color, width, and TTY detection
// utilities. It respects NO_COLOR (https://no-color.org/) and
// FORCE_COLOR environment variables, and can be used across projects
// with no application-specific dependencies.
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

// --- ANSI escape sequences ---

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

// --- Cursor control (always emitted — used only in TTY paths) ---

// CursorUp returns the ANSI sequence to move the cursor up n lines.
func CursorUp(n int) string {
	return fmt.Sprintf("\033[%dA", n)
}

// CursorDown returns the ANSI sequence to move the cursor down n lines.
func CursorDown(n int) string {
	return fmt.Sprintf("\033[%dB", n)
}

// EraseLine returns the ANSI sequence to erase the current line.
const EraseLine = "\r\033[2K"
