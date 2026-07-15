//go:build windows

package term

import (
	"golang.org/x/sys/windows"
)

// MakeCbreak puts the console handle into cbreak mode: per-keystroke input
// with no echo, Ctrl-C still delivered as a signal. Returns a restore func.
func MakeCbreak(fd int) (func(), error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	raw := old &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT)
	if err := windows.SetConsoleMode(h, raw); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}

// MakeRaw puts the console into raw mode (no line editing, no echo, no
// signal generation). On Windows this disables ENABLE_PROCESSED_INPUT
// in addition to the cbreak flags.
func MakeRaw(fd int) (func(), error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	raw := old &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if err := windows.SetConsoleMode(h, raw); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}
