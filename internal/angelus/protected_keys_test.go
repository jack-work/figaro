package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

const protectedOpen = "[authz]\nprotected_keys = \"open\"\n"

type lineageRig struct {
	doorRig
	parent, child, sibling string
}

func newLineageRig(t *testing.T, boxExtra string) lineageRig {
	t.Helper()
	d := boxDoorRigConfigured(t, defaultPolicy, "true", boxExtra)
	p, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	fork := func() string {
		out, err := d.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: p.FigaroID})
		if err != nil {
			t.Fatal(err)
		}
		return out.(rpc.ForkResponse).Alternative
	}
	r := lineageRig{doorRig: d, parent: p.FigaroID, child: fork(), sibling: fork()}
	if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:aria:*", "a", "x"})})); err != nil {
		t.Fatal(err)
	}
	return r
}

func claimed(t *testing.T, figaro string, raw json.RawMessage) json.RawMessage {
	t.Helper()
	out, err := rpc.WithCaller(raw, figaro, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func grantsPatch(t *testing.T, v any) form.Patch {
	t.Helper()
	return *heldPatch(t, map[string]any{form.GrantsKey: v})
}

func (r lineageRig) setAs(t *testing.T, c *jkrpc.Client, figaro, target string, p form.Patch) error {
	t.Helper()
	return c.Call(context.Background(), rpc.MethodSet, claimed(t, figaro, withAria(t, target, rpc.SetRequest{Patch: p})), nil)
}

func (r lineageRig) forkAs(t *testing.T, c *jkrpc.Client, figaro, target string, p form.Patch) error {
	t.Helper()
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: target, Patch: &p})
	return c.Call(context.Background(), rpc.MethodFork, claimed(t, figaro, raw), nil)
}

func grantsOn(t *testing.T, r lineageRig, id string) string {
	t.Helper()
	v, _ := keyOn(t, r.grantRig, id, form.GrantsKey)
	return string(v)
}

func TestAFigaroCannotSetItsOwnProtectedKeys(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	err := r.setAs(t, c, r.child, r.child, grantsPatch(t, []string{"read:aria:*"}))
	if err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("the child set its own grants: %v", err)
	}
	if got := grantsOn(t, r, r.child); got != "" {
		t.Fatalf("grants landed: %s", got)
	}
}

func TestTheParentSetsItsChildsProtectedKeysThroughTheBoxDoor(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	if err := r.setAs(t, c, r.parent, r.child, grantsPatch(t, []string{"read:aria:*"})); err != nil {
		t.Fatal(err)
	}
	if got := grantsOn(t, r, r.child); got != `["read:aria:*"]` {
		t.Fatalf("grants %s", got)
	}
}

func TestASiblingCannotSetProtectedKeys(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	err := r.setAs(t, c, r.sibling, r.child, grantsPatch(t, []string{"x"}))
	if err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("a sibling set grants: %v", err)
	}
	err = r.forkAs(t, c, r.sibling, r.child, grantsPatch(t, []string{"x"}))
	if err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("a sibling forked the child with grants: %v", err)
	}
	if err := r.forkAs(t, c, r.parent, r.child, grantsPatch(t, []string{"x"})); err != nil {
		t.Fatalf("control: an ancestor forks a descendant with grants it holds: %v", err)
	}
	if err := r.forkAs(t, c, r.sibling, r.child, *heldPatch(t, map[string]any{"system.sandbox": "judge"})); err != nil {
		t.Fatalf("control: a fork naming the sandbox it inherits changes nothing: %v", err)
	}
}

func TestAClaimOfAFigaroOutsideTheBoxIsRefused(t *testing.T) {
	r := newLineageRig(t, "")
	outside, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: outside.FigaroID, Patch: heldPatch(t, map[string]any{"system.sandbox": "judge"})})
	if err != nil {
		t.Fatal(err)
	}
	boxedChild := out.(rpc.ForkResponse).Alternative
	c := r.dial(t)
	err = r.setAs(t, c, outside.FigaroID, boxedChild, grantsPatch(t, []string{"x"}))
	if err == nil || !strings.Contains(err.Error(), "does not run in it") {
		t.Fatalf("a claim of a figaro outside the box passed: %v", err)
	}
	if err := r.setAs(t, c, "", boxedChild, grantsPatch(t, []string{"x"})); err == nil || !strings.Contains(err.Error(), "names no figaro") {
		t.Fatalf("an unclaimed call passed: %v", err)
	}
}

func TestTheOwnerSetsProtectedKeys(t *testing.T) {
	r := newLineageRig(t, "")
	if _, err := r.call(asOwner(), rpc.MethodSet, rpc.SetRequest{Patch: grantsPatch(t, []string{"a"})}); err == nil {
		t.Fatal("a set naming no aria passed")
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"a"})})); err != nil {
		t.Fatal(err)
	}
	if got := grantsOn(t, r, r.child); got != `["a"]` {
		t.Fatalf("grants %s", got)
	}
}

func TestGrantsMustBeAnArrayOfStrings(t *testing.T) {
	r := newLineageRig(t, "")
	for _, bad := range []any{"read", []int{1}, map[string]any{"a": 1}, nil, []string{" a"}, []string{"a "}, []string{"a", ""}} {
		_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, bad)}))
		if err == nil || !strings.Contains(err.Error(), form.GrantsKey) {
			t.Errorf("grants %v: %v", bad, err)
		}
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{})})); err != nil {
		t.Fatalf("control: an empty array: %v", err)
	}
}

func TestOpenProtectedKeysKeepTodaysRules(t *testing.T) {
	r := newLineageRig(t, protectedOpen)
	c := r.dial(t)
	err := r.setAs(t, c, r.parent, r.child, grantsPatch(t, []string{"x"}))
	if err == nil || !strings.Contains(err.Error(), "listed for") {
		t.Fatalf("open: a box set an unlisted system key: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"a"})})); err != nil {
		t.Fatal(err)
	}
}

const remoteSetsPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.remote]
grants = [{ verbs = ["set", "read", "create", "use"], on = ["*"] }]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "remote"
principals = ["user:remote"]
`

func TestAnUnboxedNonOwnerCannotSetProtectedKeys(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		refused      bool
	}{{"lineage", "", true}, {"open", "protected_keys = \"open\"\n", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRigConfigured(t, remoteSetsPolicy, tc.config)
			a, err := r.create(asOwner(), "mock")
			if err != nil {
				t.Fatal(err)
			}
			move := *heldPatch(t, map[string]any{"system.sandbox": "other"})
			_, err = r.h[rpc.MethodSet](as("user", "remote"), claimed(t, a.FigaroID, withAria(t, a.FigaroID, rpc.SetRequest{Patch: move})))
			if refused := err != nil && strings.Contains(err.Error(), "protected"); refused != tc.refused {
				t.Fatalf("refused %v, want %v: %v", refused, tc.refused, err)
			}
			if !tc.refused && err != nil {
				t.Fatal(err)
			}
			_, err = r.h[rpc.MethodSet](as("user", "remote"), claimed(t, a.FigaroID, withAria(t, a.FigaroID, rpc.SetRequest{Patch: grantsPatch(t, []string{"x"})})))
			if err == nil {
				t.Fatal("a non-owner with no grants of its own wrote grants")
			}
		})
	}
}

func TestAForkInheritsBothProtectedKeys(t *testing.T) {
	r := newLineageRig(t, "")
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:aria:*"})})); err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: r.parent})
	if err != nil {
		t.Fatal(err)
	}
	alt := out.(rpc.ForkResponse).Alternative
	if got := grantsOn(t, r, alt); got != `["read:aria:*"]` {
		t.Fatalf("the fork's grants %s", got)
	}
	if got := r.sandboxOf(t, alt); got != "judge" {
		t.Fatalf("the fork's sandbox %q", got)
	}
}

func TestTheFormAccessDenyListsProtectedKeysForAllButHeirs(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	access := func(figaro string) *rpc.FormAccess {
		var out rpc.FormResponse
		if err := c.Call(context.Background(), rpc.MethodForm, claimed(t, figaro, withAria(t, r.child, rpc.FormRequest{})), &out); err != nil {
			t.Fatal(err)
		}
		return out.Access
	}
	denies := func(a *rpc.FormAccess, key string) bool {
		for _, d := range a.Deny {
			if d.Denies(key) {
				return true
			}
		}
		return false
	}
	if a := access(r.child); a == nil || !denies(a, form.GrantsKey) {
		t.Fatalf("the child may write its own grants: %+v", a)
	}
	if a := access(r.parent); a == nil || denies(a, form.GrantsKey) || denies(a, "system.sandbox") {
		t.Fatalf("the parent may not write its child's protected keys: %+v", a)
	}
}

func TestABoxAncestorLosesADescendantTheOwnerMovedOut(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	if err := r.setAs(t, c, r.parent, r.sibling, grantsPatch(t, []string{"a"})); err != nil {
		t.Fatalf("control: the parent sets grants on a child still in its box: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.sandbox": "elsewhere"})})); err != nil {
		t.Fatal(err)
	}
	err := r.setAs(t, c, r.parent, r.child, grantsPatch(t, []string{"a"}))
	if err == nil {
		t.Fatal("the old box's ancestor set grants on a moved descendant")
	}
	t.Log(err)
	if got := grantsOn(t, r, r.child); got != "" {
		t.Fatalf("grants landed: %s", got)
	}
}

func TestAFigaroGrantsOnlyWhatItHolds(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	err := r.setAs(t, c, r.parent, r.child, grantsPatch(t, []string{"a", "write:aria:*"}))
	if err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("the parent granted what it does not hold: %v", err)
	}
	if got := grantsOn(t, r, r.child); got != "" {
		t.Fatalf("grants landed: %s", got)
	}
	err = r.forkAs(t, c, r.parent, r.parent, grantsPatch(t, []string{"write:aria:*"}))
	if err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("a fork -S granted beyond its writer: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, grantsPatch(t, []string{"a", "x"})); err != nil {
		t.Fatalf("control: a subset: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"write:aria:*"})})); err != nil {
		t.Fatalf("control: the owner is exempt: %v", err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: r.parent})
	if err != nil {
		t.Fatal(err)
	}
	if got := grantsOn(t, r, out.(rpc.ForkResponse).Alternative); got != `["read:aria:*","a","x"]` {
		t.Fatalf("a plain fork's grants %s", got)
	}
}
