package angelus_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func outfitGrantsPatch(t *testing.T, v []string) form.Patch {
	t.Helper()
	return *heldPatch(t, map[string]any{form.OutfitGrantsKey: v})
}

// Defends the owner-held row: as a lineage key the parent would hand its
// child outfits, and as a self key the figaro would grant itself any.
func TestOnlyTheOwnerWritesAFigarosOutfitGrants(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	for _, by := range []string{r.child, r.parent, r.sibling} {
		if err := r.setAs(t, c, by, r.child, outfitGrantsPatch(t, []string{"*"})); err == nil {
			t.Errorf("%s wrote the child's outfit grants", by)
		}
	}
	if err := r.forkAs(t, c, r.parent, r.child, outfitGrantsPatch(t, []string{"*"})); err == nil {
		t.Error("the parent forked the child with outfit grants")
	}
	if v, ok := keyOn(t, r.grantRig, r.child, form.OutfitGrantsKey); ok {
		t.Fatalf("outfit grants landed: %s", v)
	}
	r.ownerSets(t, r.child, map[string]any{form.OutfitGrantsKey: []string{"hk"}})
	if v, _ := keyOn(t, r.grantRig, r.child, form.OutfitGrantsKey); string(v) != `["hk"]` {
		t.Fatalf("the owner's grant: %s", v)
	}
	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{form.OutfitGrantsKey: nil})); err == nil {
		t.Error("the parent rewrote the owner's grant")
	}
}

func TestTheOperatorAppliesARolesOutfitGrantsAndAFigaroMayNot(t *testing.T) {
	r, id := managedRole(t)
	body := "[system.grants]\noutfits = [\"hk\", \"model-*\"]\n[system]\nmodel = \"wrong\"\n"
	if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", "role-outfits.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	managed := []string{form.OutfitGrantsKey}
	err := applyFanout(t, r, id, "role-outfits", managed, keyOnForm(t, r, id, "target-aria"))
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("the holder applied its own role's outfit grants: %v", err)
	}
	if v, ok := keyOn(t, r, id, form.OutfitGrantsKey); ok {
		t.Fatalf("outfit grants landed from a figaro: %s", v)
	}
	if err := applyFanout(t, r, id, "role-outfits", managed, ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := keyOn(t, r, id, form.OutfitGrantsKey); string(v) != `["hk","model-*"]` {
		t.Fatalf("outfit grants = %s", v)
	}
	if keyOnForm(t, r, id, "system.model") == "wrong" {
		t.Fatal("unmanaged system state followed the outfit")
	}
}
