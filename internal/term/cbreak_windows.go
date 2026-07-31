//go:build windows

package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// MakeRaw puts the console into raw VT-input mode: no line editing, echo, or
// signal generation, and navigation/mouse events encoded as terminal sequences.
//
// It arms the OUTPUT handle too, and that half was missing. Nothing in figaro
// ever set ENABLE_VIRTUAL_TERMINAL_PROCESSING, so the renderer's escapes were
// only honoured when something else had already turned it on. Windows Terminal
// does (measured: stdout mode 0x0007); a bare conhost does not (0x0003), and
// there \x1b[?1049h is inert — the pager's frames land in the PRIMARY buffer
// as ordinary text and the whole transcript is left in the user's scrollback.
// Console mode belongs to the console, not the process, so anything sharing it
// (a tool's child process) can clear the flag mid-session, which is why the
// symptom comes and goes on a terminal that started out fine.
//
// DISABLE_NEWLINE_AUTO_RETURN rides along because it is the same defect at the
// other edge of the screen: without it the console advances the cursor the
// instant the last cell of a row is written, instead of deferring the wrap the
// way every UNIX terminal does. render.Prose lands glamour at EXACTLY the
// viewport width, so a full-width row cost two rows and the painter's
// one-row-per-line cursor math drifted (measured under conhost at width 120:
// 2 rows as found, 1 with the flag; Windows Terminal was already 1).
//
// The restore puts both handles back, and it is the same closure the renderer
// already unwinds — so this costs no new lifecycle.
func MakeRaw(fd int) (func(), error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(h, rawInputMode(old)); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}

// ArmOutput turns on VT output processing and deferred right-edge wrap for
// stdout, returning the restore.
//
// It is SEPARATE from MakeRaw, and called EARLIER, because the two answer
// different questions: MakeRaw is about the console we READ (and only runs on
// an interactive TTY path), while this is about the console we WRITE. figaro
// writes its first escapes — autowrapOff+cursorHide — before any raw-mode
// session exists, and paths that never take raw mode at all (`figaro show`
// rendering markdown, a non-interactive listen) still emit ANSI. Arming inside
// MakeRaw left every one of those unarmed, which on a bare conhost means the
// escapes print as literal text.
//
// A non-console stdout (piped, redirected, a service with no console) is NOT
// an error: there is simply nothing to arm, and a failed SetConsoleMode is a
// normal condition. Both degrade to a no-op restore rather than a failure,
// because a redirected stdout is the ordinary case for `figaro list -j | jq`.
func ArmOutput() func() {
	h := windows.Handle(os.Stdout.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, vtOutputMode(old)); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, old) }
}

func rawInputMode(mode uint32) uint32 {
	mode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	return mode
}

func vtOutputMode(mode uint32) uint32 {
	return mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.DISABLE_NEWLINE_AUTO_RETURN
}
