package cli

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/mark"
)

// markMemory samples the process and the pager's retained state on a clock
// while marks are on. Stops with the returned func.
func markMemory(mu *sync.Mutex, lt *livelogTurn, every time.Duration) func() {
	if !mark.Enabled() {
		return func() {}
	}
	done := make(chan struct{})
	sample := func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		msgs, rows := -1, -1
		if mu.TryLock() {
			msgs, rows = lt.tr.retained()
			mu.Unlock()
		}
		mark.Mark("mem", "rss", rssBytes(), "heap_alloc", ms.HeapAlloc, "heap_inuse", ms.HeapInuse,
			"sys", ms.Sys, "gc", ms.NumGC, "goroutines", runtime.NumGoroutine(),
			"window_msgs", msgs, "rowcache_rows", rows)
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		sample()
		for {
			select {
			case <-done:
				sample()
				return
			case <-t.C:
				sample()
			}
		}
	}()
	return func() { close(done) }
}

// rssBytes is the resident set from /proc, or 0 where there is none.
func rssBytes() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(f[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}
