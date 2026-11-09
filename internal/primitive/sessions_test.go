package primitive

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu    sync.Mutex
	start map[int]uint64
}

func (f *fakeClock) clock() Clock {
	return Clock{
		StartOf: func(pid int) uint64 { f.mu.Lock(); defer f.mu.Unlock(); return f.start[pid] },
		Alive:   func(pid int) bool { f.mu.Lock(); defer f.mu.Unlock(); _, ok := f.start[pid]; return ok },
	}
}

func (f *fakeClock) set(pid int, start uint64) { f.mu.Lock(); f.start[pid] = start; f.mu.Unlock() }
func (f *fakeClock) kill(pid int)              { f.mu.Lock(); delete(f.start, pid); f.mu.Unlock() }

type counter struct {
	N    int      `json:"n"`
	Seen []string `json:"seen"`
}

func cloneCounter(c counter) counter { c.Seen = append([]string(nil), c.Seen...); return c }

func newSessions(t *testing.T, path string, clk *fakeClock) *Sessions[counter] {
	t.Helper()
	return New(path, clk.clock(), cloneCounter, nil)
}

func bump(s *Sessions[counter], pid int) counter {
	v, _ := s.Update(pid, func(c *counter) error { c.N++; return nil })
	return v
}

// One writer per session: concurrent updates to one session all land.
func TestUpdatesToOneSessionAreSerialised(t *testing.T) {
	clk := &fakeClock{start: map[int]uint64{7: 100}}
	s := newSessions(t, filepath.Join(t.TempDir(), "s.json"), clk)
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() { bump(s, 7) })
	}
	wg.Wait()
	if got := s.Get(7).N; got != 200 {
		t.Fatalf("n = %d, want 200", got)
	}
}

func TestAValueHandedOutIsACopy(t *testing.T) {
	clk := &fakeClock{start: map[int]uint64{7: 100}}
	s := newSessions(t, filepath.Join(t.TempDir(), "s.json"), clk)
	v, _ := s.Update(7, func(c *counter) error { c.Seen = append(c.Seen, "a"); return nil })
	v.Seen[0] = "mutated"
	if s.Get(7).Seen[0] != "a" {
		t.Fatal("a caller's copy reached the held value")
	}
}

func TestSavedSessionsComeBackForTheSameProcessOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	clk := &fakeClock{start: map[int]uint64{7: 100, 8: 200, 9: 300}}
	s := newSessions(t, path, clk)
	bump(s, 7)
	bump(s, 8)
	bump(s, 8)
	bump(s, 9)
	if err := s.Save(time.Hour); err != nil {
		t.Fatal(err)
	}
	clk.set(8, 201)
	clk.kill(9)
	again := newSessions(t, path, clk)
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	if again.Get(7).N != 1 {
		t.Fatalf("pid 7: %+v", again.Get(7))
	}
	if again.Get(8).N != 0 {
		t.Fatal("a reused pid took the old session's value")
	}
	if again.Get(9).N != 0 {
		t.Fatal("a dead pid's value came back")
	}
}

func TestAReusedPIDStartsFresh(t *testing.T) {
	clk := &fakeClock{start: map[int]uint64{7: 100}}
	s := newSessions(t, filepath.Join(t.TempDir(), "s.json"), clk)
	bump(s, 7)
	clk.set(7, 101)
	if s.Get(7).N != 0 || bump(s, 7).N != 1 {
		t.Fatal("a new process under an old pid saw the old value")
	}
}

func TestSweepDropsDeadSessions(t *testing.T) {
	clk := &fakeClock{start: map[int]uint64{7: 100, 8: 200}}
	s := newSessions(t, filepath.Join(t.TempDir(), "s.json"), clk)
	bump(s, 7)
	bump(s, 8)
	clk.kill(8)
	if gone := s.Sweep(); len(gone) != 1 || gone[0] != 8 {
		t.Fatalf("swept %v", gone)
	}
}

func TestACorruptFileFallsBackToTheBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	clk := &fakeClock{start: map[int]uint64{7: 100}}
	s := newSessions(t, path, clk)
	bump(s, 7)
	if err := s.Save(0); err != nil {
		t.Fatal(err)
	}
	bump(s, 7)
	if err := s.Save(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	if err := os.WriteFile(path, []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := newSessions(t, path, clk)
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	if again.Get(7).N != 1 {
		t.Fatalf("backup gave %+v, want the save before the last", again.Get(7))
	}
}

func TestNothingToLoadIsNotAnError(t *testing.T) {
	clk := &fakeClock{start: map[int]uint64{}}
	if err := newSessions(t, filepath.Join(t.TempDir(), "none.json"), clk).Load(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistSavesOnTheTimerAndOnTheWayOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	clk := &fakeClock{start: map[int]uint64{7: 100}}
	s := newSessions(t, path, clk)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { s.Persist(done, 10*time.Millisecond, time.Hour); close(stopped) }()
	bump(s, 7)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the timer never saved")
		}
		time.Sleep(5 * time.Millisecond)
	}
	bump(s, 7)
	close(done)
	<-stopped
	again := newSessions(t, path, clk)
	if err := again.Load(); err != nil || again.Get(7).N != 2 {
		t.Fatalf("after exit: %+v %v", again.Get(7), err)
	}
}

func TestOnChangeSeesEveryCommittedValue(t *testing.T) {
	clk := &fakeClock{start: map[int]uint64{7: 100}}
	var got []int
	s := New(filepath.Join(t.TempDir(), "s.json"), clk.clock(), cloneCounter, func(pid int, v counter) { got = append(got, v.N) })
	bump(s, 7)
	bump(s, 7)
	if len(got) != 2 || got[1] != 2 {
		t.Fatalf("onChange saw %v", got)
	}
}
