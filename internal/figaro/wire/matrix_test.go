package wire_test

import (
	"encoding/json"
	"errors"
	"github.com/jack-work/figaro/api/form"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/message"

	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/topo"
)

func patch(k, v string) message.Patch {
	raw, _ := json.Marshal(v)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{k: raw}, nil)
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

// Everything below runs with the capability on and off.
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

// Without the capability there is no hierarchy to edit, and the store says so.
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

// A trunkless figaro writes no presentation state.
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

// A trunkless figaro is always normalized: a delete can never orphan a survivor.
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

// Promotion opens a boundary and must show as a loss of normalization.
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

// A survivor absorbs a prefix it reads through before the delete proceeds.
func TestDeleteRepairsTheBoundary(t *testing.T) {
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
	before, err := b.FormState(alt)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(conv, true); err != nil {
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

// After Normalize no delete can owe a boundary repair.
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
	before, _ := b.FormState(alt)
	if err := b.Remove(conv, true); err != nil {
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

// Normalize on a trunkless figaro is a no-op, not an error.
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

// Normalize must report itself done once an aria has absorbed its history,
// or every run re-absorbs every promoted aria.
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
