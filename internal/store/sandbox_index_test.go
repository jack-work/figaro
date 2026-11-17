package store

import (
	"testing"
)

func formResident(b *XwalBackend, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.forms[id] != nil
}

// The sandbox a list's authz asks about per row is answered without re-opening
// a form the idle sweep dropped (#130), and still follows every write.
func TestHeldSandboxOutlivesEvictionAndFollowsCommits(t *testing.T) {
	b, aria, _ := healFixture(t, t.TempDir(), 1)

	if box, ok := b.HeldSandbox(aria); !ok || box != "" {
		t.Fatalf("unboxed aria: got %q, %v", box, ok)
	}
	if _, err := b.ApplyFormPrivileged(aria, patchSet(map[string]string{"system.sandbox": "cell"})); err != nil {
		t.Fatal(err)
	}
	if box, ok := b.HeldSandbox(aria); !ok || box != "cell" {
		t.Fatalf("after the write: got %q, %v", box, ok)
	}

	b.dropForm(aria) // what the idle sweep does
	if box, ok := b.HeldSandbox(aria); !ok || box != "cell" {
		t.Fatalf("after eviction: got %q, %v", box, ok)
	}
	if formResident(b, aria) {
		t.Fatal("HeldSandbox re-opened an evicted form")
	}

	if _, err := b.ApplyFormPrivileged(aria, patchSet(map[string]string{"system.sandbox": ""})); err != nil {
		t.Fatal(err)
	}
	if box, _ := b.HeldSandbox(aria); box != "" {
		t.Fatalf("a write after eviction was not followed: got %q", box)
	}

	b.bury([]string{aria})
	if _, ok := b.sandboxes.get(aria); ok {
		t.Fatal("a deleted node kept its sandbox entry")
	}
}

// A fill that read before a commit must not overwrite what the commit noted.
func TestSandboxIndexKeepsTheNewerVersion(t *testing.T) {
	var x sandboxIndex
	x.note("a", "new", 7)
	x.note("a", "stale", 6)
	if box, _ := x.get("a"); box != "new" {
		t.Fatalf("got %q", box)
	}
	x.note("a", "newer", 8)
	if box, _ := x.get("a"); box != "newer" {
		t.Fatalf("got %q", box)
	}
}

func TestHeldSandboxUnreadableIsNotCached(t *testing.T) {
	b, _, _ := healFixture(t, t.TempDir(), 1)
	if _, ok := b.HeldSandbox("0123456789abcdef0123456789abcdef"); ok {
		t.Fatal("an unknown node read as readable")
	}
	if _, ok := b.sandboxes.get("0123456789abcdef0123456789abcdef"); ok {
		t.Fatal("an unreadable node was cached")
	}
}
