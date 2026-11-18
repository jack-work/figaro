package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func managedRole(t *testing.T) (grantRig, string) {
	t.Helper()
	return managedRoleUnder(t, doorPolicy)
}

func managedRoleUnder(t *testing.T, policy string) (grantRig, string) {
	t.Helper()
	r := newRigWith(t, policy)
	if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", "role.toml"), []byte("[charter]\npurpose = \"new\"\n[system]\nprovider = \"ignored\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	target, _ := json.Marshal(holder.FigaroID)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"name":            json.RawMessage(`"managed-role"`),
		"target-aria":     target,
		"charter.purpose": json.RawMessage(`"old"`),
		"charter.note":    json.RawMessage(`"keep"`),
		"handoff":         json.RawMessage(`"keep.md"`),
	}, nil)
	out, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: &patch})
	if err != nil {
		t.Fatal(err)
	}
	return r, out.(rpc.FormCreateResponse).FormID
}

func checkManagedRole(t *testing.T, r grantRig, id string) rpc.FormApplyResponse {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodFormApply, rpc.FormApplyRequest{
		FigaroID: id, Outfits: []string{"role"}, Managed: []string{"charter.purpose"}, Check: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.FormApplyResponse)
}

func TestManagedRoleCheckApplyAndRepeat(t *testing.T) {
	r, id := managedRole(t)
	checked := checkManagedRole(t, r, id)
	if checked.Outcome != "would-apply" || keyOnForm(t, r, id, "charter.purpose") != "old" {
		t.Fatal("check wrote state or did not report its planned change")
	}
	req := rpc.FormApplyRequest{FigaroID: id, Outfits: []string{"role"}, Managed: []string{"charter.purpose"}, IfVersion: checked.Version, ExpectedOutfit: checked.Outfit}
	out, err := r.call(asOwner(), rpc.MethodFormApply, req)
	if err != nil {
		t.Fatal(err)
	}
	applied := out.(rpc.FormApplyResponse)
	if applied.Outcome != rpc.OutcomeApplied || applied.Version <= checked.Version {
		t.Fatalf("wrong apply result: %+v", applied)
	}
	for key, want := range map[string]string{"charter.purpose": "new", "charter.note": "keep", "handoff": "keep.md"} {
		if got := keyOnForm(t, r, id, key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if keyOnForm(t, r, id, "target-aria") == "" || keyOnForm(t, r, id, "system.provider") != "" {
		t.Fatal("occupancy or unmanaged inference state changed")
	}
	req.IfVersion = applied.Version
	out, err = r.call(asOwner(), rpc.MethodFormApply, req)
	if err != nil {
		t.Fatal(err)
	}
	if repeated := out.(rpc.FormApplyResponse); repeated.Outcome != rpc.OutcomeUnchanged || repeated.Version != applied.Version {
		t.Fatalf("repeat changed the durable version: %+v", repeated)
	}
}

func TestManagedRoleRefusesWrongIdentityOutfitAndCaller(t *testing.T) {
	r, id := managedRole(t)
	checked := checkManagedRole(t, r, id)
	base := rpc.FormApplyRequest{FigaroID: id, Outfits: []string{"role"}, Managed: []string{"charter.purpose"}, IfVersion: checked.Version, ExpectedOutfit: checked.Outfit}
	for _, bad := range []rpc.FormApplyRequest{
		{FigaroID: id, Outfits: base.Outfits, Managed: base.Managed},
		{FigaroID: id, Outfits: base.Outfits, Managed: base.Managed, IfVersion: checked.Version, ExpectedOutfit: "wrong"},
		{FigaroID: "@managed-role", Outfits: base.Outfits, Managed: base.Managed, Check: true},
		{FigaroID: id[:8], Outfits: base.Outfits, Managed: base.Managed, Check: true},
		{FigaroID: id, Outfits: base.Outfits, Managed: []string{"target-aria"}, Check: true},
	} {
		if _, err := r.call(asOwner(), rpc.MethodFormApply, bad); err == nil {
			t.Fatalf("accepted unsafe apply: %+v", bad)
		}
	}
	if _, err := r.call(as("user", "stranger"), rpc.MethodFormApply, base); err == nil {
		t.Fatal("a stranger applied the owner's outfit")
	}
	if keyOnForm(t, r, id, "charter.purpose") != "old" {
		t.Fatal("a refused request changed the role")
	}
}

func TestManagedRoleConcurrentCASHasOneWinner(t *testing.T) {
	r, id := managedRole(t)
	checked := checkManagedRole(t, r, id)
	req := rpc.FormApplyRequest{FigaroID: id, Outfits: []string{"role"}, Managed: []string{"charter.purpose"}, IfVersion: checked.Version, ExpectedOutfit: checked.Outfit}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.call(asOwner(), rpc.MethodFormApply, req)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	wins, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			wins++
		} else if strings.Contains(err.Error(), "version") || strings.Contains(err.Error(), "moved") {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("got %d winners and %d conflicts", wins, conflicts)
	}
}

func TestManagedRoleApplyRefusesARoleThatMovedSinceItsCheck(t *testing.T) {
	r, id := managedRole(t)
	checked := checkManagedRole(t, r, id)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"charter.note": json.RawMessage(`"moved"`)}, nil)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: patch})); err != nil {
		t.Fatal(err)
	}
	_, err := r.call(asOwner(), rpc.MethodFormApply, rpc.FormApplyRequest{FigaroID: id, Outfits: []string{"role"}, Managed: []string{"charter.purpose"}, IfVersion: checked.Version, ExpectedOutfit: checked.Outfit})
	if err == nil {
		t.Fatal("an apply against a stale check was written")
	}
	if keyOnForm(t, r, id, "charter.purpose") != "old" {
		t.Fatal("a refused apply changed the role")
	}
}

func TestManagedRoleRefusesDaemonHeldKeysAndTheRoleHandoffKey(t *testing.T) {
	r, id := managedRole(t)
	if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", "held.toml"), []byte("[succession-policy]\noutfit = \"anything\"\n[worker]\npool = \"p\"\n[charter]\nhandoff = \"other.md\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := form.Build(form.Snapshot{}, map[string]json.RawMessage{"succession-policy.handoff-key": json.RawMessage(`"charter.handoff"`)}, nil)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: policy})); err != nil {
		t.Fatal(err)
	}
	for _, managed := range [][]string{{"succession-policy"}, {"succession-policy.outfit"}, {"worker"}, {"worker.pool"}, {"charter"}, {"charter.handoff"}} {
		_, err := r.call(asOwner(), rpc.MethodFormApply, rpc.FormApplyRequest{FigaroID: id, Outfits: []string{"held"}, Managed: managed, Check: true})
		if err == nil || !strings.Contains(err.Error(), "holds") {
			t.Fatalf("managed %v was planned: %v", managed, err)
		}
	}
	if keyOnForm(t, r, id, "succession-policy.handoff-key") != "charter.handoff" {
		t.Fatal("the role's handoff key moved")
	}
}
