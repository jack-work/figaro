//go:build windows

package term

import (
	"os"
	"time"
)

func NewClient() Client { return &winClient{} }

type winClient struct{}

func (winClient) MakeRaw() (func(), error)    { return MakeRaw(int(os.Stdin.Fd())) }
func (winClient) Size() (int, int)            { return Width(), Height() }
func (winClient) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (winClient) SetClipboard(s string)       { _, _ = os.Stdout.WriteString(OSC52(s)) }
func (winClient) IsTTY() bool                 { return IsTerminal(int(os.Stdin.Fd())) }

// OnResize polls for size changes. Windows has no SIGWINCH; ConPTY
// delivers WINDOW_BUFFER_SIZE_EVENT through ReadConsoleInput but that
// requires a dedicated console read loop. Polling at 4 Hz is simple
// and sufficient for a TUI that repaints on content changes anyway.
func (c winClient) OnResize(cb func(w, h int)) func() {
	done := make(chan struct{})
	go func() {
		pw, ph := c.Size()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w, h := c.Size()
				if w != pw || h != ph {
					pw, ph = w, h
					cb(w, h)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
