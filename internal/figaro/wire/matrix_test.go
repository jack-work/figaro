package wire_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"

	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/topo"
)

func patch(k, v string) message.Patch {
	raw, _ := json.Marshal(v)
	return message.Patch{Set: map[string]json.RawMessage{k: raw}}
}

func backend(t *testing.T, trunks bool) (*store.XwalBackend, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "arias")
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: trunks}); err != nil {
		t.Fatal(err)
	}
	return b, root
}

// The matrix. Everything below runs with the capability on AND off, because
// "the dependency is optional" is only true if the off case is exercised.
func eachCapability(t *testing.T, fn func(t *testing.T, b *store.XwalBackend, trunks bool)) {
	t.Helper()
	for _, trunks := range []bool{false, true} {
		name := "trunkless"
		if trunks {
			name = "trunks"
		}
		t.Run(name, func(t *testing.T) {
			b, _ := backend(t, trunks)
			fn(t, b, trunks)
		})
	}
}

// Forking, reading and listing must not care whether the capability exists.
func TestAriasWorkEitherWay(t *testing.T) {
	eachCapability(t, func(t *testing.T, b *store.XwalBackend, trunks bool) {
		l, err := b.CreateLoadout("d", patch("system.model", "m"))
		if err != nil {
			t.Fatal(err)
		}
		conv, err := b.CreateConversation(l)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.Fork(conv); err != nil {
			t.Fatalf("fork: %v", err)
		}
		snap, err := b.ChalkboardState(conv)
		if err != nil {
			t.Fatalf("chalkboard: %v", err)
		}
		if v, ok := snap.Get("system.model"); !ok || string(v) != `"m"` {
			t.Errorf("conversation lost its loadout chalkboard: %s ok=%v", v, ok)
		}
	})
}

// Without the capability there is no presentation hierarchy to edit, and
// the store says so rather than pretending to succeed.
func TestPromoteOnlyWithTheCapability(t *testing.T) {
	eachCapability(t, func(t *testing.T, b *store.XwalBackend, trunks bool) {
		l, _ := b.CreateLoadout("d", patch("system.model", "m"))
		conv, _ := b.CreateConversation(l)
		_, alt, err := b.Fork(conv)
		if err != nil {
			t.Fatal(err)
		}
		_, err = b.Store().Promote(alt, 1)
		if trunks && err != nil {
			t.Fatalf("promote with the capability: %v", err)
		}
		if !trunks && err == nil {
			t.Fatal("promote succeeded without the trunk capability")
		}
	})
}

// A trunkless figaro writes NO presentation state, ever. This is the
// concrete form of "the capability is optional": not merely unused, absent.
func TestTrunklessWritesNoPstate(t *testing.T) {
	b, root := backend(t, false)
	l, _ := b.CreateLoadout("d", patch("system.model", "m"))
	conv, _ := b.CreateConversation(l)
	if _, _, err := b.Fork(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "trunks.json")); !os.IsNotExist(err) {
		t.Fatalf("a trunkless figaro wrote a trunk pstate (err=%v)", err)
	}
}

// A trunkless figaro is always normalized: the two hierarchies are one, so
// a delete can never orphan a survivor and no boundary repair can be owed.
func TestTrunklessIsAlwaysNormalized(t *testing.T) {
	b, _ := backend(t, false)
	if !b.Store().Tree().Normalized() {
		t.Fatal("a trunkless figaro must be normalized by construction")
	}
	if _, ok := b.Store().Tree().(interface{ Reparent(string, string) error }); ok {
		t.Error("the trunkless tree must not expose presentation edits")
	}
}

// Promotion is what opens a boundary, so it must be visible as a loss of
// normalization -- that flag is what a delete consults before unlinking.
func TestPromotionBreaksNormalization(t *testing.T) {
	b, _ := backend(t, true)
	l, _ := b.CreateLoadout("d", patch("system.model", "m"))
	conv, _ := b.CreateConversation(l)
	_, alt, err := b.Fork(conv)
	if err != nil {
		t.Fatal(err)
	}
	tree := b.Store().Tree()
	if !tree.Normalized() {
		t.Fatal("a fresh forest must be normalized")
	}
	if _, err := b.Store().Promote(alt, 1); err != nil {
		t.Fatal(err)
	}
	if tree.Normalized() {
		t.Fatal("a promoted forest is not normalized: a delete must repair its boundary")
	}
	var _ topo.Tree = tree
}

// A promoted forest can produce a delete that takes a directory some
// survivor still reads through. Until boundary repair exists, that delete
// must be refused, not performed.
func TestDeleteRefusesToOrphan(t *testing.T) {
	b, _ := backend(t, true)
	l, _ := b.CreateLoadout("d", patch("system.model", "m"))
	conv, _ := b.CreateConversation(l)
	_, alt, err := b.Fork(conv)
	if err != nil {
		t.Fatal(err)
	}
	// alt takes conv's place; conv comes to sit under alt. alt still reads
	// its history through conv, so deleting conv's subtree would strand it.
	if _, err := b.Store().Promote(alt, 1); err != nil {
		t.Fatal(err)
	}
	err = b.Store().RemoveLeaf(conv, true)
	if err == nil {
		t.Fatal("delete of a promoted parent succeeded; it orphans the promoted aria")
	}
	if !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("delete error = %v, want an orphan refusal", err)
	}
}
