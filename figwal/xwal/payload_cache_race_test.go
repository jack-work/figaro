package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/figwal/segment"
)

// Severity probe for the payload-cache drop race: readers Range a trunk
// through the hot store while forks and detaches retire it, with the cache
// on. Every payload read is checked against the bytes on disk, so a run
// served from another coordinate shows as a mismatch, and a panic in a
// reader is caught and counted rather than taking the test binary down.
func TestPayloadCacheRetireUnderConcurrentRange(t *testing.T) {
	dur := 2 * time.Second
	if s := os.Getenv("PCR_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		dur = time.Duration(n) * time.Second
	}
	readers := 8
	if s := os.Getenv("PCR_READERS"); s != "" {
		readers, _ = strconv.Atoi(s)
	}
	segment.SetCacheBudget(32 << 20)

	dir := filepath.Join(t.TempDir(), "f")
	f, parent := seedMapTrunk(t, dir)
	for i := range 300 {
		if _, _, err := f.Append(parent, 0, fmt.Appendf(nil, `"p%d"`, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		if _, _, err := f.Append(child, 0, fmt.Appendf(nil, `"c%d"`, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	trunks := []TrunkID{parent, child}

	segment.SetCacheBudget(0)
	truth := map[TrunkID]map[uint64]string{}
	for _, tr := range trunks {
		truth[tr] = readMain(t, f, tr)
	}
	segment.SetCacheBudget(32 << 20)

	var reads, mismatches, short, panics, errs, retires atomic.Int64
	var panicSites sites
	var firstPanic, firstMismatch, firstShort atomic.Value
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr := trunks[r%len(trunks)]
			want := truth[tr]
			for {
				select {
				case <-stop:
					return
				default:
				}
				func() {
					defer func() {
						if p := recover(); p != nil {
							panics.Add(1)
							firstPanic.CompareAndSwap(nil, fmt.Sprintf("%v\n%s", p, debug.Stack()))
							panicSites.record(fmt.Sprint(p), debug.Stack())
						}
					}()
					x, err := f.Head(tr)
					if err != nil {
						errs.Add(1)
						return
					}
					defer x.Close()
					var seen int
					var prev uint64
					gap := false
					rerr := x.channel(f.main).mustLog().Range(1, func(idx uint64, b []byte) error {
						reads.Add(1)
						if seen > 0 && idx != prev+1 {
							gap = true
						}
						seen++
						prev = idx
						if w, ok := want[idx]; !ok || w != string(b) {
							mismatches.Add(1)
							firstMismatch.CompareAndSwap(nil, fmt.Sprintf("%s idx %d: got %q want %q", tr, idx, b, w))
						}
						return nil
					})
					if rerr != nil {
						errs.Add(1)
						return
					}
					if gap || seen != len(want) {
						short.Add(1)
						firstShort.CompareAndSwap(nil, fmt.Sprintf("%s: Range yielded %d of %d records, gap=%v", tr, seen, len(want), gap))
					}
				}()
			}
		}()
	}
	sib, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := f.Detach(f.head(sib)); err != nil {
				errs.Add(1)
				continue
			}
			retires.Add(1)
		}
	}()
	time.Sleep(dur)
	close(stop)
	wg.Wait()

	t.Logf("reads %d, retiring mutations %d, errors %d, panics %d, mismatches %d, short ranges %d", reads.Load(), retires.Load(), errs.Load(), panics.Load(), mismatches.Load(), short.Load())
	if s := firstShort.Load(); s != nil {
		t.Errorf("short read: %v", s)
	}
	for k, v := range panicSites.m {
		t.Logf("panic x%d: %s", v, k)
	}
	if p := firstPanic.Load(); p != nil {
		t.Errorf("reader panicked: %v", p)
	}
	if m := firstMismatch.Load(); m != nil {
		t.Errorf("payload mismatch: %v", m)
	}
}

func readMain(t *testing.T, f *Trunks, tr TrunkID) map[uint64]string {
	t.Helper()
	x, err := f.Head(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	out := map[uint64]string{}
	if err := x.channel(f.main).mustLog().Range(1, func(idx uint64, b []byte) error {
		out[idx] = string(b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// sites counts panics by the frame that raised them.
type sites struct {
	mu sync.Mutex
	m  map[string]int
}

func (s *sites) record(msg string, stack []byte) {
	lines := strings.Split(string(stack), "\n")
	at := ""
	for i, l := range lines {
		if strings.HasPrefix(l, "panic(") && i+3 < len(lines) {
			at = strings.TrimSpace(lines[i+2]) + " " + strings.TrimSpace(lines[i+3])
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]int{}
	}
	s.m[msg+" @ "+at]++
}
