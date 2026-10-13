package cli

// Freeze forensics: SIGUSR1 dumps goroutines, SIGUSR2 profiles CPU, and the
// watchdog dumps by itself when one render-lock acquisition outlives any honest
// frame.

import (
	"github.com/jack-work/figaro/internal/boot"

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

// One acquisition, not contention: a frame is milliseconds.
const freezeStuckAfter = 5 * time.Second

const freezePollEvery = 500 * time.Millisecond

// Every dump stops the world; the first describes a wedged pager.
const freezeDumpCap = 8

const freezeKeep = 200

const freezeProfileFor = 10 * time.Second

// renderLock remembers when the acquisition in force began. The watchdog must not
// poll TryLock: it fails on a merely busy mutex (and outright in starvation mode),
// which at the spinner's duty cycle reports freezes that never happened.
type renderLock struct {
	mu sync.Mutex
	// since is written by the holder and read by the watchdog; 0 when free.
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

// For callers that must never block (the exit hook, the memory mark).
func (l *renderLock) TryLock() bool {
	if !l.mu.TryLock() {
		return false
	}
	l.since.Store(time.Now().UnixNano())
	return true
}

// A zero duration means the lock was free at the instant it was read.
func (l *renderLock) heldFor(now time.Time) (time.Duration, int64) {
	since := l.since.Load()
	if since == 0 {
		return 0, 0
	}
	return now.Sub(time.Unix(0, since)), since
}

func freezeDir() string {
	dir := filepath.Join(boot.StateDir(), "freeze")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	freezeTrim.Do(func() { trimFreezeDir(dir, freezeKeep) })
	return dir
}

var freezeTrim sync.Once

// The names are timestamps, so sorting them is sorting by age.
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

// Never fails loudly: a diagnostic that panics is worse than none.
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
		boot.BuildRevision(), os.Getpid(), why, time.Now().Format(time.RFC3339Nano))
	if err := os.WriteFile(path, append([]byte(head), buf...), 0o600); err != nil {
		slog.Error("freeze dump failed", "err", err)
		return ""
	}
	slog.Error("freeze dump written", "path", path, "why", why)
	return path
}

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

// An episode is one holder, identified by its stamp; a changed or zero stamp means
// it finished. Only the first episode profiles: the profiler slows every frame it
// watches.
func watchRenderLock(l *renderLock) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(freezePollEvery)
		defer t.Stop()
		var reported int64
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

// ProfileEnv names a directory for a CPU profile of the session and a heap profile
// at exit.
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
