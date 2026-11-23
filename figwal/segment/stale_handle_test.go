package segment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jsonPayload(n int) []byte {
	return []byte(`{"t":"` + strings.Repeat("x", n) + `"}`)
}

func scanClean(t *testing.T, path string) int {
	t.Helper()
	ro, err := OpenReadOnly(path, JSONLCodec{}, 1)
	if err != nil {
		t.Fatalf("segment no longer opens: %v", err)
	}
	defer ro.Close()
	raw, _ := os.ReadFile(path)
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		t.Fatalf("file does not end at a frame boundary")
	}
	return int(ro.Count())
}

func TestSecondHandleCannotOverwriteWhatTheFirstWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.jsonl")
	seed, err := Create(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Append(jsonPayload(10)); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	w1, err := Open(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()
	w2, err := Open(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	if _, err := w2.Append(jsonPayload(200)); err != nil {
		t.Fatal(err)
	}
	if _, err := w1.Append(jsonPayload(900)); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("stale handle appended over another writer's frame: %v", err)
	}
	if _, err := w2.Append(jsonPayload(30)); err != nil {
		t.Fatal(err)
	}
	if n := scanClean(t, path); n != 3 {
		t.Fatalf("frames=%d want 3", n)
	}
}

func TestAppendRefusesAfterForeignTailBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00001.jsonl")
	s, err := Create(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Append(jsonPayload(10)); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	f.WriteString(`{"_hash":"abc","_idx":2,"t":"cut`)
	f.Close()
	if _, err := s.Append(jsonPayload(10)); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("append landed after a torn tail: %v", err)
	}
}
