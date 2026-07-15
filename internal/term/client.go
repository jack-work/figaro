package term

import (
	"os"
	"os/signal"
	"syscall"
)

// Client abstracts the terminal operations whose implementation differs by
// platform and emulator: raw-mode entry, size, resize notification, raw input
// reads, and clipboard. The unix/bash implementation here uses termios +
// SIGWINCH + OSC 52. A Windows / Windows Terminal implementation MUST satisfy
// this same interface on the Windows branch — using SetConsoleMode (for raw
// mode + ENABLE_VIRTUAL_TERMINAL_INPUT), a ConPTY WINDOW_BUFFER_SIZE_EVENT (or a
// size poll) instead of SIGWINCH, and treating Ctrl-C as a 0x03 input byte
// (VT-input) rather than a signal. ALL client-specific special-casing lives
// behind this boundary — never scattered through the renderer or the prompt loop.
type Client interface {
	// MakeRaw puts stdin in per-key, no-echo mode WITH SIGNAL GENERATION
	// DISABLED, so Ctrl-C (0x03) and Ctrl-D (0x04) arrive as ordinary input
	// bytes (portable across platforms, unlike SIGINT). Returns a restore func.
	MakeRaw() (restore func(), err error)
	Size() (w, h int)                      // current viewport
	OnResize(func(w, h int)) (stop func()) // fires on size change; SIGWINCH on unix
	Read(p []byte) (n int, err error)      // raw stdin bytes
	SetClipboard(s string)                 // copy s to the system clipboard
	IsTTY() bool
}

// NewClient returns the unix/bash Client (termios, SIGWINCH, OSC 52).
func NewClient() Client { return &unixClient{} }

type unixClient struct{}

func (unixClient) MakeRaw() (func(), error) { return MakeRaw(int(os.Stdin.Fd())) }
func (unixClient) Size() (int, int)         { return Width(), Height() }
func (unixClient) Read(p []byte) (int, error) {
	return os.Stdin.Read(p)
}
func (unixClient) SetClipboard(s string) { _, _ = os.Stdout.WriteString(OSC52(s)) }
func (unixClient) IsTTY() bool           { return IsTerminal(int(os.Stdin.Fd())) }

func (c unixClient) OnResize(cb func(w, h int)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				w, h := c.Size()
				cb(w, h)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
