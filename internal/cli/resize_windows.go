//go:build windows

package cli

import "os"

// notifyResize is a no-op on Windows: the console subsystem delivers
// resize events through ReadConsoleInput, not a signal. The TUI already
// polls term.Width()/Height() on each repaint tick, so resize is handled
// without a dedicated signal channel.
func notifyResize(ch chan<- os.Signal) func() {
	return func() {}
}
