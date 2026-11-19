package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
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

func grantedOutfitsRig(t *testing.T) lineageRig {
	t.Helper()
	return grantedOutfitsRigIn(t, "")
}

func grantedOutfitsRigIn(t *testing.T, boxExtra string) lineageRig {
	t.Helper()
	r := newLineageRig(t, boxExtra)
	for _, name := range []string{"hk", "model-a", "secret"} {
		if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", name+".toml"), []byte("[notes]\nwore = \""+name+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r lineageRig) forkDressed(t *testing.T, figaro string, outfits ...string) (string, error) {
	t.Helper()
	out, err := r.h[rpc.MethodFork](localOwner(), claimed(t, figaro, rawJSON(t, rpc.ForkRequest{FigaroID: figaro, Outfits: outfits})))
	if err != nil {
		return "", err
	}
	return out.(rpc.ForkResponse).Alternative, nil
}

func refusedFor(t *testing.T, err error, figaro, outfit, grants string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s dressed in %s ungranted", figaro, outfit)
	}
	want := "outfit " + outfit + " is not granted to " + figaro
	fix := "figaro set --id " + figaro + " " + form.OutfitGrantsKey + " '" + grants + "'"
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), fix) {
		t.Fatalf("refusal %q\nwant it to say %q and %q", err, want, fix)
	}
}

// Defends #171: an outfit's owner-held keys land past -S, so a figaro names
// only default and what the owner granted it.
func TestAFigaroForksOnlyInDefaultAndTheOutfitsItIsGranted(t *testing.T) {
	r := grantedOutfitsRig(t)
	if _, err := r.forkDressed(t, r.child); err != nil {
		t.Fatalf("control: a fork naming no outfit: %v", err)
	}
	if _, err := r.forkDressed(t, r.child, "default"); err != nil {
		t.Fatalf("default was refused: %v", err)
	}
	_, err := r.forkDressed(t, r.child, "hk")
	refusedFor(t, err, r.child, "hk", `["hk"]`)

	r.ownerSets(t, r.child, map[string]any{form.OutfitGrantsKey: []string{"hk", "model-*"}})
	for _, layers := range [][]string{{"hk"}, {"default", "hk"}, {"model-a"}, {"hk", "model-a"}} {
		if _, err := r.forkDressed(t, r.child, layers...); err != nil {
			t.Errorf("granted %v refused: %v", layers, err)
		}
	}
	_, err = r.forkDressed(t, r.child, "hk", "secret")
	refusedFor(t, err, r.child, "secret", `["hk","model-*","secret"]`)
	_, err = r.forkDressed(t, r.child, "secret", "hk")
	refusedFor(t, err, r.child, "secret", `["hk","model-*","secret"]`)

	if _, err := r.h[rpc.MethodFork](localOwner(), rawJSON(t, rpc.ForkRequest{FigaroID: r.child, Outfits: []string{"secret"}})); err != nil {
		t.Fatalf("the owner at its terminal was refused an outfit: %v", err)
	}
}

func TestAForkIsBornWithItsParentsOwnOutfitGrants(t *testing.T) {
	r := grantedOutfitsRig(t)
	r.ownerSets(t, r.child, map[string]any{form.OutfitGrantsKey: []string{"hk"}})
	kid, err := r.forkDressed(t, r.child)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := keyOn(t, r.grantRig, kid, form.OutfitGrantsKey); string(v) != `["hk"]` {
		t.Fatalf("the fork's grants: %s", v)
	}
}

func TestNewBindAndFormCreateAreJudgedAsAForkIs(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", "hk.toml"), []byte("[notes]\nwore = \"hk\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.x, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.FanoutMintKey: true})})); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"new": func() error {
			_, err := r.h[rpc.MethodCreate](localOwner(), claimed(t, r.x, rawJSON(t, rpc.CreateRequest{Outfits: []string{"mock", "hk"}, Cwd: r.cwd})))
			return err
		},
		"bind": func() error {
			_, err := r.h[rpc.MethodFormBind](localOwner(), claimed(t, r.x, rawJSON(t, rpc.FormBindRequest{Parent: "null", Outfits: []string{"mock", "hk"}, Cwd: r.cwd})))
			return err
		},
		"form create": func() error {
			_, err := r.h[rpc.MethodFormCreate](localOwner(), claimed(t, r.x, rawJSON(t, rpc.FormCreateRequest{Outfits: []string{"hk"}})))
			return err
		},
	}
	for _, call := range calls {
		refusedFor(t, call(), r.x, "hk", `["mock","hk"]`)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.x, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.OutfitGrantsKey: []string{"mock", "hk"}})})); err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Errorf("%s in a granted outfit: %v", name, err)
		}
	}
}

func TestSetSendAndCastNamingAnOutfitAreJudgedToo(t *testing.T) {
	r := grantedOutfitsRig(t)
	mantra := *heldPatch(t, map[string]any{"mantra": "dressed"})
	set := func() error {
		_, err := r.h[rpc.MethodSet](localOwner(), claimed(t, r.child, withAria(t, r.child, rpc.SetRequest{Patch: mantra, Outfits: []string{"hk"}})))
		return err
	}
	cast := func() error {
		role := *heldPatch(t, map[string]any{"name": "dressed-role"})
		_, err := r.h[rpc.MethodCast](localOwner(), claimed(t, r.child, withAria(t, r.child, rpc.CastRequest{Outfits: []string{"hk"}, RolePatch: &role})))
		return err
	}
	refusedFor(t, set(), r.child, "hk", `["hk"]`)
	refusedFor(t, cast(), r.child, "hk", `["hk"]`)
	_, err := r.h[rpc.MethodQua](localOwner(), claimed(t, r.child, withAria(t, r.child, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Outfits: []string{"hk"}}})))
	refusedFor(t, err, r.child, "hk", `["hk"]`)
	if v, _ := keyOn(t, r.grantRig, r.child, "notes.wore"); v != nil {
		t.Fatalf("the refused outfit landed: %s", v)
	}
	r.ownerSets(t, r.child, map[string]any{form.OutfitGrantsKey: []string{"hk"}})
	if err := set(); err != nil {
		t.Fatalf("set in a granted outfit: %v", err)
	}
	if v, _ := keyOn(t, r.grantRig, r.child, "notes.wore"); string(v) != `"hk"` {
		t.Fatalf("the granted outfit did not land: %s", v)
	}
	if err := cast(); err != nil {
		t.Fatalf("cast in a granted outfit: %v", err)
	}
}

// Defends the role union: the charter grants a seat's outfits on the role
// form, and its holder's own form carries none.
func TestARoleGrantsItsOutfitsToWhoeverHoldsIt(t *testing.T) {
	r := grantedOutfitsRig(t)
	target, _ := json.Marshal(r.child)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"name":               json.RawMessage(`"seat"`),
		"target-aria":        target,
		form.OutfitGrantsKey: json.RawMessage(`["secret"]`),
	}, nil)
	out, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: &patch})
	if err != nil {
		t.Fatal(err)
	}
	role := out.(rpc.FormCreateResponse).FormID
	if _, err := r.forkDressed(t, r.child, "secret"); err != nil {
		t.Fatalf("the holder was refused its role's outfit: %v", err)
	}
	_, err = r.forkDressed(t, r.sibling, "secret")
	refusedFor(t, err, r.sibling, "secret", `["secret"]`)

	moved, _ := json.Marshal(r.sibling)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, role, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": moved}, nil)})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.forkDressed(t, r.sibling, "secret"); err != nil {
		t.Fatalf("the new holder was refused the role's outfit: %v", err)
	}
	_, err = r.forkDressed(t, r.child, "secret")
	refusedFor(t, err, r.child, "secret", `["secret"]`)
}

// Defends H1 of the #171 review: a box with no outfits list dresses in
// default only, so a tool shell that drops its claim names nothing more.
func TestABoxWithNoOutfitsListDressesInDefaultOnly(t *testing.T) {
	r := grantedOutfitsRig(t)
	r.ownerSets(t, r.child, map[string]any{form.OutfitGrantsKey: []string{"secret"}})
	c := r.dial(t)
	fork := func(claim bool, outfits ...string) error {
		raw := withAria(t, r.child, rpc.ForkRequest{FigaroID: r.child, Outfits: outfits})
		if claim {
			raw = claimed(t, r.child, raw)
		}
		return c.Call(context.Background(), rpc.MethodFork, raw, nil)
	}
	for _, claim := range []bool{false, true} {
		if err := fork(claim, "secret"); err == nil || !strings.Contains(err.Error(), "lists no outfits") {
			t.Errorf("claim %v: a box with no list forked in secret: %v", claim, err)
		}
		if err := fork(claim, "default"); err != nil {
			t.Errorf("claim %v: control: default refused: %v", claim, err)
		}
		if err := fork(claim); err != nil {
			t.Errorf("claim %v: control: no outfit refused: %v", claim, err)
		}
	}
}

func TestABoxsOutfitsListIsItsGrantWhenItClaimsNoFigaro(t *testing.T) {
	r := grantedOutfitsRigIn(t, "outfits = [\"secret\"]\n")
	c := r.dial(t)
	if err := boxOnAria(t, c, rpc.MethodFork, r.child, rpc.ForkRequest{FigaroID: r.child, Outfits: []string{"secret"}}, nil); err != nil {
		t.Fatalf("an unclaimed fork in the box's listed outfit: %v", err)
	}
	if err := boxOnAria(t, c, rpc.MethodFork, r.child, rpc.ForkRequest{FigaroID: r.child, Outfits: []string{"hk"}}, nil); err == nil {
		t.Fatal("an unclaimed fork in an outfit the box does not list")
	}
}

// Defends review L2 on #171: a CLI from before it drops system.grants with
// every system.scopes write. The owner's would wipe outfit grants, and an
// ancestor's would be refused as an owner-held removal.
func TestAnOldCLIsScopesWriteKeepsTheOutfitGrants(t *testing.T) {
	r := grantedOutfitsRig(t)
	c := r.dial(t)
	oldCLI := func(scopes string) form.Patch {
		return form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: json.RawMessage(scopes)}, []string{form.GrantsKey})
	}
	r.ownerSets(t, r.child, map[string]any{form.OutfitGrantsKey: []string{"hk"}})
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: oldCLI(`["read:*","send:*"]`)})); err != nil {
		t.Fatalf("the owner's old-CLI scopes write: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, oldCLI(`["read:*"]`)); err != nil {
		t.Fatalf("an ancestor's old-CLI scopes write: %v", err)
	}
	if got := grantsOn(t, r, r.child); got != `["read:*"]` {
		t.Fatalf("scopes = %s", got)
	}
	if v, _ := keyOn(t, r.grantRig, r.child, form.OutfitGrantsKey); string(v) != `["hk"]` {
		t.Fatalf("the outfit grants went with the scopes write: %s", v)
	}
	drop := form.Build(form.Snapshot{}, nil, []string{form.GrantsKey})
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: drop})); err != nil {
		t.Fatal(err)
	}
	if v, ok := keyOn(t, r.grantRig, r.child, form.OutfitGrantsKey); ok {
		t.Fatalf("control: the owner's own removal of %s left %s", form.GrantsKey, v)
	}
}
