package log

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jack-work/figaro/internal/store/segment"
)

func TestStoreSiblingForksSharePhysicalPrefixAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	root, err := Open(dir, Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 4; i++ {
		if err := root.Write(i, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	a, err := root.Fork(3, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := root.Fork(3, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	defer store.Close()
	a, err = store.Open(filepath.Join(dir, "a"), Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err = store.Open(filepath.Join(dir, "b"), Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Disk().Parent() != b.Disk().Parent() {
		t.Fatal("sibling logs copied their shared prefix")
	}
	if a.Disk().Parent() != b.Disk().Parent() {
		t.Fatal("sibling disk logs do not share the same prefix location")
	}
	if got, err := a.Read(2); err != nil || string(got) != `{"i":2}` {
		t.Fatalf("shared prefix read = %q, %v", got, err)
	}
	if _, err := a.Fork(3, "nested"); !errors.Is(err, ErrSharedMutation) {
		t.Fatalf("shared fork error = %v, want ErrSharedMutation", err)
	}
}

func TestStoreConcurrentReadAppend(t *testing.T) {
	store := NewStore()
	defer store.Close()
	c, err := store.Open(t.TempDir(), Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 20; i++ {
		if err := c.Write(i, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		// Bounded: an unthrottled memory-speed writer makes the readers'
		// full-log scans quadratic and the cache grow without limit.
		for i := uint64(21); i <= 20_000; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := c.Write(i, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				snap := c.Snapshot()
				var previous uint64
				if err := snap.Range(1, func(idx uint64, payload []byte) error {
					if previous != 0 && idx != previous+1 {
						return fmt.Errorf("gap after %d: %d", previous, idx)
					}
					previous = idx
					if string(payload) != fmt.Sprintf(`{"i":%d}`, idx) {
						return fmt.Errorf("bad payload at %d: %q", idx, payload)
					}
					return nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-writerDone
}
