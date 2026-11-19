package angelus

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

// Defends the scan while the role index seeds: a daemon just started would
// otherwise refuse a holder the outfits its role grants.
func TestAHolderKeepsItsRolesOutfitsBeforeTheRoleIndexIsSeeded(t *testing.T) {
	dir := t.TempDir()
	b, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	holder, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{form.OutfitGrantsKey: json.RawMessage(`["hk"]`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := json.Marshal(holder)
	if _, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": target, form.OutfitGrantsKey: json.RawMessage(`["secret"]`)}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	a := &Angelus{Backend: reopened}
	if _, ready := a.roles(); ready {
		t.Fatal("the reopened index is seeded; the test needs it unseeded")
	}
	own, all, err := a.outfitGrants(holder)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(own, []string{"hk"}) || !slices.Contains(all, "secret") || !slices.Contains(all, "hk") {
		t.Fatalf("own %v, all %v", own, all)
	}
}
