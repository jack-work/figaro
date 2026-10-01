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
//	watchdog  dump by itself when one acquisition of the render lock outlives
//	          any frame that could be honest
//
// The watchdog is the one that matters: it turns "it locked up last night"
// into a file with the answer in it.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// freezeStuckAfter is how long ONE acquisition of the render lock may stay
// open before the pager is presumed frozen. A slow frame is milliseconds; a
// page fetch under the lock would be a bug of its own. Five seconds is far
// past both.
const freezeStuckAfter = 5 * time.Second

// freezePollEvery is how often the watchdog reads the lock's clock. Reading it
// is one atomic load, so this can be frequent without being felt; it only sets
// how late a report can be.
const freezePollEvery = 500 * time.Millisecond

// freezeDumpCap is how many episodes one session reports. A pager that is
// genuinely wedged is described by its first dump; the rest are noise that
// slows the thing being diagnosed (every dump stops the world).
const freezeDumpCap = 8

// freezeKeep is how many files the freeze directory holds. Forensics older
// than that have outlived their session: 7069 of them accumulated under the
// watchdog this comment's sibling describes, which is its own kind of bug
// report.
const freezeKeep = 200

// freezeProfileFor is how long SIGUSR2 profiles for.
const freezeProfileFor = 10 * time.Second

// renderLock is the pager's render lock AND the watchdog's witness: a mutex
// that remembers when the acquisition in force began. Nothing outside a
// sync.Mutex can answer "has one frame been stuck under this", which is the
// only question worth raising an alarm over.
//
// THE OLD WATCHDOG ASKED WITH TryLock AND BELIEVED THE ANSWER. TryLock fails
// when the mutex is merely BUSY: it joins no waiter queue, it does not spin,
// its single CAS gives up on any race, and it returns false outright while the
// mutex is in starvation mode -- which one waiter parked for a millisecond is
// enough to set. The pager takes this lock eleven times a second for the
// spinner alone, plus once per keystroke and once per streamed token, so five
// consecutive failed polls were routine rather than alarming. Modelled at that
// duty cycle with a 5ms hold, 37 of 40 polls failed and the watchdog "found"
// six freezes in forty seconds. One log here carried 236 of those reports,
// each one costing a stop-the-world stack dump and a ten-second CPU profile of
// a process that was answering keys the whole time, and each one surfacing in
// the pager as an error the reader could do nothing about.
type renderLock struct {
	mu sync.Mutex
	// since is the UnixNano at which the acquisition in force began, or 0 when
	// the lock is free. Written by the holder, read by the watchdog: the only
	// field in figaro a sampler is allowed to race with, and it is atomic.
	since atomic.Int64
}

func (l *renderLock) Lock() {
	l.mu.Lock()
	l.since.Store(time.Now().UnixNano())
}

func (l *renderLock) Unlock() {
	l.since.Store(0)
	l.mu.Unlock()
}

// TryLock is still here for the callers that must never block -- the exit hook
// and the memory mark -- and it stamps the clock like any other acquisition.
func (l *renderLock) TryLock() bool {
	if !l.mu.TryLock() {
		return false
	}
	l.since.Store(time.Now().UnixNano())
	return true
}

// heldFor reports how long the acquisition in force has been open, and the
// stamp that identifies it. A zero duration means the lock was free at the
// instant it was read: not "contended", not "busy", FREE.
func (l *renderLock) heldFor(now time.Time) (time.Duration, int64) {
	since := l.since.Load()
	if since == 0 {
		return 0, 0
	}
	return now.Sub(time.Unix(0, since)), since
}

// freezeDir is where dumps land: beside the telemetry the daemon already
// writes, because that is the directory a reader is already being asked for.
func freezeDir() string {
	dir := filepath.Join(stateDir(), "freeze")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	freezeTrim.Do(func() { trimFreezeDir(dir, freezeKeep) })
	return dir
}

var freezeTrim sync.Once

// trimFreezeDir keeps the newest `keep` forensic files and removes the rest.
// The names are timestamps, so sorting them IS sorting by age. Called once per
// session: a directory nobody prunes is a directory nobody reads.
func trimFreezeDir(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !(strings.HasPrefix(n, "stacks-") || strings.HasPrefix(n, "cpu-")) {
			continue
		}
		names = append(names, n)
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		os.Remove(filepath.Join(dir, n))
	}
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

// watchRenderLock is the watchdog. Twice a second it reads the render lock's
// own clock and reports when ONE acquisition has stayed open past
// freezeStuckAfter -- once per acquisition, because a pager that is stuck
// stays stuck and a dump per poll would bury the one that matters.
//
// IT MEASURES WHAT THE MESSAGE CLAIMS. The old version polled TryLock and
// counted failures, which reports a busy lock as a frozen one (see renderLock).
// An episode here is a single holder that will not let go, identified by the
// stamp it wrote: when the stamp changes or goes to zero, that holder finished,
// and the pager was never frozen.
//
// Only the first episode of a session profiles. The profiler slows every frame
// it watches, and the second answer is the same as the first.
func watchRenderLock(l *renderLock) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(freezePollEvery)
		defer t.Stop()
		var reported int64 // the acquisition already written up
		dumps, profiled := 0, false
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				held, stamp := l.heldFor(now)
				if stamp == 0 || stamp == reported || held < freezeStuckAfter {
					continue
				}
				reported = stamp
				if dumps >= freezeDumpCap {
					continue
				}
				dumps++
				dumpGoroutines(fmt.Sprintf("one render lock acquisition open for %s", held.Round(time.Millisecond)))
				if !profiled {
					profiled = true
					profileCPU(freezeProfileFor)
				}
			}
		}
	}()
	return func() { close(done) }
}

// ProfileEnv names a directory; when set, the CLI writes a CPU profile of the
// whole session and a heap profile at exit there, so a benchmark run can be
// read with `go tool pprof` instead of guessed at.
const ProfileEnv = "FIGARO_CLI_PPROF"

func profileSession() func() {
	dir := os.Getenv(ProfileEnv)
	if dir == "" {
		return func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	stamp := fmt.Sprintf("%d-%s", os.Getpid(), time.Now().Format("150405"))
	cpu, err := os.Create(filepath.Join(dir, "cli-cpu-"+stamp+".pprof"))
	if err != nil {
		return func() {}
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		cpu.Close()
		return func() {}
	}
	return func() {
		pprof.StopCPUProfile()
		cpu.Close()
		if heap, err := os.Create(filepath.Join(dir, "cli-heap-"+stamp+".pprof")); err == nil {
			runtime.GC()
			_ = pprof.WriteHeapProfile(heap)
			heap.Close()
		}
	}
}
