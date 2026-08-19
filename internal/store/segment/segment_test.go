package segment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverTornWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.seg")
	s, _ := Create(path, BinaryCodec{}, 1, 0)
	off1, _ := s.Append([]byte("good"))
	s.Sync()
	s.Close()

	// Simulate torn write: append a partial frame.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	f.Write([]byte{0xFF, 0xFF})
	f.Close()

	s2, err := Open(path, BinaryCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Count() != 1 {
		t.Fatalf("count=%d", s2.Count())
	}
	got, _ := s2.ReadAt(off1)
	if string(got) != "good" {
		t.Fatalf("got %q", got)
	}
}

func TestSegmentFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.seg")
	s, _ := Create(path, BinaryCodec{}, 1, 32)
	defer s.Close()
	if _, err := s.Append([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]byte("world-too-big-now")); err != ErrFull {
		t.Fatalf("want ErrFull, got %v", err)
	}
}

func TestReadIndexBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.seg")
	s, _ := Create(path, BinaryCodec{}, 10, 0)
	defer s.Close()

	entries := []string{"a", "bb", "ccc"}
	for _, e := range entries {
		if _, err := s.Append([]byte(e)); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range entries {
		got, err := s.ReadIndex(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("i=%d got %q want %q", i, got, want)
		}
	}
	if _, err := s.ReadIndex(99); err != ErrOutOfRange {
		t.Fatalf("want ErrOutOfRange, got %v", err)
	}
	if s.FirstIndex() != 10 || s.LastIndex() != 12 {
		t.Fatalf("range %d..%d", s.FirstIndex(), s.LastIndex())
	}
}

func TestReadIndexJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.jsonl")
	s, _ := Create(path, JSONLCodec{}, 10, 0)
	defer s.Close()

	entries := []string{`{"a":1}`, `{"b":2}`, `{"c":3}`}
	for _, e := range entries {
		if _, err := s.Append([]byte(e)); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range entries {
		got, err := s.ReadIndex(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("i=%d got %q want %q", i, got, want)
		}
	}
}

func TestReopenJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.jsonl")
	s, _ := Create(path, JSONLCodec{}, 1, 0)
	for _, e := range []string{`{"a":1}`, `{"b":2}`} {
		s.Append([]byte(e))
	}
	s.Sync()
	s.Close()

	s2, err := Open(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Count() != 2 {
		t.Fatalf("count=%d", s2.Count())
	}
	got, _ := s2.ReadIndex(1)
	if string(got) != `{"b":2}` {
		t.Fatalf("got %q", got)
	}
}

func TestRecoverTornWriteJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.jsonl")
	s, _ := Create(path, JSONLCodec{}, 1, 0)
	if _, err := s.Append([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	s.Sync()
	s.Close()

	// Append a partial line that will never decode.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	f.Write([]byte(`{"idx":1,"hash":"deadbeef","value":{"a":`))
	f.Close()

	fi, _ := os.Stat(path)
	preSize := fi.Size()

	s2, err := Open(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Count() != 1 {
		t.Fatalf("count=%d", s2.Count())
	}
	got, _ := s2.ReadIndex(0)
	if string(got) != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
	fi2, _ := os.Stat(path)
	if fi2.Size() >= preSize {
		t.Fatalf("expected file truncated, got size %d (was %d)", fi2.Size(), preSize)
	}
}

func TestOpenReadOnlyDoesNotTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.jsonl")
	s, _ := Create(path, JSONLCodec{}, 1, 0)
	s.Append([]byte(`{"a":1}`))
	s.Sync()
	s.Close()

	// Add a torn tail.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	f.Write([]byte(`garbage`))
	f.Close()
	fi, _ := os.Stat(path)
	sizeWithTear := fi.Size()

	ro, err := OpenReadOnly(path, JSONLCodec{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ro.Count() != 1 {
		t.Fatalf("count=%d", ro.Count())
	}
	if !ro.ReadOnly() {
		t.Fatal("ReadOnly() should be true")
	}
	if _, err := ro.Append([]byte(`{"b":2}`)); err != ErrReadOnly {
		t.Fatalf("want ErrReadOnly on Append, got %v", err)
	}
	// Tear should still be on disk.
	fi2, _ := os.Stat(path)
	if fi2.Size() != sizeWithTear {
		t.Fatalf("read-only open truncated file: %d != %d", fi2.Size(), sizeWithTear)
	}
}

func TestAppendUsesGlobalIndexInJSONL(t *testing.T) {
	// When baseIndex is non-zero, the JSONL envelope should record the
	// global log index, not the segment-local count.
	path := filepath.Join(t.TempDir(), "00000000000000000100.jsonl")
	s, _ := Create(path, JSONLCodec{}, 100, 0)
	defer s.Close()
	if _, err := s.Append([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`"_idx":100`, `"_idx":101`} {
		if !strings.Contains(got, want) {
			t.Fatalf("file missing %q:\n%s", want, got)
		}
	}
}
