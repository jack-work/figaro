package angelus_test

import (
	"context"
	"encoding/json"
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
