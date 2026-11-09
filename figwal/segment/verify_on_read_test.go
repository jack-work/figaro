package segment

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A JSONL read checks the line's _hash, so a byte changed on disk after the
// segment was opened is refused rather than handed up as a payload that
// still parses. json.Valid used to be the only check after the open, and it
// passes any change that keeps the JSON well formed.
func TestAJSONLReadRefusesAByteChangedAfterTheOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00000000000000000001.jsonl")
	writeFrames(t, path, JSONLCodec{}, `{"a":"alpha"}`, `{"a":"bravo"}`, `{"a":"charlie"}`)
	s, err := Open(path, JSONLCodec{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	raw, _ := os.ReadFile(path)
	at := bytes.Index(raw, []byte("bravo"))
	raw[at] = 'B'
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.ReadIndex(1)
	if err == nil {
		t.Fatalf("read the changed record as %s", got)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v, want ErrCorrupt", err)
	}
	if got, err := s.ReadIndex(2); err != nil || string(got) != `{"a":"charlie"}` {
		t.Fatalf("an untouched record: %s %v", got, err)
	}
}
