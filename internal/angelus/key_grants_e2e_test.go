package angelus_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func (r lineageRig) ownerSets(t *testing.T, id string, keys map[string]any) {
	t.Helper()
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, keys)})); err != nil {
		t.Fatal(err)
	}
}

func (r lineageRig) forkAsFigaro(t *testing.T, figaro string) string {
	t.Helper()
	c := r.dial(t)
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: figaro})
	var out rpc.ForkResponse
	if err := c.Call(context.Background(), rpc.MethodFork, claimed(t, figaro, raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Alternative
}

// Defends the per-key write check: skip it and the child writes any key on
// its own form.
func TestAWriteIsCheckedKeyByKey(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "write:aria:self#mantra", "delete:aria:self#mantra"}})
	if err := r.setAs(t, c, r.child, r.child, *heldPatch(t, map[string]any{"mantra": "mine"})); err != nil {
		t.Fatalf("a granted key was refused: %v", err)
	}
	err := r.setAs(t, c, r.child, r.child, *heldPatch(t, map[string]any{"note": "x"}))
	if err == nil || !strings.Contains(err.Error(), "do not reach that key") {
		t.Fatalf("an ungranted key was written: %v", err)
	}
	both := *heldPatch(t, map[string]any{"mantra": "again", "note": "x"})
	if err := r.setAs(t, c, r.child, r.child, both); err == nil {
		t.Fatal("a patch with one ungranted key landed")
	}
	if v, _ := keyOn(t, r.grantRig, r.child, "mantra"); string(v) != `"mine"` {
		t.Fatalf("the refused patch landed in part: mantra %s", v)
	}
	drop := form.Build(form.Snapshot{}, nil, []string{"mantra"})
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "write:aria:self#mantra"}})
	if err := r.setAs(t, c, r.child, r.child, drop); err == nil || !strings.Contains(err.Error(), "may not delete mantra") {
		t.Fatalf("a removal passed on write alone: %v", err)
	}
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "write:aria:self#mantra", "delete:aria:self#mantra"}})
	if err := r.setAs(t, c, r.child, r.child, drop); err != nil {
		t.Fatalf("delete granted and refused: %v", err)
	}
}

func TestNoGrantWritesARuntimeKey(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	for _, key := range []string{form.ForkedFromKey, form.RetiredKey, "runtime.anything"} {
		if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{key: "x"})); err == nil {
			t.Errorf("a figaro holding *:* wrote %s", key)
		}
		if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{key: "x"})})); err == nil {
			t.Errorf("the owner wrote %s", key)
		}
	}
	if err := r.setAs(t, c, r.parent, r.child, grantsPatch(t, []string{"write:*#runtime.**"})); err == nil {
		t.Error("a grant on runtime.* was accepted")
	}
}

func TestAWriterOnAnotherFigarosScopesGrantsOnlyWhatItHolds(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "fork:aria:self", "set:children#mantra", "set:children#system.scopes"}})
	grandkid := r.forkAsFigaro(t, r.child)
	for scopes, ok := range map[string]bool{
		`["send:parent","set:aria:self#mantra"]`: false,
		`["read:*","set:aria:self#mantra"]`:      true,
		`["set:aria:self#mantra"]`:               true,
		`["set:aria:self"]`:                      false,
		`["send:*"]`:                             false,
		`["*:*"]`:                                false,
	} {
		var v []string
		_ = json.Unmarshal([]byte(scopes), &v)
		err := r.setAs(t, c, r.child, grandkid, grantsPatch(t, v))
		if (err == nil) != ok {
			t.Errorf("child gives its child %s: %v, want ok=%v", scopes, err, ok)
		}
	}
}

func TestAForkMadeByAFigaroCanReachOnlyItsParent(t *testing.T) {
	r := newLineageRig(t, "")
	kid := r.forkAsFigaro(t, r.parent)
	if got := grantsOn(t, r, kid); got != `["send:parent","read:aria:self","write:aria:self#@self","delete:aria:self#@self"]` {
		t.Fatalf("default child scopes %s", got)
	}
	if got := strings.Join(grantsOf(t, r.grantRig, kid).Effective, ", "); got != "read:aria:"+kid+", send:aria:"+r.parent+", set:aria:"+kid+"#@self, delete:aria:"+kid+"#@self" {
		t.Fatalf("effective %s", got)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: r.parent})
	if err != nil {
		t.Fatal(err)
	}
	if got := grantsOn(t, r, out.(rpc.ForkResponse).Alternative); !strings.Contains(got, "*:*") {
		t.Fatalf("a fork the owner makes keeps its parent's scopes, got %s", got)
	}
}

func TestAChildPolicyIsAppliedAtBirthAndNotKeptLinked(t *testing.T) {
	r := newLineageRig(t, "")
	r.ownerSets(t, r.parent, map[string]any{form.ChildPolicyKey: map[string]any{
		form.ScopesKey: []string{"read:*", "send:aria:self"},
		"mantra":       "a lane",
	}})
	kid := r.forkAsFigaro(t, r.parent)
	if got := grantsOn(t, r, kid); got != `["read:*","send:aria:self"]` {
		t.Fatalf("policy scopes %s", got)
	}
	if v, _ := keyOn(t, r.grantRig, kid, "mantra"); string(v) != `"a lane"` {
		t.Fatalf("policy mantra %s", v)
	}
	eff := grantsOf(t, r.grantRig, kid).Effective
	if slices.Contains(eff, "send:aria:"+r.parent) {
		t.Fatalf("a policy without send:parent still reaches the parent: %s", eff)
	}
	r.ownerSets(t, r.parent, map[string]any{form.ChildPolicyKey: map[string]any{"mantra": "changed"}})
	if v, _ := keyOn(t, r.grantRig, kid, "mantra"); string(v) != `"a lane"` {
		t.Fatalf("a policy edit reached a living child: %s", v)
	}
	_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{"system.model": "x"}})}))
	if err == nil || !strings.Contains(err.Error(), "owner class") {
		t.Fatalf("a policy carrying an owner-class key was accepted: %v", err)
	}
}

// A default child writes its own board (mantra above all) and nothing of
// another class on itself, and nothing at all on anyone else.
func TestADefaultChildWritesOnlyItsOwnSelfClassKeys(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	kid := r.forkAsFigaro(t, r.parent)
	for keys, ok := range map[string]bool{
		`{"mantra":"lane"}`:                      true,
		`{"notes.today":"x"}`:                    true,
		`{"system.thinking_effort":"low"}`:       true,
		`{"system.scopes":["*:*"]}`:              false,
		`{"system.child_policy":{"mantra":"x"}}`: false,
		`{"system.model":"x"}`:                   false,
		`{"system.ttl":"1h"}`:                    false,
		`{"runtime.forked_from":"x"}`:            false,
		`{"system.fanout.max_children":3}`:       false,
	} {
		var m map[string]any
		_ = json.Unmarshal([]byte(keys), &m)
		err := r.setAs(t, c, kid, kid, *heldPatch(t, m))
		if (err == nil) != ok {
			t.Errorf("default child sets %s on itself: %v, want ok=%v", keys, err, ok)
		}
	}
	if err := r.setAs(t, c, kid, kid, form.Build(form.Snapshot{}, nil, []string{"mantra"})); err != nil {
		t.Errorf("default child could not unset its own mantra: %v", err)
	}
	if err := r.setAs(t, c, kid, r.parent, *heldPatch(t, map[string]any{"mantra": "x"})); err == nil {
		t.Error("default child set its parent's mantra")
	}
}

// Defends the child policy's shapes: a policy that would give a child a value
// outside its row's shape is refused when it is written, owner included, and
// a leaf it carries is judged as the key it lands under.
func TestAChildPolicyCarriesOnlyWellShapedValues(t *testing.T) {
	r := newLineageRig(t, "")
	for want, policy := range map[string]map[string]any{
		"system.fanout.max_children": {form.FanoutMaxChildrenKey: "many"},
		form.ScopesKey:               {form.ScopesKey: "read:*"},
		"system.study_incantation":   {"system.study_incantation.onstudied": "x"},
	} {
		_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ChildPolicyKey: policy})}))
		if err == nil || !strings.Contains(err.Error(), "a child would be born with "+want) {
			t.Errorf("policy %v: %v", policy, err)
		}
	}
	r.ownerSets(t, r.parent, map[string]any{form.ChildPolicyKey: map[string]any{
		"system.study_incantation.onstudy": "hello",
		"system.thinking_budget":           2048,
	}})
	kid := r.forkAsFigaro(t, r.parent)
	if v, _ := keyOn(t, r.grantRig, kid, "system.study_incantation"); string(v) != `{"onstudy":"hello"}` {
		t.Fatalf("policy incantation %s", v)
	}
	if v, _ := keyOn(t, r.grantRig, kid, "system.thinking_budget"); string(v) != `2048` {
		t.Fatalf("policy thinking_budget %s", v)
	}
}
