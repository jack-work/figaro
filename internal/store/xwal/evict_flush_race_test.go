package xwal

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Retiring an idle hot store closes it, and its Close is what flushes the
// entries it buffered. A new hot store opened before that flush lands reads
// the directory's tail short and hands an append the LT the buffered entry
// already holds: two acknowledged appends, one record. Here the old store's
// Close is held while the next append opens a new store.
func TestAnAppendAfterARetirementNeverReusesABufferedLT(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil)
	if err != nil {
		t.Fatal(err)
	}

	reached, release := make(chan struct{}), make(chan struct{})
	s.Trunks.testStoreClose = func() { close(reached); <-release }
	retired := make(chan struct{})
	go func() { s.Trunks.retireHot(); close(retired) }()
	<-reached

	type result struct {
		lt  uint64
		err error
	}
	next := make(chan result, 1)
	go func() {
		lt, err := s.Append(tr, "ir", 0, []byte(`{"i":1}`), nil)
		next <- result{lt, err}
	}()
	var second result
	select {
	case second = <-next:
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	<-retired
	if second == (result{}) {
		second = <-next
	}
	s.Trunks.testStoreClose = nil
	if second.err != nil {
		t.Fatal(second.err)
	}
	if second.lt == first {
		t.Errorf("the append after the retirement was given LT %d, the buffered entry's LT", first)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, lt := range []uint64{first, second.lt} {
		_, payload, err := s2.Read(tr, "ir", lt)
		if want := fmt.Sprintf(`{"i":%d}`, i); err != nil || string(payload) != want {
			t.Fatalf("append %d at LT %d reads %s (err %v), want %s", i, lt, payload, err, want)
		}
	}
}

// The same race through a store retired while a reader still borrows it: the
// store is not closed then, so its buffered entry stays unflushed while the
// next append opens a new store over the same directory.
func TestAnAppendAfterABorrowedStoreRetiresNeverReusesABufferedLT(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := s.Trunks.Head(tr)
	if err != nil {
		t.Fatal(err)
	}
	s.Trunks.retireHot()
	second, err := s.Append(tr, "ir", 0, []byte(`{"i":1}`), nil)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Errorf("the append after the retirement was given LT %d, the buffered entry's LT", first)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, lt := range []uint64{first, second} {
		_, payload, err := s2.Read(tr, "ir", lt)
		if want := fmt.Sprintf(`{"i":%d}`, i); err != nil || string(payload) != want {
			t.Fatalf("append %d at LT %d reads %s (err %v), want %s", i, lt, payload, err, want)
		}
	}
}

// The third order. An append borrows the hot store, the store is retired and
// synced, and only then does the append write, into the retired store. Its
// release closes the store outside hotMu, but appendMainWith releases before
// it drops the lineage lock, so that close (the flush) finishes before the
// next append on the lineage can borrow. Here the close is held: the next
// append must still be waiting on the lineage when it is let go.
func TestAnAppendThatWritesIntoARetiredStoreIsFlushedBeforeTheNext(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(tr, "ir", 0, []byte(`{"i":-1}`), nil); err != nil {
		t.Fatal(err)
	}

	borrowed, write := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.Trunks.testAppendBorrow = func() {
		once.Do(func() {
			close(borrowed)
			<-write
		})
	}
	type result struct {
		lt  uint64
		err error
	}
	first := make(chan result, 1)
	go func() {
		lt, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil)
		first <- result{lt, err}
	}()
	<-borrowed
	s.Trunks.retireHot()

	closing, closeNow := make(chan struct{}), make(chan struct{})
	var closeOnce sync.Once
	s.Trunks.testStoreClose = func() {
		closeOnce.Do(func() {
			close(closing)
			<-closeNow
		})
	}
	close(write)
	<-closing
	second := make(chan result, 1)
	go func() {
		lt, err := s.Append(tr, "ir", 0, []byte(`{"i":1}`), nil)
		second <- result{lt, err}
	}()
	select {
	case r := <-second:
		close(closeNow)
		t.Fatalf("the next append took LT %d while the retired store's flush was still held", r.lt)
	case <-time.After(300 * time.Millisecond):
	}
	close(closeNow)
	a, b := <-first, <-second
	s.Trunks.testAppendBorrow, s.Trunks.testStoreClose = nil, nil
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	if a.lt == b.lt {
		t.Fatalf("both appends were given LT %d", a.lt)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, lt := range []uint64{a.lt, b.lt} {
		_, payload, err := s2.Read(tr, "ir", lt)
		if want := fmt.Sprintf(`{"i":%d}`, i); err != nil || string(payload) != want {
			t.Fatalf("append %d at LT %d reads %s (err %v), want %s", i, lt, payload, err, want)
		}
	}
}

// Through a fork, not the idle sweep, and through a peer: a fork below a
// trunk's own first entry retires the hot stores of every Trunks open on the
// root (fork.go, ForkAt -> retireTrunkStores). The forking peer flushes only
// its own store first; this one is retired while a reader still borrows it,
// with an append buffered. That append must be on disk before the next append
// here opens a new store.
func TestAPeersForkThatRetiresThisStoreNeverLetsTheNextAppendReuseALT(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(other, "ir", 0, []byte(fmt.Sprintf(`{"o":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	forked, err := s.Trunks.ForkTail(other)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	s.syncDirty()
	peer, err := openTrunks(dir, s.Trunks.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	first, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := s.Trunks.Head(tr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ForkAt(forked, 1); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	second, err := s.Append(tr, "ir", 0, []byte(`{"i":1}`), nil)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Errorf("the append after the peer's fork was given LT %d, the buffered entry's LT", first)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, lt := range []uint64{first, second} {
		_, payload, err := s2.Read(tr, "ir", lt)
		if want := fmt.Sprintf(`{"i":%d}`, i); err != nil || string(payload) != want {
			t.Fatalf("append %d at LT %d reads %s (err %v), want %s", i, lt, payload, err, want)
		}
	}
}

// Fail closed: when a retired store's tail cannot be flushed (ENOSPC, EIO),
// the node refuses new heads instead of letting the next append read a short
// tail and reuse an LT. Once a retry flushes the tail, appends go on, and
// nothing is lost.
func TestAFailedFlushAtRetirementRefusesTheNodeUntilItFlushes(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	node, _ := s.HeadNode(tr)
	var mu sync.Mutex
	failing := true
	s.Trunks.testHeadSync = func(key string) error {
		mu.Lock()
		defer mu.Unlock()
		if failing && key == node {
			return fmt.Errorf("no space left on device")
		}
		return nil
	}
	s.Trunks.retireHot()
	if lt, err := s.Append(tr, "ir", 0, []byte(`{"i":1}`), nil); err == nil {
		t.Fatalf("an append while the retired tail is unflushed was given LT %d", lt)
	} else if !strings.Contains(err.Error(), "could not flush") {
		t.Fatalf("refused with %v", err)
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	second, err := s.Append(tr, "ir", 0, []byte(`{"i":1}`), nil)
	if err != nil {
		t.Fatalf("after the flush succeeds: %v", err)
	}
	s.Trunks.testHeadSync = nil
	if second != first+1 {
		t.Fatalf("after the retry the append got LT %d, want %d", second, first+1)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, lt := range []uint64{first, second} {
		_, payload, err := s2.Read(tr, "ir", lt)
		if want := fmt.Sprintf(`{"i":%d}`, i); err != nil || string(payload) != want {
			t.Fatalf("append %d at LT %d reads %s (err %v), want %s", i, lt, payload, err, want)
		}
	}
}

// The fence is per root: while one Trunks holds a retired store whose tail on
// a node could not be flushed, a peer Trunks on the same root is refused that
// node too, until the holder's flush succeeds.
func TestAPeerIsRefusedANodeAnotherTrunksCouldNotFlush(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	s.syncDirty()
	peer, err := openTrunks(dir, s.Trunks.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	first, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	node, _ := s.HeadNode(tr)
	var mu sync.Mutex
	failing := true
	s.Trunks.testHeadSync = func(key string) error {
		mu.Lock()
		defer mu.Unlock()
		if failing && key == node {
			return fmt.Errorf("input/output error")
		}
		return nil
	}
	s.Trunks.retireHot()

	if _, lt, err := peer.Append(tr, 0, []byte(`{"i":1}`), nil); err == nil {
		t.Fatalf("a peer appended at LT %d while another Trunks held the node's tail unflushed", lt)
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	_, second, err := peer.Append(tr, 0, []byte(`{"i":1}`), nil)
	if err != nil {
		t.Fatalf("after the holder's flush succeeds: %v", err)
	}
	s.Trunks.testHeadSync = nil
	if second != first+1 {
		t.Fatalf("the peer's append got LT %d, want %d", second, first+1)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, lt := range []uint64{first, second} {
		_, payload, err := s2.Read(tr, "ir", lt)
		if want := fmt.Sprintf(`{"i":%d}`, i); err != nil || string(payload) != want {
			t.Fatalf("append %d at LT %d reads %s (err %v), want %s", i, lt, payload, err, want)
		}
	}
}
