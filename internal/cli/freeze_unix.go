//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

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
