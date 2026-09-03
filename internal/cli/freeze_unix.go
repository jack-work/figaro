//go:build !windows

package cli

// SIGUSR1 AND SIGUSR2 ARE NOT PORTABLE, and naming them in a file with no
// build tag is what stopped every release archive from being built:
// goreleaser cross-compiles windows/amd64, where neither constant exists, so
// the whole release failed over a debugging affordance no Windows user was
// going to reach for anyway.

import (
	"os"
	"os/signal"
	"syscall"
)

// armFreezeSignals wires SIGUSR1/SIGUSR2. Returns a stop func.
func armFreezeSignals() func() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGUSR2)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				if sig == syscall.SIGUSR2 {
					profileCPU(freezeProfileFor)
					continue
				}
				dumpGoroutines("SIGUSR1")
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
