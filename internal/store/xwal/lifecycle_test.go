package xwal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoveDirtyTrunkDoesNotPoisonStore(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 20 * time.Millisecond
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(a, "ir", 0, []byte(`{"x":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(a, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := s.Append(b, "ir", 0, []byte(`{"y":2}`), nil); err != nil {
		t.Fatalf("append to healthy trunk after removing dirty trunk: %v", err)
	}
	s.mu.Lock()
	_, dirtyA := s.dirty[a]
	failsA := s.lineageFails[a]
	s.mu.Unlock()
	if dirtyA || failsA != 0 {
		t.Fatalf("removed trunk bookkeeping not purged: dirty=%v fails=%d", dirtyA, failsA)
	}
}

func TestRawTrunksRemoveIsPurgedByFlusher(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 20 * time.Millisecond
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(a, "ir", 0, []byte(`{"x":1}`), nil); err != nil {
		t.Fatal(err)
	}
	// Bypass the Store override (legacy embedded-call shape).
	if err := s.Trunks.Remove(a, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := s.Append(b, "ir", 0, []byte(`{"y":2}`), nil); err != nil {
		t.Fatalf("append to healthy trunk after raw remove: %v", err)
	}
}

func TestPoisonIsPerLineage(t *testing.T) {
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
	bad, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(bad, "ir", 0, []byte(`{"warm":1}`), nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "warm flush", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.dirty) == 0
	})
	var badBranch string
	for _, ti := range s.ListLight() {
		if ti.ID == bad {
			badBranch = ti.Head[0]
		}
	}
	badDir := filepath.Join(dir, "ir", badBranch)
	if err := os.Chmod(badDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(badDir, 0o755) })

	big := `{"pad":"` + string(make([]byte, 0)) + `xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`
	deadline := time.Now().Add(10 * time.Second)
	var badErr error
	for {
		if time.Now().After(deadline) {
			t.Fatal("bad lineage never poisoned")
		}
		_, badErr = s.Append(bad, "ir", 0, []byte(big), nil)
		if badErr != nil {
			break
		}
		if _, err := s.Append(good, "ir", 0, []byte(`{"g":1}`), nil); err != nil {
			t.Fatalf("healthy lineage affected while bad lineage degrades: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !isStorePoisoned(badErr) {
		t.Fatalf("bad lineage append error = %v", badErr)
	}
	for i := 0; i < 10; i++ {
		if _, err := s.Append(good, "ir", 0, []byte(`{"g":2}`), nil); err != nil {
			t.Fatalf("healthy lineage poisoned by sibling failures: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	failsBad := s.lineageFails[bad]
	s.mu.Unlock()
	if failsBad < storePoisonThreshold {
		t.Fatalf("good-lineage successes reset the bad lineage counter: %d", failsBad)
	}
}

func TestFlushStumpMakesBirthDurable(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateStump("cfg"); err != nil {
		t.Fatal(err)
	}
	sx, err := s.StumpHead("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sx.AppendMain([]byte(`{"birth":"payload-sentinel"}`), nil); err != nil {
		sx.Close()
		t.Fatal(err)
	}
	if err := sx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncStump("cfg"); err != nil {
		t.Fatal(err)
	}
	found := false
	filepath.Walk(filepath.Join(dir, "ir", "cfg"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, rerr := os.ReadFile(p); rerr == nil && len(b) > 0 && string(b) != "" {
				if containsBytes(b, []byte("payload-sentinel")) {
					found = true
				}
			}
		}
		return nil
	})
	if !found {
		t.Fatal("stump birth record not durable after SyncStump")
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestStoreClearDoesNotResurrectPending(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = 10 * time.Millisecond
	opts.Opaque = []string{"translations"}
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	lt, err := s.Append(tr, "ir", 0, []byte(`{"turn":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s.Append(tr, "translations", lt, []byte("stale-fingerprint-record"), nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Kick()
	if err := s.Clear(tr, "translations"); err != nil {
		t.Fatalf("Store.Clear: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if rec, ok, err := s.Lookup(tr, "translations", lt); err != nil || ok {
		t.Fatalf("cleared record resurrected: %+v ok=%v err=%v", rec, ok, err)
	}
	clt, err := s.Append(tr, "translations", lt, []byte("fresh-record"), nil)
	if err != nil {
		t.Fatalf("append after clear: %v", err)
	}
	if clt != 1 {
		t.Fatalf("cleared channel did not reset: first append at %d", clt)
	}
	time.Sleep(100 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	found := false
	filepath.Walk(filepath.Join(dir, "translations"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, rerr := os.ReadFile(p); rerr == nil && containsBytes(b, []byte("stale-fingerprint-record")) {
				found = true
			}
		}
		return nil
	})
	if found {
		t.Fatal("stale records present on disk after Clear + flush window")
	}
	s2, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rec, ok, err := s2.Lookup(tr, "translations", lt)
	if err != nil || !ok || string(rec.Payload) != "fresh-record" {
		t.Fatalf("post-clear record after reopen: %+v ok=%v err=%v", rec, ok, err)
	}
}

func TestTopologyMutationRefusedWhileFlushFailing(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.SyncInterval = time.Hour
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
	if _, err := s.Append(tr, "ir", 0, []byte(`{"seed":1}`), nil); err != nil {
		t.Fatal(err)
	}
	s.Kick()
	waitFor(t, 5*time.Second, "seed flush", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.dirty) == 0
	})
	var branch string
	for _, ti := range s.ListLight() {
		if ti.ID == tr {
			branch = ti.Head[0]
		}
	}
	mainDir := filepath.Join(dir, "ir", branch)

	// Fill past a segment so the next flush needs a rotation, then make
	// the dir unwritable and append (pending only; SyncInterval is huge).
	pad := make([]byte, 150)
	for i := range pad {
		pad[i] = 'x'
	}
	big := []byte(`{"pad":"` + string(pad) + `"}`)
	acked := 0
	for i := 0; i < 4; i++ {
		if _, err := s.Append(tr, "ir", 0, big, nil); err != nil {
			t.Fatal(err)
		}
		acked++
	}
	if err := os.Chmod(mainDir, 0o555); err != nil {
		t.Fatal(err)
	}
	restore := func() { os.Chmod(mainDir, 0o755) }
	defer restore()

	if _, err := s.Fork(tr, 2); err == nil {
		t.Fatal("fork proceeded while flush failing (would truncate acked tail)")
	}
	restore()

	alt, err := s.Fork(tr, 2)
	if err != nil {
		t.Fatalf("fork after restore: %v", err)
	}
	if alt == "" || alt == tr {
		t.Fatalf("fork result: %q", alt)
	}
	chans, err := s.Channels(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chans {
		if c.Name == "ir" && c.Last != uint64(2+acked) {
			t.Fatalf("acked tail truncated: last=%d want %d", c.Last, 2+acked)
		}
	}
}
