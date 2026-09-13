package mark

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMarksAreOffWithoutTheEnv(t *testing.T) {
	t.Setenv(Env, "")
	Init("test")
	if Enabled() {
		t.Fatal("marks are on with no sink named")
	}
	Mark("frame", "bytes", 1)
}

func TestEveryMarkIsOneJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marks.jsonl")
	enabled.Store(false)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	out, proc, pid = f, "test", "1"
	enabled.Store(true)
	t.Cleanup(func() { enabled.Store(false); f.Close() })

	Mark("frame", "bytes", 12, "content", true)
	Span("hop", "to", "abc")("parts", 3)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("a mark is not one JSON object: %v", err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2", len(lines))
	}
	if lines[0]["m"] != "frame" || lines[0]["bytes"] != float64(12) || lines[0]["content"] != true {
		t.Fatalf("frame mark lost its fields: %v", lines[0])
	}
	if lines[1]["m"] != "hop" || lines[1]["to"] != "abc" || lines[1]["parts"] != float64(3) {
		t.Fatalf("span mark lost its fields: %v", lines[1])
	}
	if _, ok := lines[1]["ms"].(float64); !ok {
		t.Fatalf("span mark carries no ms: %v", lines[1])
	}
}
