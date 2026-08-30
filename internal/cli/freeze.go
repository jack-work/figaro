package cli

// FREEZE FORENSICS: what to do when the pager stops answering.
//
// A frozen pager is one of two animals and they need the same evidence. Either
// a goroutine holds the render lock and will not give it back (a deadlock: no
// CPU, dead keys, no repaint on resize), or something under that lock is
// spinning (a livelock: one core pinned, dead keys, no repaint on resize). A
// full goroutine dump names both -- who holds it, and where they are.
//
// Three doors, because a freeze rarely happens while anyone is watching:
//
//	SIGUSR1   dump every goroutine, now, and keep running
//	SIGUSR2   ten seconds of CPU profile, for the spinning kind
//	watchdog  dump by itself when the render lock has been unavailable too long
//
// The watchdog is the one that matters: it turns "it locked up last night"
// into a file with the answer in it.

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"
)

// freezeStuckAfter is how long the render lock may be unavailable before the
// pager is presumed frozen. A slow frame is milliseconds; a page fetch under
// the lock would be a bug of its own. Five seconds is far past both.
const freezeStuckAfter = 5 * time.Second

// freezeProfileFor is how long SIGUSR2 profiles for.
const freezeProfileFor = 10 * time.Second

// freezeDir is where dumps land: beside the telemetry the daemon already
// writes, because that is the directory a reader is already being asked for.
func freezeDir() string {
	dir := filepath.Join(stateDir(), "freeze")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	return dir
}

// dumpGoroutines writes every goroutine's stack to a timestamped file and
// returns its path. It never fails loudly: this runs when something is already
// wrong, and a diagnostic that panics is worse than no diagnostic.
func dumpGoroutines(why string) string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	path := filepath.Join(freezeDir(), fmt.Sprintf("stacks-%s.txt", time.Now().Format("20060102-150405.000")))
	head := fmt.Sprintf("# figaro %s pid %d\n# why: %s\n# %s\n\n",
		buildRevision(), os.Getpid(), why, time.Now().Format(time.RFC3339Nano))
	if err := os.WriteFile(path, append([]byte(head), buf...), 0o600); err != nil {
		slog.Error("freeze dump failed", "err", err)
		return ""
	}
	slog.Error("freeze dump written", "path", path, "why", why)
	return path
}

// profileCPU records a CPU profile for d, for the spinning kind of freeze.
func profileCPU(d time.Duration) string {
	path := filepath.Join(freezeDir(), fmt.Sprintf("cpu-%s.pprof", time.Now().Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.Error("freeze profile failed", "err", err)
		return ""
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		slog.Error("freeze profile failed", "err", err)
		return ""
	}
	go func() {
		time.Sleep(d)
		pprof.StopCPUProfile()
		f.Close()
		slog.Error("freeze profile written", "path", path)
	}()
	return path
}

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

// watchRenderLock is the watchdog. Once a second it asks for the render lock
// and gives it straight back; when it cannot have it for freezeStuckAfter, it
// dumps -- ONCE per episode, because a pager that is stuck stays stuck and a
// dump per second would bury the one that matters.
//
// TryLock, never Lock: the watchdog must not be the thing that is waiting.
func watchRenderLock(mu *sync.Mutex) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var stuck time.Duration
		dumped := false
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if mu.TryLock() {
					mu.Unlock()
					stuck, dumped = 0, false
					continue
				}
				stuck += time.Second
				if stuck >= freezeStuckAfter && !dumped {
					dumped = true
					dumpGoroutines(fmt.Sprintf("render lock held for %s", stuck))
					profileCPU(freezeProfileFor)
				}
			}
		}
	}()
	return func() { close(done) }
}
