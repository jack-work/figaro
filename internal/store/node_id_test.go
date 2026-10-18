package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The node id is minted once per store and survives every reopen; it is the
// name other nodes will know this store by.
func TestNodeIDIsMintedOnceAndKept(t *testing.T) {
	root := t.TempDir()
	s, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := s.NodeID()
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("node id %q is not a uuid: %v", id, err)
	}
	s.Close()

	s, err = OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.NodeID() != id {
		t.Fatalf("reopen minted %q, want %q", s.NodeID(), id)
	}
}

// A store written before node ids gains one on first open and nothing else
// about it changes.
func TestAnExistingStoreGainsANodeID(t *testing.T) {
	root := t.TempDir()
	s, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Remove(filepath.Join(root, nodeIDFileName)); err != nil {
		t.Fatal(err)
	}
	s, err = OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := uuid.Parse(s.NodeID()); err != nil {
		t.Fatalf("node id %q: %v", s.NodeID(), err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, nodeIDFileName+".*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestACorruptNodeIDRefusesToOpen(t *testing.T) {
	root := t.TempDir()
	s, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.WriteFile(filepath.Join(root, nodeIDFileName), []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenXwalStore(root, 0); err == nil || !strings.Contains(err.Error(), "node id") {
		t.Fatalf("err = %v, want a refusal naming the node id", err)
	}
}
