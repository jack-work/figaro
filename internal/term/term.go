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

// SetColorMode overrides the detected mode and returns the restore. For tests:
// a colour assertion is vacuous on the non-TTY stdout a test binary has.
func SetColorMode(m ColorMode) func() {
	prev := mode
	mode = m
	return func() { mode = prev }
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

const reset = "\033[0m"

// A palette is the whole theme: one SGR body per role. Roles are named for
// MEANING, not for hue, so a second palette is a second var rather than a
// rewrite of every call site.
//
// Kanagawa, at the owner's request, in xterm-256 — the terminal's own 8
// primaries vary per theme, and truecolor is not safe to assume through tmux.
// Each field names the Kanagawa colour it approximates.
type palette struct {
	dim   string
	red   string
	green string
	cyan  string

	arg   string // springBlue #7FB4CA — what NAMES a call: tool name, status word
	body  string // fujiWhite #DCD7BA — prose, thinking, argument VALUES: one voice
	label string // fujiGray #727169 — argument NAMES, quieter than their values

	diffAdd string // added line: autumnGreen #76946A on a winterGreen wash
	diffDel string // removed line: autumnRed #C34043 on a winterRed wash
}

var kanagawa = palette{
	dim:     "\033[2m",
	red:     "\033[31m",
	green:   "\033[32m",
	cyan:    "\033[36m",
	arg:     "\033[38;5;110m",
	body:    "\033[38;5;252m",
	label:   "\033[38;5;242m",
	diffAdd: "\033[38;5;108;48;5;22m",
	diffDel: "\033[38;5;167;48;5;52m",
}

// active is the palette in force. One var is the whole theme mechanism until
// there is a second theme to choose between.
var active = kanagawa

// paint wraps s in one role's SGR, or returns it bare when colour is off.
func paint(code, s string) string {
	if !Enabled() {
		return s
	}
	return code + s + reset
}

// One function per role. The name says what the thing IS; the palette says
// what colour that is today.
func Dim(s string) string     { return paint(active.dim, s) }
func Red(s string) string     { return paint(active.red, s) }
func Green(s string) string   { return paint(active.green, s) }
func Cyan(s string) string    { return paint(active.cyan, s) }
func Arg(s string) string     { return paint(active.arg, s) }
func Body(s string) string    { return paint(active.body, s) }
func Label(s string) string   { return paint(active.label, s) }
func DiffAdd(s string) string { return paint(active.diffAdd, s) }
func DiffDel(s string) string { return paint(active.diffDel, s) }

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
