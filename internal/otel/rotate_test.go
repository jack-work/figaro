package otel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingWriter_RollsOverAtCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traces.jsonl")
	w, err := newRotatingWriter(path, 100) // tiny cap
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 40) + "\n") // 41 bytes
	for i := 0; i < 5; i++ {                        // 205 bytes total > cap, must roll
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	// The active file is bounded (<= cap + one line), and a ".1" backup exists.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active file: %v", err)
	}
	if fi.Size() > 100+int64(len(line)) {
		t.Fatalf("active file %d bytes exceeds cap+1line", fi.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rolled-over .1 backup: %v", err)
	}
}

func TestRotatingWriter_AppendsExistingSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.jsonl")
	if err := os.WriteFile(path, []byte("preexisting\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := newRotatingWriter(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if w.size != int64(len("preexisting\n")) {
		t.Fatalf("size = %d, want %d (should account for existing bytes)", w.size, len("preexisting\n"))
	}
	w.Close()
}
