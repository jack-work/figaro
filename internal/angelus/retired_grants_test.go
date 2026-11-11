package angelus_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func plantOldGrants(t *testing.T, r lineageRig, id string, v []string) {
	t.Helper()
	raw, _ := json.Marshal(v)
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.backend.ApplyForm(id, form.Build(snap, map[string]json.RawMessage{form.RetiredGrantsKey: raw}, []string{form.ScopesKey})); err != nil {
		t.Fatal(err)
	}
}

func oldGrantsPatch(t *testing.T, v any) form.Patch {
	t.Helper()
	return *heldPatch(t, map[string]any{form.RetiredGrantsKey: v})
}

func movePatch(t *testing.T, v []string) form.Patch {
	t.Helper()
	raw, _ := json.Marshal(v)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: raw}, []string{form.RetiredGrantsKey})
}

func TestNoCallerWritesSystemGrants(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: oldGrantsPatch(t, []string{"read:*"})})); err == nil || !strings.Contains(err.Error(), form.ScopesKey) {
		t.Fatalf("the owner set system.grants: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, oldGrantsPatch(t, []string{"read:*"})); err == nil || !strings.Contains(err.Error(), form.ScopesKey) {
		t.Fatalf("an ancestor set system.grants: %v", err)
	}
	if err := r.forkAs(t, c, r.parent, r.child, oldGrantsPatch(t, []string{"read:*"})); err == nil || !strings.Contains(err.Error(), form.ScopesKey) {
		t.Fatalf("an ancestor forked with system.grants: %v", err)
	}
	if v, ok := keyOn(t, r.grantRig, r.child, form.RetiredGrantsKey); ok {
		t.Fatalf("system.grants landed: %s", v)
	}
}

func TestAWriteLeavingBothNamesIsRefused(t *testing.T) {
	r := newLineageRig(t, "")
	plantOldGrants(t, r, r.child, []string{"read:*"})
	_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:*"})}))
	if err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("a set of system.scopes beside system.grants: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: movePatch(t, []string{"read:*"})})); err != nil {
		t.Fatalf("control: setting scopes and dropping grants in one write: %v", err)
	}
	if got := grantsOn(t, r, r.child); got != `["read:*"]` {
		t.Fatalf("scopes %s", got)
	}
	if v, ok := keyOn(t, r.grantRig, r.child, form.RetiredGrantsKey); ok {
		t.Fatalf("system.grants survived the move: %s", v)
	}
}

func TestTheOldKeyIsProtectedAsTheNewOneIs(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	plantOldGrants(t, r, r.child, []string{"read:*", "set:*"})
	drop := form.Build(form.Snapshot{}, nil, []string{form.RetiredGrantsKey})
	if err := r.setAs(t, c, r.child, r.child, drop); err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("the child dropped its own system.grants: %v", err)
	}
	if err := r.setAs(t, c, r.sibling, r.child, drop); err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("a sibling dropped system.grants: %v", err)
	}
	if err := r.setAs(t, c, r.child, r.child, movePatch(t, []string{"*:*"})); err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("the child moved its own grants to wider scopes: %v", err)
	}
	if v, _ := keyOn(t, r.grantRig, r.child, form.RetiredGrantsKey); string(v) != `["read:*","set:*"]` {
		t.Fatalf("system.grants changed: %s", v)
	}
	if err := r.setAs(t, c, r.parent, r.child, movePatch(t, []string{"read:*", "set:*"})); err != nil {
		t.Fatalf("control: the parent moves the child's grants to scopes: %v", err)
	}
}

func TestTheSubsetRuleBindsAMoveToTheNewName(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	plantOldGrants(t, r, r.child, []string{"read:*"})
	if err := r.setAs(t, c, r.parent, r.child, movePatch(t, []string{"cred:y"})); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("the parent gave scopes it does not hold: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, movePatch(t, []string{"cred:x"})); err != nil {
		t.Fatalf("control: scopes the parent holds: %v", err)
	}
}

func TestAnExportSayingSystemGrantsImportsAsSystemScopes(t *testing.T) {
	r := newLineageRig(t, "")
	raw, _ := json.Marshal([]string{"read:*"})
	out, err := r.call(asOwner(), rpc.MethodImport, rpc.ImportRequest{
		Outfit: "imported-old",
		Form:   form.Build(form.Snapshot{}, map[string]json.RawMessage{form.RetiredGrantsKey: raw}, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(rpc.ImportResponse).FigaroID
	if got := grantsOn(t, r, id); got != `["read:*"]` {
		t.Fatalf("scopes %s", got)
	}
	if v, ok := keyOn(t, r.grantRig, id, form.RetiredGrantsKey); ok {
		t.Fatalf("system.grants kept: %s", v)
	}
}

func TestFigaroGrantsAnswersAsFigaroScopesDoes(t *testing.T) {
	r := newLineageRig(t, "")
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:*"})})); err != nil {
		t.Fatal(err)
	}
	newer, err := r.call(asOwner(), rpc.MethodScopes, rpc.ScopesRequest{FigaroID: r.child})
	if err != nil {
		t.Fatal(err)
	}
	older, err := r.call(asOwner(), rpc.MethodGrants, rpc.GrantsRequest{FigaroID: r.child})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(newer, older) {
		t.Fatalf("figaro.scopes %+v\nfigaro.grants %+v", newer, older)
	}
	chain := newer.(rpc.ScopesResponse).Chain
	last := chain[len(chain)-1]
	if strings.Join(last.Scopes, ",") != "read:*" || strings.Join(last.Grants, ",") != "read:*" {
		t.Fatalf("the level names its set under one name only: %+v", last)
	}
	raw, _ := json.Marshal(last)
	if !strings.Contains(string(raw), `"grants":["read:*"]`) || !strings.Contains(string(raw), `"scopes":["read:*"]`) {
		t.Fatalf("wire %s", raw)
	}
}
