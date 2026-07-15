package term

// Client abstracts the terminal operations whose implementation differs by
// platform and emulator: raw-mode entry, size, resize notification, raw input
// reads, and clipboard. The unix/bash implementation uses termios + SIGWINCH +
// OSC 52. The Windows implementation uses SetConsoleMode + size polling.
type Client interface {
	MakeRaw() (restore func(), err error)
	Size() (w, h int)
	OnResize(func(w, h int)) (stop func())
	Read(p []byte) (n int, err error)
	SetClipboard(s string)
	IsTTY() bool
}
