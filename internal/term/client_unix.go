//go:build !windows

package term

import (
	"os"
	"os/signal"
	"syscall"
)

func NewClient() Client { return &unixClient{} }

type unixClient struct{}

func (unixClient) MakeRaw() (func(), error)    { return MakeRaw(int(os.Stdin.Fd())) }
func (unixClient) Size() (int, int)            { return Width(), Height() }
func (unixClient) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (unixClient) SetClipboard(s string)       { _, _ = os.Stdout.WriteString(OSC52(s)) }
func (unixClient) IsTTY() bool                 { return IsTerminal(int(os.Stdin.Fd())) }

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
