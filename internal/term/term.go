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
type palette struct {
	dim   string
	red   string
	green string
	cyan  string

	arg   string // springBlue #7FB4CA: what NAMES a call: tool name, status word
	body  string // fujiWhite #DCD7BA: prose, thinking, argument VALUES: one voice
	label string // fujiGray #727169, argument NAMES, quieter than their values

	diffAdd string // added line: autumnGreen #76946A on a winterGreen wash
	diffDel string // removed line: autumnRed #C34043 on a winterRed wash

	present string // a thing that IS there: diffAdd's autumnGreen, no wash
	absent  string // a thing that is NOT: diffDel's autumnRed, no wash

	stateDim string // sumiInk-adjacent gray-violet: form-state furniture below a node, quieter than label

	// notice is trouble reported INSIDE an already-dimmed row. It carries its
	// own re-light (22 = not-dim) because a dim red reads as decoration; the
	// whole point of a notice is that it does not.
	notice string
}

var kanagawa = palette{
	dim:      "\033[2m",
	red:      "\033[31m",
	green:    "\033[32m",
	cyan:     "\033[36m",
	arg:      "\033[38;5;110m",
	body:     "\033[38;5;252m",
	label:    "\033[38;5;242m",
	diffAdd:  "\033[38;5;108;48;5;22m",
	diffDel:  "\033[38;5;167;48;5;52m",
	present:  "\033[38;5;108m",
	absent:   "\033[38;5;167m",
	stateDim: "\033[38;5;60m",     // ≈ sumiInk4 #54546D: dimmer than label, still legible
	notice:   "\033[22;38;5;167m", // autumnRed #C34043, the palette's own red
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
func Present(s string) string { return paint(active.present, s) }
func Absent(s string) string  { return paint(active.absent, s) }

// NoticeInDim paints trouble that lives inside a row the caller has already
// dimmed, and hands the row back exactly as it found it: default foreground,
// still dim. It is not Red: basic 31 is the terminal's red rather than the
// theme's, and it was the one string in the status row painted by a literal
// SGR instead of the palette.
//
// The hand-back is the reason this is a function and not a role. Every other
// role closes with a full reset, which would drop the dim for the rest of the
// row; this one has to close with 39;2, and a caller open-coding that pair is
// how the literal got there the first time.
func NoticeInDim(s string) string {
	if !Enabled() {
		return s
	}
	return active.notice + s + dimHandBack
}

// dimHandBack restores default foreground and re-dims: the state a dimmed row
// was in before the notice interrupted it.
const dimHandBack = "\033[39;2m"

// StateDim is the form-state furniture drawn beneath a node: dimmer than
// the selection UI, so the eye ranks selection above state and state above
// nothing. One name, one meaning: do not reuse the comment colour by
// coincidence.
func StateDim(s string) string { return paint(active.stateDim, s) }

// roles indexes the palette by name, so a caller carrying a role as DATA (a
// config value, a wire field) can paint with it without importing a func.
var roles = map[string]func(string) string{
	"dim":       Dim,
	"red":       Red,
	"green":     Green,
	"cyan":      Cyan,
	"arg":       Arg,
	"body":      Body,
	"label":     Label,
	"diff-add":  DiffAdd,
	"diff-del":  DiffDel,
	"present":   Present,
	"absent":    Absent,
	"state-dim": StateDim,
}

// Paint applies the named role, or returns s unchanged when the name is not a
// role. An unknown name is deliberately not an error: a colour is decoration,
// and losing it must never be worse than the thing it decorates.
func Paint(role, s string) string {
	if f, ok := roles[role]; ok {
		return f(s)
	}
	return s
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
