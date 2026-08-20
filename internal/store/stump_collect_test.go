package store

import (
	"testing"
)

// An outfit stump is content-addressed, so it is minted afresh by the next aria
// that wants it. Collecting one when its last child dies is what keeps a store
// from accumulating a directory per outfit version forever.
func TestRemovingTheLastAriaCollectsItsStump(t *testing.T) {
	b := mustBackend(t)

	outfitID, err := b.CreateOutfit("demo", patchSet(nil))
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.CreateConversation(outfitID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.CreateConversation(outfitID)
	if err != nil {
		t.Fatal(err)
	}

	if got := stumpNames(b); len(got) != 1 {
		t.Fatalf("stumps = %v, want one", got)
	}

	// While a sibling survives, the stump must stay: it is the shared prefix
	// that sibling reads its history through.
	if err := b.Remove(first, false); err != nil {
		t.Fatal(err)
	}
	if got := stumpNames(b); len(got) != 1 {
		t.Fatalf("stump collected while %s still lives: %v", second, got)
	}

	if err := b.Remove(second, false); err != nil {
		t.Fatal(err)
	}
	if got := stumpNames(b); len(got) != 0 {
		t.Fatalf("stumps = %v, want none once the last aria is gone", got)
	}
}

// Collecting one stump must not disturb another.
func TestCollectingAStumpLeavesSiblingsIntact(t *testing.T) {
	b := mustBackend(t)

	keepID, err := b.CreateOutfit("keep", patchSet(nil))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := b.CreateConversation(keepID)
	if err != nil {
		t.Fatal(err)
	}
	dropID, err := b.CreateOutfit("drop", patchSet(nil))
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := b.CreateConversation(dropID)
	if err != nil {
		t.Fatal(err)
	}

	if err := b.Remove(doomed, false); err != nil {
		t.Fatal(err)
	}
	got := stumpNames(b)
	if len(got) != 1 {
		t.Fatalf("stumps = %v, want only keep's", got)
	}
	if _, err := b.OpenFigIR(kept); err != nil {
		t.Fatalf("survivor unreadable after a sibling stump was collected: %v", err)
	}
}

// The stump is content-addressed, so the next mint recreates it: collecting is
// not destructive to anything but disk.
func TestACollectedStumpIsRemintedByTheNextAria(t *testing.T) {
	b := mustBackend(t)

	first, err := b.CreateOutfit("demo", patchSet(nil))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(conv, false); err != nil {
		t.Fatal(err)
	}
	if got := stumpNames(b); len(got) != 0 {
		t.Fatalf("stumps = %v, want none", got)
	}

	again, err := b.CreateOutfit("demo", patchSet(nil))
	if err != nil {
		t.Fatalf("re-minting a collected stump: %v", err)
	}
	if again != first {
		t.Fatalf("re-mint = %q, want the same content-addressed id %q", again, first)
	}
	revived, err := b.CreateConversation(again)
	if err != nil {
		t.Fatalf("spawn under a re-minted stump: %v", err)
	}
	if _, err := b.OpenFigIR(revived); err != nil {
		t.Fatalf("aria under a re-minted stump: %v", err)
	}
}

func mustBackend(t *testing.T) *XwalBackend {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func stumpNames(b *XwalBackend) []string {
	var out []string
	for _, st := range b.store.listStumps() {
		out = append(out, st.Name)
	}
	return out
}
