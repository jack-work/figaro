package wire_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
		l, err := b.CreateOutfit("d", patch("system.model", "m"))
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
		snap, err := b.FormState(conv)
		if err != nil {
			t.Fatalf("form: %v", err)
		}
		if v, ok := snap.Get("system.model"); !ok || string(v) != `"m"` {
			t.Errorf("conversation lost its outfit form: %s ok=%v", v, ok)
		}
	})
}

// Without the capability there is no presentation hierarchy to edit, and
// the store says so rather than pretending to succeed.
func TestPromoteOnlyWithTheCapability(t *testing.T) {
	eachCapability(t, func(t *testing.T, b *store.XwalBackend, trunks bool) {
		l, _ := b.CreateOutfit("d", patch("system.model", "m"))
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
	l, _ := b.CreateOutfit("d", patch("system.model", "m"))
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
	if err := b.Store().Tree().Promote("anything"); !errors.Is(err, topo.ErrNoPromote) {
		t.Errorf("trunkless promote = %v, want ErrNoPromote", err)
	}
	if err := b.Store().Tree().Reparent("a", "b"); err != nil {
		t.Errorf("trunkless reparent must be a no-op, got %v", err)
	}
	if b.Store().Tree().Edges() != nil {
		t.Error("the trunkless tree must carry no presentation edges")
	}
}

// Promotion is what opens a boundary, so it must be visible as a loss of
// normalization -- that flag is what a delete consults before unlinking.
func TestPromotionBreaksNormalization(t *testing.T) {
	b, _ := backend(t, true)
	l, _ := b.CreateOutfit("d", patch("system.model", "m"))
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
// survivor still reads through. The survivor absorbs that prefix first,
// then the delete proceeds and it is unharmed.
func TestDeleteRepairsTheBoundary(t *testing.T) {
	b, _ := backend(t, true)
	l, _ := b.CreateOutfit("d", patch("system.model", "m"))
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
	// The boundary is repaired first: alt absorbs the prefix it borrows,
	// then conv's subtree goes. alt must survive with its history intact.
	before, err := b.FormState(alt)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Store().RemoveLeaf(conv, true); err != nil {
		t.Fatalf("delete after boundary repair: %v", err)
	}
	after, err := b.FormState(alt)
	if err != nil {
		t.Fatalf("the promoted aria did not survive the delete: %v", err)
	}
	bv, _ := before.Get("system.model")
	av, _ := after.Get("system.model")
	if string(av) != string(bv) || string(av) == "" {
		t.Fatalf("survivor lost inherited state: %s -> %s", bv, av)
	}
}

// Normalize is the deferred work made immediate: after it, no delete can
// owe a boundary repair, whatever the presentation hierarchy says.
func TestNormalizeMakesDeletesFree(t *testing.T) {
	b, _ := backend(t, true)
	l, _ := b.CreateOutfit("d", patch("system.model", "m"))
	conv, _ := b.CreateConversation(l)
	_, alt, err := b.Fork(conv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store().Promote(alt, 1); err != nil {
		t.Fatal(err)
	}
	if b.Store().Tree().Normalized() {
		t.Fatal("a promoted forest must not be normalized")
	}
	n, err := b.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if n == 0 {
		t.Fatal("normalize absorbed nothing, but an aria was promoted")
	}
	// The survivor now owns its history, so the delete owes no repair.
	before, _ := b.FormState(alt)
	if err := b.Store().RemoveLeaf(conv, true); err != nil {
		t.Fatalf("delete after normalize: %v", err)
	}
	after, err := b.FormState(alt)
	if err != nil {
		t.Fatalf("survivor broken by the delete: %v", err)
	}
	bv, _ := before.Get("system.model")
	av, _ := after.Get("system.model")
	if string(av) != string(bv) {
		t.Fatalf("survivor state changed: %s -> %s", bv, av)
	}
}

// A trunkless figaro is normalized by construction, so normalize is a
// no-op rather than an error.
func TestNormalizeIsANoOpWithoutTheCapability(t *testing.T) {
	b, _ := backend(t, false)
	n, err := b.Normalize()
	if err != nil {
		t.Fatalf("normalize without the capability: %v", err)
	}
	if n != 0 {
		t.Fatalf("normalize absorbed %d on a trunkless figaro", n)
	}
}

// Normalize must be idempotent IN EFFECT: once an aria has absorbed its
// history it can never be orphaned again, so the operation that made that
// true has to report itself done. Otherwise every run re-absorbs every
// promoted aria (O(bytes) each) and the "boundary is empty" invariant that
// delete relies on never becomes true.
func TestNormalizeIsIdempotent(t *testing.T) {
	b, _ := backend(t, true)
	l, _ := b.CreateOutfit("d", patch("system.model", "m"))
	conv, _ := b.CreateConversation(l)
	_, alt, err := b.Fork(conv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store().Promote(alt, 1); err != nil {
		t.Fatal(err)
	}
	first, err := b.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if first == 0 {
		t.Fatal("normalize absorbed nothing, but an aria was promoted")
	}
	if !b.Store().Tree().Normalized() {
		t.Error("after normalize the tree must report itself normalized")
	}
	second, err := b.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Fatalf("second normalize absorbed %d again; it is not idempotent", second)
	}
}
