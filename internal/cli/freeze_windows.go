//go:build windows

package cli

// Windows has no SIGUSR1/SIGUSR2. The on-demand goroutine dump is a unix
// affordance; every other freeze protection -- the render-lock watchdog, the
// profile on a stuck pager -- is portable and still armed.
func armFreezeSignals() func() { return func() {} }
