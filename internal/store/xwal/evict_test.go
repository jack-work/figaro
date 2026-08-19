package xwal

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestIdleLineageEvictsAndReloads(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 10 * time.Millisecond
	opts.IdleUnload = 30 * time.Millisecond
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	var lts []uint64
	for i := 0; i < 5; i++ {
		lt, err := s.Append(tr, "ir", 0, []byte(fmt.Sprintf(`{"n":%d}`, i)), nil)
		if err != nil {
			t.Fatal(err)
		}
		lts = append(lts, lt)
		patch, _ := MapSetPatch([]string{"n"}, []byte(itoa(lt)))
		if _, err := s.Append(tr, "chalkboard", lt, patch, nil); err != nil {
			t.Fatal(err)
		}
	}
	if s.LoadedHeads() == 0 {
		t.Fatal("head not loaded after appends")
	}

	waitFor(t, 5*time.Second, "idle eviction", func() bool { return s.LoadedHeads() == 0 })

	// A read transparently reloads and sees everything.
	m, payload, err := s.Read(tr, "ir", lts[4])
	if err != nil || m != lts[4] || string(payload) != `{"n":4}` {
		t.Fatalf("read after evict: m=%d payload=%s err=%v", m, payload, err)
	}
	// THE RELOAD IS OBSERVED WITH A RETRY, NOT AT AN INSTANT. IdleUnload is
	// 30ms in this fixture and the unloader runs in the background, so under a
	// loaded machine it can unload the head again between the read and the
	// check -- which is a correct system and a racing assertion. Two arias saw
	// this fail once each (fd15d2a0 and 041454f1, 2026-08-19) and neither could
	// reproduce it in isolation, which is the signature of exactly that window.
	reloaded := s.LoadedHeads() != 0
	for deadline := time.Now().Add(time.Second); !reloaded && time.Now().Before(deadline); {
		if _, _, err := s.Read(tr, "ir", lts[4]); err != nil {
			t.Fatalf("re-read after evict: %v", err)
		}
		reloaded = s.LoadedHeads() != 0
	}
	if !reloaded {
		t.Fatal("read did not reload the head")
	}
	rec, ok, err := s.Lookup(tr, "chalkboard", lts[4])
	if err != nil || !ok {
		t.Fatalf("chalkboard after evict: ok=%v err=%v", ok, err)
	}
	state, err := s.StateAt(tr, "chalkboard", rec.ChannelLT)
	if err != nil || string(state) != fmt.Sprintf(`{"n":%d}`, lts[4]) {
		t.Fatalf("state after evict: %s err=%v", state, err)
	}

	// A later append lands at the right index.
	lt, err := s.Append(tr, "ir", 0, []byte(`{"n":99}`), nil)
	if err != nil || lt != lts[4]+1 {
		t.Fatalf("append after evict: lt=%d err=%v", lt, err)
	}
}

func TestEvictionNeverLosesPendingAppends(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 2 * time.Millisecond
	opts.IdleUnload = 2 * time.Millisecond
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	const total = 400
	var last uint64
	for i := 0; i < total; i++ {
		lt, err := s.Append(tr, "ir", 0, []byte(fmt.Sprintf(`{"i":%d}`, i)), nil)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		last = lt
		if i%20 == 0 {
			time.Sleep(4 * time.Millisecond)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i := 0; i < total; i++ {
		lt := last - uint64(total-1-i)
		_, payload, err := s2.Read(tr, "ir", lt)
		if err != nil || string(payload) != fmt.Sprintf(`{"i":%d}`, i) {
			t.Fatalf("entry %d (lt %d): %s err=%v", i, lt, payload, err)
		}
	}
}

func TestBorrowedHeadIsNotEvicted(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 5 * time.Millisecond
	opts.IdleUnload = 10 * time.Millisecond
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(tr, "ir", 0, []byte(`{"n":1}`), nil); err != nil {
		t.Fatal(err)
	}
	head, err := s.Head(tr)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if s.LoadedHeads() == 0 {
		head.Close()
		t.Fatal("borrowed head was evicted")
	}
	if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "eviction after release", func() bool { return s.LoadedHeads() == 0 })
}

func TestFlushFailuresPoisonAppendsAndRecover(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 5 * time.Millisecond
	opts.IdleUnload = -1
	opts.SegmentSize = 256
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(tr, "ir", 0, []byte(`{"warm":true}`), nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "warm flush", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.dirty) == 0
	})

	var headBranch string
	for _, ti := range s.ListLight() {
		if ti.ID == tr {
			headBranch = ti.Head[0]
		}
	}
	mainDir := filepath.Join(dir, "ir", headBranch)
	if err := os.Chmod(mainDir, 0o500); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			os.Chmod(mainDir, 0o755)
		}
	}
	defer restore()

	// Fill segments so every flush needs a rotation (a new file in the
	// unwritable dir).
	big := fmt.Sprintf(`{"pad":%q}`, bytes.Repeat([]byte("x"), 150))
	deadline := time.Now().Add(10 * time.Second)
	var appendErr error
	for {
		if time.Now().After(deadline) {
			t.Fatal("store never poisoned")
		}
		_, appendErr = s.Append(tr, "ir", 0, []byte(big), nil)
		if appendErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !isStorePoisoned(appendErr) {
		t.Fatalf("append error = %v, want poisoned-store error", appendErr)
	}

	restore()
	waitFor(t, 10*time.Second, "poison to clear", func() bool {
		_, err := s.Append(tr, "ir", 0, []byte(`{"after":true}`), nil)
		return err == nil
	})
}

func isStorePoisoned(err error) bool {
	return err != nil && strings.Contains(err.Error(), "flushes failing")
}
