package log

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/store/disk"
)

func TestWriteBackpressureFlushesInline(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{MaxUnflushedBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	payload := bytes.Repeat([]byte{7}, 48)
	for i := uint64(1); i <= 10; i++ {
		if err := l.Write(i, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, last, ok := l.PendingBounds(); ok && last-l.inner.LastIndex() > 2 {
		t.Fatalf("lag not bounded: pending to %d, durable %d", last, l.inner.LastIndex())
	}
	if durable := l.inner.LastIndex(); durable < 8 {
		t.Fatalf("inline flush did not run: durable=%d", durable)
	}
}

func TestFlushRetrySkipsPersistedEntries(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		if err := l.Write(i, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Disk().Write(1, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := l.Disk().Write(2, []byte{2}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); err != nil {
		t.Fatalf("flush over partially persisted state: %v", err)
	}
	if _, _, ok := l.PendingBounds(); ok {
		t.Fatal("pending not trimmed after successful flush")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	for i := uint64(1); i <= 3; i++ {
		got, err := l2.Read(i)
		if err != nil || !bytes.Equal(got, []byte{byte(i)}) {
			t.Fatalf("idx %d: %v %v", i, got, err)
		}
	}
}

func TestFlushFailureKeepsPendingAndRetriesCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	dir := t.TempDir()
	l, err := Open(dir, Options{SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(1, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(2, []byte{2}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(3, bytes.Repeat([]byte{3}, 200)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := l.Sync(); err == nil {
		t.Fatal("first flush succeeded with no room to rotate into")
	}
	first, last, ok := l.PendingBounds()
	if !ok || first > 3 || last != 3 {
		t.Fatalf("pending after failure: %d..%d ok=%v", first, last, ok)
	}
	retryErr := l.Sync()
	if errors.Is(retryErr, disk.ErrOutOfOrder) {
		t.Fatal("retry re-wrote persisted entries")
	}
	if retryErr == nil {
		t.Fatal("retry succeeded with no room to rotate into")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); err != nil {
		t.Fatalf("flush once the directory is writable: %v", err)
	}
	if _, _, ok := l.PendingBounds(); ok {
		t.Fatal("pending left after a clean flush")
	}
	got, err := l.Read(3)
	if err != nil || len(got) != 200 {
		t.Fatalf("read 3: %d bytes, %v", len(got), err)
	}
}
