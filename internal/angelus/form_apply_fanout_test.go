package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func applyFanout(t *testing.T, r grantRig, id, outfit string, managed []string, claim string) error {
	t.Helper()
	call := func(req rpc.FormApplyRequest) (any, error) {
		raw, _ := json.Marshal(req)
		if claim != "" {
			raw = claimed(t, claim, raw)
		}
		return r.h[rpc.MethodFormApply](asOwner(), raw)
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
	if err == nil || !strings.Contains(err.Error(), "runtime-owned") {
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
