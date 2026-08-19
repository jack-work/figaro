//go:build windows

package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// MakeRaw puts the console into raw VT-input mode: no line editing, echo, or
// signal generation, and navigation/mouse events encoded as terminal sequences.
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

// cookedInput is the trio a line-oriented prompt needs: the console does the
// editing, echoes what is typed, and turns Ctrl-C into an interrupt.
const cookedInput = windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT

// cookedInputMode is rawInputMode's inverse: line editing, echo and Ctrl-C
// back on, VT input off, everything else the console arrived with untouched.
func cookedInputMode(mode uint32) uint32 {
	mode |= cookedInput
	mode &^= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	return mode
}

// ArmCookedInput puts the console we READ into line mode for the duration of a
// plain prompt, and returns the restore.
func ArmCookedInput() func() {
	h := windows.Handle(os.Stdin.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, cookedInputMode(old)); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, old) }
}

// SanitizeInput repairs a console left in raw mode by a figaro that died
// without unwinding, a crash, a taskkill, a window closed mid-session.
func SanitizeInput() {
	h := windows.Handle(os.Stdin.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return // not a console: nothing to repair
	}
	if old&cookedInput != 0 {
		return
	}
	_ = windows.SetConsoleMode(h, cookedInputMode(old))
}

// DISABLE_NEWLINE_AUTO_RETURN is deliberately NOT set here. It defers the
// right-edge wrap, which the painter wants: but it also stops a bare LF from
// implying a carriage return, and every line-oriented command (`list`, `show`,
// `status`) writes bare \n through fmt.Println. Armed globally at startup it
// staircased all of them rightward until they wrapped, which reads as "figaro
// ignores my terminal width". The painter arms it for itself: ArmDeferredWrap.
func vtOutputMode(mode uint32) uint32 {
	return mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
}

// ArmDeferredWrap defers the right-edge wrap for the duration of a PAINTED
// session, and returns the restore.
func ArmDeferredWrap() func() {
	h := windows.Handle(os.Stdout.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(h, old|windows.DISABLE_NEWLINE_AUTO_RETURN); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, old) }
}
