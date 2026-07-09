//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize subscribes ch to terminal-resize events. Returns a
// cleanup func that unsubscribes.
func notifyResize(ch chan<- os.Signal) func() {
	signal.Notify(ch, syscall.SIGWINCH)
	return func() { signal.Stop(ch) }
}
