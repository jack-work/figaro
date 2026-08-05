// Package term provides terminal color, width, and TTY detection.
// Respects NO_COLOR and FORCE_COLOR.
package term

import (
	"os"
	"sync"

	"golang.org/x/term"
)

// ColorMode describes whether color output is enabled.
type ColorMode int

const (
	ColorAuto   ColorMode = iota // detect from TTY + env
	ColorNever                   // NO_COLOR or explicit disable
	ColorAlways                  // FORCE_COLOR or explicit enable
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
	if c, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return sizeOr(c, 80)
	}
	return 80
}

// Height returns the terminal height in rows, defaulting to 24 if not a
// TTY or if detection fails.
func Height() int {
	if !isTTY {
		return 24
	}
	if _, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return sizeOr(r, 24)
	}
	return 24
}

// sizeOr accepts any POSITIVE measurement and falls back only when the
// terminal reported nothing usable.
//
// This guard used to read `c > 20` for columns and `r > 2` for rows, which
// discarded legitimate small terminals and substituted a fabricated 80x24.
// The renderer then painted 24 rows into a 2-row pane; every repaint scrolled
// the previous frame into history, so a streaming reply appeared several times
// over, each copy longer than the last, and the pager floor and bodyHidden()
// were both reasoning about a height the terminal never had. A measurement we
// dislike is still a measurement — the only honest fallback is when there is
// no measurement at all.
func sizeOr(measured, fallback int) int {
	if measured > 0 {
		return measured
	}
	return fallback
}

const (
	reset = "\033[0m"

	codeDim   = "\033[2m"
	codeRed   = "\033[31m"
	codeGreen = "\033[32m"
	codeCyan  = "\033[36m"

	// codeArg is the off colour tool ARGUMENTS are drawn in: a soft blue-grey
	// (xterm 256). Arguments are neither prose (the agent's voice), nor tool
	// output (the machine's), nor thinking (dim) — they are what the agent is
	// about to DO, and on a dense screen that distinction is worth a colour of
	// its own. 256-colour, like the transcript's selection wash, so it reads
	// as a tint rather than one of the eight loud primaries.
	codeArg = "\033[38;5;109m"
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

// Arg wraps s in the argument colour if color is enabled.
func Arg(s string) string {
	if !Enabled() {
		return s
	}
	return codeArg + s + reset
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
