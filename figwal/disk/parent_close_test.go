package disk

import (
	"os"
	"testing"
)

func TestAutoOpenedParentClosesWithChild(t *testing.T) {
	dir := t.TempDir()
	parent, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Write(1, []byte("one")); err != nil {
		t.Fatal(err)
	}
	child, err := parent.Fork(2, "child")
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove fork tree after close: %v", err)
	}
}
