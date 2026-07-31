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
	restoreOut := armOutput()
	return func() {
		restoreOut()
		_ = windows.SetConsoleMode(h, old)
	}, nil
}

// armOutput turns on VT output processing and deferred right-edge wrap for
// stdout, returning the restore. A non-console stdout (piped, redirected) is
// not an error: there is simply nothing to arm.
func armOutput() func() {
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
