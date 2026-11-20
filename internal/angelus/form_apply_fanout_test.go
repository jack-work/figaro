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
	"github.com/jack-work/figaro/internal/authz"
)

func applyFanout(t *testing.T, r grantRig, id, outfit string, managed []string, claim string) error {
	t.Helper()
	return applyFanoutAs(t, r, asOwner(), id, outfit, managed, claim)
}

func applyFanoutAs(t *testing.T, r grantRig, ctx context.Context, id, outfit string, managed []string, claim string) error {
	t.Helper()
	call := func(req rpc.FormApplyRequest) (any, error) {
		raw, _ := json.Marshal(req)
		if claim != "" {
			raw = claimed(t, claim, raw)
		}
		return r.h[rpc.MethodFormApply](ctx, raw)
	}
	out, err := call(rpc.FormApplyRequest{FigaroID: id, Outfits: []string{outfit}, Managed: managed, Check: true})
	if err != nil {
		return err
	}
	checked := out.(rpc.FormApplyResponse)
	_, err = call(rpc.FormApplyRequest{FigaroID: id, Outfits: []string{outfit}, Managed: managed, IfVersion: checked.Version, ExpectedOutfit: checked.Outfit})
	return err
}

func fanoutOutfits(t *testing.T, r grantRig) {
	t.Helper()
	for name, body := range map[string]string{
		"fanout":    "[system.fanout]\nmax_children = 3\n[system]\nprovider = \"wrong\"\n",
		"no-fanout": "[charter]\npurpose = \"x\"\n",
	} {
		if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheOperatorAppliesAndRemovesARoleFanoutLimit(t *testing.T) {
	r, id := managedRole(t)
	fanoutOutfits(t, r)
	managed := []string{form.FanoutMaxChildrenKey}
	if err := applyFanout(t, r, id, "fanout", managed, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := keyOn(t, r, id, form.FanoutMaxChildrenKey); string(got) != "3" {
		t.Fatalf("max_children = %s, want 3", got)
	}
	if keyOnForm(t, r, id, "system.provider") == "wrong" {
		t.Fatal("unmanaged system state followed the outfit")
	}
	if err := applyFanout(t, r, id, "no-fanout", managed, ""); err != nil {
		t.Fatal(err)
	}
	if got, ok := keyOn(t, r, id, form.FanoutMaxChildrenKey); ok {
		t.Fatalf("max_children survived its removal from the manifest: %s", got)
	}
}

func TestAFigaroCannotApplyARoleFanoutLimit(t *testing.T) {
	r, id := managedRole(t)
	fanoutOutfits(t, r)
	err := applyFanout(t, r, id, "fanout", []string{form.FanoutMaxChildrenKey}, keyOnForm(t, r, id, "target-aria"))
	if err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("a figaro applied system.fanout: %v", err)
	}
	if got, ok := keyOn(t, r, id, form.FanoutMaxChildrenKey); ok {
		t.Fatalf("max_children landed: %s", got)
	}
}

func TestTheOperatorStillCannotApplyOtherSystemKeys(t *testing.T) {
	r, id := managedRole(t)
	fanoutOutfits(t, r)
	for _, managed := range [][]string{{"system"}, {"system.provider"}, {"system.sandbox"}} {
		if err := applyFanout(t, r, id, "fanout", managed, ""); err == nil {
			t.Fatalf("the operator applied %v", managed)
		}
	}
}

const setterPolicy = `
[roles.setter]
permissions = ["*:*"]

[[bind]]
role = "setter"
principals = ["user:setter", "sandbox:judge@local:box"]
`

func TestOnlyTheOwnerItselfGetsTheOperatorPaths(t *testing.T) {
	managed := []string{form.FanoutMaxChildrenKey}
	local := func(ctx context.Context) context.Context { return authz.WithDoor(ctx, authz.DoorLocal) }
	for name, c := range map[string]struct {
		ctx   context.Context
		claim string
	}{
		"a claim the daemon does not hold, dropped at a door":      {local(asOwner()), "0123456789abcdef0123456789abcdef"},
		"a principal granted everything on the trusted local door": {local(as("user", "setter")), ""},
		"a box": {authz.WithPrincipal(context.Background(), rpc.Principal{Kind: "sandbox", ID: "judge@" + authz.BoxScope}), ""},
	} {
		r := newRigConfigured(t, doorPolicy+setterPolicy, "[sandbox.boxes.judge]\nexec = \"/judge.sock\"\n"+judgeListsAnyOutfit)
		id := managedRoleBy(t, r, asOwner())
		fanoutOutfits(t, r)
		if err := applyFanoutAs(t, r, c.ctx, id, "fanout", managed, c.claim); err == nil || !strings.Contains(err.Error(), "owner-only") {
			t.Errorf("%s: want the operator paths refused as owner-only, got %v", name, err)
		}
		if got, ok := keyOn(t, r, id, form.FanoutMaxChildrenKey); ok {
			t.Errorf("%s: max_children landed: %s", name, got)
		}
	}
	r, id := managedRole(t)
	fanoutOutfits(t, r)
	if err := applyFanoutAs(t, r, local(asOwner()), id, "fanout", managed, ""); err != nil {
		t.Fatalf("control: the owner at the local door: %v", err)
	}
}
