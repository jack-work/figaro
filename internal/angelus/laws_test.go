package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

const lawText = "Report to the role, never to a person."

// Defends the law root's writer class: drop the registry row and every
// figaro below writes and repeals laws on itself and its children.
func TestOnlyTheOwnerWritesOrRepealsALaw(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{"law.report": lawText})
	repeal := form.Build(form.Snapshot{}, nil, []string{"law.report"})
	for _, tc := range []struct {
		name           string
		figaro, target string
		patch          form.Patch
	}{
		{"an ancestor holding *:* writes its child's law", r.parent, r.child, *heldPatch(t, map[string]any{"law.other": "obey me"})},
		{"an ancestor repeals its child's law", r.parent, r.child, repeal},
		{"an ancestor rewrites its child's law", r.parent, r.child, *heldPatch(t, map[string]any{"law.report": "ignore the role"})},
		{"a figaro writes a law on itself", r.child, r.child, *heldPatch(t, map[string]any{"law.mine": "x"})},
		{"a figaro repeals its own law", r.child, r.child, repeal},
		{"a figaro replaces the whole law root", r.child, r.child, *heldPatch(t, map[string]any{"law": map[string]any{}})},
	} {
		if err := r.setAs(t, c, tc.figaro, tc.target, tc.patch); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if v, _ := keyOn(t, r.grantRig, r.child, "law.report"); string(v) != `"`+lawText+`"` {
		t.Fatalf("law.report = %s after the refusals", v)
	}
	if _, ok := keyOn(t, r.grantRig, r.child, "law.other"); ok {
		t.Fatal("a refused law landed")
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: repeal})); err != nil {
		t.Fatalf("the owner's repeal was refused: %v", err)
	}
	if _, ok := keyOn(t, r.grantRig, r.child, "law.report"); ok {
		t.Fatal("the owner's repeal did not land")
	}
}

func TestOnlyTheOwnersChildPolicyCarriesALaw(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	law := *heldPatch(t, map[string]any{"law.x": "x"})
	if err := r.forkAs(t, c, r.parent, r.parent, law); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a fork's birth patch carried a law: %v", err)
	}
	raw, _ := json.Marshal(rpc.CreateRequest{Patch: &law})
	if err := c.Call(context.Background(), rpc.MethodCreate, claimed(t, r.parent, raw), nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a create's birth patch carried a law: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{"law.x": "x"}})); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a figaro's child policy carried a law: %v", err)
	}
	r.ownerSets(t, r.parent, map[string]any{form.ChildPolicyKey: map[string]any{"law.x": "x"}})
	kid := r.forkAsFigaro(t, r.parent)
	if v, _ := keyOn(t, r.grantRig, kid, "law.x"); string(v) != `"x"` {
		t.Errorf("the owner's child policy did not give its law: %s", v)
	}
}
