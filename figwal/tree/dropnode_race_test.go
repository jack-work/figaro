package tree

import (
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Severity probe: DropNode on one tenant of a node while another reads it
// through a Handle, the pair two Segment instances of one path make. Readers
// recover panics and check every unit's key, so the run classifies what the
// interleaving does: panic, wrong unit, or misses only.
func TestDropNodeAgainstConcurrentHandleReads(t *testing.T) {
	dur := 2 * time.Second
	if s := os.Getenv("PCR_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		dur = time.Duration(n) * time.Second
	}
	const records = 256
	c := New(func(co Coord) ([]unit, error) {
		var out []unit
		for k := co.From + 1; k <= co.To; k++ {
			out = append(out, unit{k: k, s: fmt.Sprintf("p-%d", k)})
		}
		return out, nil
	}, NewBudget(0), func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
	h := c.Node("p")

	var hits, misses, wrong, panics, shortRange, shortResident atomic.Int64
	var panicSites sites
	var firstPanic, firstWrong, firstShort atomic.Value
	stop := make(chan struct{})
	var wg sync.WaitGroup
	check := func(idx uint64, u unit) {
		if u.k != idx || u.s != fmt.Sprintf("p-%d", idx) {
			wrong.Add(1)
			firstWrong.CompareAndSwap(nil, fmt.Sprintf("idx %d got %+v", idx, u))
		}
	}
	for r := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := uint64(r); ; i++ {
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
					idx := i%records + 1
					if u, ok := h.At(idx); ok {
						hits.Add(1)
						check(idx, u)
					} else {
						misses.Add(1)
					}
					lo := (idx - 1) / 32 * 32
					us, err := h.Range(lo, lo+32)
					if err != nil {
						return
					}
					if len(us) != 32 {
						shortRange.Add(1)
						firstShort.CompareAndSwap(nil, fmt.Sprintf("Range(%d,%d) returned %d units, nil error", lo, lo+32, len(us)))
					}
					for j, u := range us {
						check(lo+uint64(j)+1, u)
					}
					if rs, ok := h.ResidentAt(lo, lo+32); ok {
						if len(rs) != 32 {
							shortResident.Add(1)
							firstShort.CompareAndSwap(nil, fmt.Sprintf("ResidentAt(%d,%d) ok with %d units", lo, lo+32, len(rs)))
						}
						for j, u := range rs {
							check(lo+uint64(j)+1, u)
						}
					}
				}()
			}
		}()
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
			c.DropNode("p")
		}
	}()
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	t.Logf("hits %d misses %d wrong %d short Range %d short ResidentAt %d panics %d", hits.Load(), misses.Load(), wrong.Load(), shortRange.Load(), shortResident.Load(), panics.Load())
	if s := firstShort.Load(); s != nil {
		t.Errorf("short read: %v", s)
	}
	for k, v := range panicSites.m {
		t.Logf("panic x%d: %s", v, k)
	}
	if p := firstPanic.Load(); p != nil {
		t.Errorf("reader panicked: %v", p)
	}
	if w := firstWrong.Load(); w != nil {
		t.Errorf("wrong unit: %v", w)
	}
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

// A reader that loaded a node's index before DropNode keeps every run it
// loaded: Drop publishes successors, it never edits a published run.
func TestDropNodeLeavesLoadedRunsIntact(t *testing.T) {
	c := New(func(co Coord) ([]unit, error) {
		var out []unit
		for k := co.From + 1; k <= co.To; k++ {
			out = append(out, unit{k: k, s: fmt.Sprintf("p-%d", k)})
		}
		return out, nil
	}, NewBudget(0), func(u unit) int { return len(u.s) }, func(u unit) uint64 { return u.k })
	h := c.Node("p")
	if _, err := h.Range(0, 64); err != nil {
		t.Fatal(err)
	}
	loaded := h.n.index()
	if loaded == nil || len(loaded.runs) == 0 {
		t.Fatal("no runs after a range read")
	}
	c.DropNode("p")
	for _, r := range loaded.runs {
		if !r.resident || len(r.units) == 0 {
			t.Fatalf("run %v was hollowed in place: resident=%v units=%d", r.coord, r.resident, len(r.units))
		}
	}
	if _, ok := h.At(1); ok {
		t.Fatal("At still hits after DropNode")
	}
}
