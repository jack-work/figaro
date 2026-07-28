//go:build windows

package term

import (
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
	raw := rawInputMode(old)
	if err := windows.SetConsoleMode(h, raw); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}

func rawInputMode(mode uint32) uint32 {
	mode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	return mode
}
