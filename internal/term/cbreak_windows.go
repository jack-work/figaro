//go:build windows

package term

import (
	"os"

	"golang.org/x/sys/windows"
)

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

const cookedInput = windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT

func cookedInputMode(mode uint32) uint32 {
	mode |= cookedInput
	mode &^= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	return mode
}

// ArmCookedInput puts stdin into line mode for a plain prompt.
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
// without unwinding.
func SanitizeInput() {
	h := windows.Handle(os.Stdin.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return
	}
	if old&cookedInput != 0 {
		return
	}
	_ = windows.SetConsoleMode(h, cookedInputMode(old))
}

// DISABLE_NEWLINE_AUTO_RETURN is not set here: it stops a bare LF from
// implying a carriage return, which staircases every line-oriented command.
// The painter arms it for itself (ArmDeferredWrap).
func vtOutputMode(mode uint32) uint32 {
	return mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
}

// ArmDeferredWrap defers the right-edge wrap for a painted session.
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
