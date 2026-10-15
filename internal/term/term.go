// Package term provides terminal color, width, and TTY detection.
package term

import (
	"os"
	"sync"

	"golang.org/x/term"
)

type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorNever
	ColorAlways
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

// SetColorMode overrides the detected mode and returns the restore.
func SetColorMode(m ColorMode) func() {
	prev := mode
	mode = m
	return func() { mode = prev }
}

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

func IsTerminal(fd int) bool {
	return term.IsTerminal(fd)
}

func ReadPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

// Width defaults to 80 off a TTY.
func Width() int {
	if !isTTY {
		return 80
	}
	if c, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return sizeOr(c, 80)
	}
	return 80
}

// Height defaults to 24 off a TTY.
func Height() int {
	if !isTTY {
		return 24
	}
	if _, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return sizeOr(r, 24)
	}
	return 24
}

func sizeOr(measured, fallback int) int {
	if measured > 0 {
		return measured
	}
	return fallback
}

const reset = "\033[0m"

// A palette is one SGR body per role; roles are named for meaning, not hue.
type palette struct {
	dim   string
	red   string
	green string
	cyan  string

	arg   string
	body  string
	label string

	diffAdd string
	diffDel string

	present string
	absent  string

	stateDim string

	// notice and caution carry their own re-light (22): they are painted
	// inside an already-dimmed row.
	notice  string
	caution string
	// noticeOnWash and cautionOnWash are one step lighter: the palette's red
	// over the selection wash is 1.9:1.
	noticeOnWash, cautionOnWash string

	// SGR bodies without a reset: the painter that splices them owns it.
	selectWash, selectWash256   string
	cursor, cursor256           string
	nodeWash, nodeWashFocus     string
	commentWash, commentWash256 string
	commentMark                 string
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
	stateDim: "\033[38;5;60m",
	notice:   "\033[22;38;5;167m",
	caution:  "\033[22;38;5;179m",

	noticeOnWash:  "\033[22;38;5;210m",
	cautionOnWash: "\033[22;38;5;222m",

	selectWash:    "\033[48;2;45;79;103m",
	selectWash256: "\033[48;5;23m",
	nodeWash:      "\033[48;5;236m",
	nodeWashFocus: "\033[48;5;237m",

	commentWash:    "\033[48;2;54;72;48m",
	commentWash256: "\033[48;5;22m",
	commentMark:    "\033[38;5;107m",
	cursor:         "\033[38;2;22;22;29;48;2;220;215;186m",
	cursor256:      "\033[38;5;234;48;5;187m",
}

func TrueColor() bool {
	switch os.Getenv("COLORTERM") {
	case "truecolor", "24bit":
		return true
	}
	return false
}

// SelectWash is a bare SGR body; the caller owns the reset.
func SelectWash() string {
	if !Enabled() {
		return ""
	}
	if TrueColor() {
		return active.selectWash
	}
	return active.selectWash256
}

func CommentWash() string {
	if !Enabled() {
		return ""
	}
	if TrueColor() {
		return active.commentWash
	}
	return active.commentWash256
}

func CommentMark(s string) string { return paint(active.commentMark, s) }

func CommentUnderline() string {
	if !Enabled() {
		return ""
	}
	return "\033[4m"
}

// NodeWash is a bare SGR body; the caller owns the reset.
func NodeWash(focused bool) string {
	if !Enabled() {
		return ""
	}
	if focused {
		return active.nodeWashFocus
	}
	return active.nodeWash
}

// Cursor is a bare SGR body; the caller owns the reset.
func Cursor() string {
	if !Enabled() {
		return ""
	}
	if TrueColor() {
		return active.cursor
	}
	return active.cursor256
}

var active = kanagawa

func paint(code, s string) string {
	if !Enabled() {
		return s
	}
	return code + s + reset
}

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

// NoticeInDim paints inside a row the caller has dimmed and hands the row
// back still dim (39;2), where a full reset would drop the dim.
func NoticeInDim(s string) string {
	if !Enabled() {
		return s
	}
	return active.notice + s + dimHandBack
}

func CautionInDim(s string) string {
	if !Enabled() {
		return s
	}
	return active.caution + s + dimHandBack
}

// NoticeBody and CautionBody are bare SGR bodies; the caller owns the reset.
func NoticeBody() string {
	if !Enabled() {
		return ""
	}
	return active.notice
}

func CautionBody() string {
	if !Enabled() {
		return ""
	}
	return active.caution
}

// NoticeOnWash and CautionOnWash are bare SGR bodies for text over a
// selection wash.
func NoticeOnWash() string {
	if !Enabled() {
		return ""
	}
	return active.noticeOnWash
}

func CautionOnWash() string {
	if !Enabled() {
		return ""
	}
	return active.cautionOnWash
}

const dimHandBack = "\033[39;2m"

func StateDim(s string) string { return paint(active.stateDim, s) }

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

// Paint returns s unchanged for an unknown role.
func Paint(role, s string) string {
	if f, ok := roles[role]; ok {
		return f(s)
	}
	return s
}

// VisibleLen counts columns ignoring ANSI escapes.
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
		if vis >= maxCols-1 {
			out = append(out, '…')
			out = append(out, []rune(reset)...)
			return string(out)
		}
		out = append(out, r)
		vis++
	}
	return string(out)
}
