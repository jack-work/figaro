package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func fanoutGrantPatch(t *testing.T, n int) form.Patch {
	t.Helper()
	return *heldPatch(t, map[string]any{form.FanoutMaxChildrenKey: n})
}

func maxChildrenOn(t *testing.T, r lineageRig, id string) (json.RawMessage, bool) {
	t.Helper()
	return keyOn(t, r.grantRig, id, form.FanoutMaxChildrenKey)
}

func TestAFigaroCannotRaiseItsOwnFanoutLimit(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: fanoutGrantPatch(t, 1)})); err != nil {
		t.Fatal(err)
	}
	err := r.setAs(t, c, r.parent, r.parent, fanoutGrantPatch(t, 2))
	if err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("the parent raised its own max_children: %v", err)
	}
	if v, _ := maxChildrenOn(t, r, r.parent); string(v) != "1" {
		t.Fatalf("max_children changed: %s", v)
	}
}

func TestAnAncestorGrantsAtMostItsOwnFanoutLimit(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: fanoutGrantPatch(t, 1)})); err != nil {
		t.Fatal(err)
	}
	err := r.setAs(t, c, r.parent, r.child, fanoutGrantPatch(t, 2))
	if err == nil || !strings.Contains(err.Error(), "grants at most its own max_children") {
		t.Fatalf("the parent granted beyond its own max_children: %v", err)
	}
	if v, ok := maxChildrenOn(t, r, r.child); ok {
		t.Fatalf("max_children landed as %s despite the refusal", v)
	}
	if err := r.setAs(t, c, r.parent, r.child, fanoutGrantPatch(t, 1)); err != nil {
		t.Fatalf("control: a grant at the parent's own limit: %v", err)
	}
	if v, _ := maxChildrenOn(t, r, r.child); string(v) != "1" {
		t.Fatalf("max_children %s, want 1", v)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.sibling, rpc.SetRequest{Patch: fanoutGrantPatch(t, 5)})); err != nil {
		t.Fatalf("control: the owner is exempt from the bound: %v", err)
	}
	if err := r.forkAs(t, c, r.parent, r.parent, fanoutGrantPatch(t, 2)); err == nil || !strings.Contains(err.Error(), "grants at most its own max_children") {
		t.Fatalf("a fork -S granted beyond its writer: %v", err)
	}
}
