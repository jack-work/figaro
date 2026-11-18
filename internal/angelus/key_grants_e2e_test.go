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
	role := *heldPatch(t, map[string]any{"name": "minted", form.ForkedFromKey: "x"})
	if _, err := r.h[rpc.MethodCast](asOwner(), withAria(t, r.child, rpc.CastRequest{RolePatch: &role})); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Errorf("the owner minted a role carrying %s: %v", form.ForkedFromKey, err)
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
	r.ownerSets(t, r.parent, map[string]any{form.ChildPolicyKey: map[string]any{"system.model": "picked"}})
	if kid := r.forkAsFigaro(t, r.parent); keyOnString(t, r, kid, "system.model") != `"picked"` {
		t.Fatalf("the owner's policy model did not reach the child: %s", keyOnString(t, r, kid, "system.model"))
	}
	c := r.dial(t)
	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{"system.provider": "other"}})); err == nil || !strings.Contains(err.Error(), "only the owner puts it in a child policy") {
		t.Fatalf("an ancestor figaro put an owner-held key in a child policy: %v", err)
	}
	_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{form.ForkedFromKey: "x"}})}))
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("a policy carrying a runtime key was accepted: %v", err)
	}
}

func keyOnString(t *testing.T, r lineageRig, id, key string) string {
	t.Helper()
	v, _ := keyOn(t, r.grantRig, id, key)
	return string(v)
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

// Defends give-only-what-you-hold on a child policy's max_children: above the
// figaro's own it is refused when written, and at or below it the policy beats
// the cap of 0 a figaro's fork is born with when the policy is silent.
func TestAChildPolicyGivesMaxChildrenUpToItsHoldersOwn(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{form.FanoutMaxChildrenKey: 3})
	over := *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{form.FanoutMaxChildrenKey: 4}})
	if err := r.setAs(t, c, r.parent, r.child, over); err == nil || !strings.Contains(err.Error(), "more than this figaro's own 3") {
		t.Fatalf("an ancestor wrote a policy above the holder's own: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: over})); err == nil || !strings.Contains(err.Error(), "more than this figaro's own 3") {
		t.Fatalf("the owner wrote a policy above the holder's own: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{form.FanoutMaxChildrenKey: 2}})); err != nil {
		t.Fatalf("a policy under the holder's own was refused: %v", err)
	}
	kid := r.forkAsFigaro(t, r.child)
	if v, _ := keyOn(t, r.grantRig, kid, form.FanoutMaxChildrenKey); string(v) != `2` {
		t.Fatalf("the policy's max_children lost to the birth cap: %s", v)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{form.ChildPolicyKey})})); err != nil {
		t.Fatal(err)
	}
	r.ownerSets(t, r.child, map[string]any{form.ChildPolicyKey: map[string]any{"mantra": "quiet on fanout"}})
	kid = r.forkAsFigaro(t, r.child)
	if v, _ := keyOn(t, r.grantRig, kid, form.FanoutMaxChildrenKey); string(v) != `0` {
		t.Fatalf("a policy silent on fanout gave a figaro's fork %s, not the cap of 0", v)
	}
}

func (r lineageRig) setLocal(t *testing.T, figaro, target string, keys map[string]any) error {
	t.Helper()
	_, err := r.h[rpc.MethodSet](localOwner(), claimed(t, figaro, withAria(t, target, rpc.SetRequest{Patch: *heldPatch(t, keys)})))
	return err
}

// Defends the local door against a label: a labelled call is never the owner
// and takes every key rule the box door does. The four refusals are the
// P2+P5 review's DoorLocal repros (F5); each landed before.
func TestOnTheLocalDoorALabelOnlyNarrows(t *testing.T) {
	r := newLineageRig(t, "")
	kid := r.forkAsFigaro(t, r.parent)
	r.ownerSets(t, kid, map[string]any{"system.ttl": "30m"})
	for _, key := range []string{"system.model", "system.ttl"} {
		if err := r.setLocal(t, kid, kid, map[string]any{key: "1h"}); err == nil {
			t.Errorf("a default child set %s on itself through the local door", key)
		}
	}
	if err := r.setLocal(t, kid, kid, map[string]any{"mantra": "mine"}); err != nil {
		t.Errorf("a default child could not set its own mantra through the local door: %v", err)
	}

	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "write:aria:self#mantra"}})
	if err := r.setLocal(t, r.child, r.child, map[string]any{"ungranted_key": "x"}); err == nil {
		t.Error("a figaro holding only its mantra wrote another key through the local door")
	}

	r.ownerSets(t, r.parent, map[string]any{form.ScopesKey: []string{"read:*", "set:children#system.scopes"}})
	if err := r.setLocal(t, r.parent, r.sibling, map[string]any{form.ScopesKey: []string{"*:*"}}); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("a holder of set:children#system.scopes granted *:* through the local door: %v", err)
	}
	if err := r.setLocal(t, r.parent, r.sibling, map[string]any{form.ScopesKey: []string{"read:*"}}); err != nil {
		t.Errorf("a holder granted what it holds through the local door and was refused: %v", err)
	}

	r.ownerSets(t, r.sibling, map[string]any{form.ScopesKey: []string{"read:*", "write:aria:self#@lineage"}})
	if err := r.setLocal(t, r.sibling, r.sibling, map[string]any{form.ScopesKey: []string{"*:*"}}); err == nil {
		t.Error("a figaro holding write:aria:self#@lineage set its own scopes through the local door")
	}
}

// Defends the dressed check: an outfit's keys are judged as the caller's own
// patch, so naming an outfit gives the caller or its child nothing the caller
// could not write. The box rows are the P2+P5 review's F2 repros (seatfull is
// on the box's list); the runtime rows its F7 outfit gap.
func TestAnOutfitIsCheckedAsTheCallersOwnPatch(t *testing.T) {
	r := newLineageRig(t, "outfits = [\"judged\", \"seatfull\"]\n")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "fork:*", "use:*", "write:aria:self#mantra"}})
	mantra := *heldPatch(t, map[string]any{"mantra": "dressed"})
	if err := c.Call(t.Context(), rpc.MethodSet, claimed(t, r.child, withAria(t, r.child, rpc.SetRequest{Patch: mantra, Outfits: []string{"seatfull"}})), nil); err == nil {
		t.Error("a box figaro dressed itself in an outfit carrying scopes")
	}
	if got := grantsOn(t, r, r.child); strings.Contains(got, `"*:*"`) {
		t.Fatalf("the outfit's scopes landed: %s", got)
	}
	forkIn := func(ctx context.Context, figaro string, outfits ...string) error {
		raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: r.child, Outfits: outfits})
		if ctx == nil {
			return c.Call(t.Context(), rpc.MethodFork, claimed(t, figaro, raw), nil)
		}
		_, err := r.h[rpc.MethodFork](ctx, claimed(t, figaro, raw))
		return err
	}
	if err := forkIn(nil, r.child, "seatfull"); err == nil {
		t.Error("a box figaro gave its child an outfit's scopes and fanout")
	}
	if err := forkIn(nil, r.child, "judged"); err != nil {
		t.Fatalf("canary: a fork in a listed outfit with nothing to grant was refused: %v", err)
	}

	if err := forkIn(localOwner(), r.child, "judged-elsewhere"); err == nil || !strings.Contains(err.Error(), "system.provider") {
		t.Errorf("a labelled figaro gave its child another provider through an outfit: %v", err)
	}
	if _, err := r.h[rpc.MethodFork](localOwner(), rawJSON(t, rpc.ForkRequest{FigaroID: r.child, Outfits: []string{"judged-elsewhere"}})); err != nil {
		t.Errorf("the owner forked in another provider's outfit and was refused: %v", err)
	}

	if _, err := r.h[rpc.MethodFork](localOwner(), rawJSON(t, rpc.ForkRequest{FigaroID: r.child, Outfits: []string{"runtimebad"}})); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Errorf("the owner forked in an outfit carrying runtime.forked_from: %v", err)
	}
	if _, err := r.h[rpc.MethodCreate](localOwner(), rawJSON(t, rpc.CreateRequest{Outfits: []string{"runtimebad"}})); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Errorf("the owner minted in an outfit carrying runtime.forked_from: %v", err)
	}
}

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (r lineageRig) ariaCall(t *testing.T, local bool, figaro, method, id string, req any) error {
	t.Helper()
	raw := claimed(t, figaro, withAria(t, id, req))
	if local {
		_, err := r.h[method](localOwner(), raw)
		return err
	}
	return r.dial(t).Call(t.Context(), method, raw, nil)
}

// Defends study, drop and cast against a keyed set: a mantra grant must not
// let one figaro make another study a form (prompt injection), take a seat,
// or mint a role. The rows are the P2+P5 review's F3 repros, on the box door
// and on the local door.
func TestStudyDropAndCastNeedMoreThanAKeyedSet(t *testing.T) {
	r := newLineageRig(t, "")
	note, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"public": "read me"})})
	if err != nil {
		t.Fatal(err)
	}
	pub := note.(rpc.FormCreateResponse).FormID
	parent, _ := json.Marshal(r.parent)
	seat, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"name": "seat", "target-aria": json.RawMessage(parent)})})
	if err != nil {
		t.Fatal(err)
	}
	role := seat.(rpc.FormCreateResponse).FormID
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{
		"read:aria:" + r.sibling, "read:aria:" + pub, "write:aria:" + r.sibling + "#mantra", "write:aria:" + role + "#notes", "write:aria:self#mantra",
	}})
	for _, local := range []bool{false, true} {
		door := map[bool]string{false: "box", true: "local"}[local]
		if err := r.ariaCall(t, local, r.child, rpc.MethodStudy, r.sibling, rpc.StudyRequest{FormID: pub}); err == nil {
			t.Errorf("%s door: a mantra grant made another figaro study a form", door)
		}
		if err := r.ariaCall(t, local, r.child, rpc.MethodDrop, r.sibling, rpc.StudyRequest{FormID: pub}); err == nil {
			t.Errorf("%s door: a mantra grant dropped another figaro's study", door)
		}
		if err := r.ariaCall(t, local, r.child, rpc.MethodCast, r.child, rpc.CastRequest{FormID: role}); err == nil {
			t.Errorf("%s door: a notes grant on a role cast its holder's descendant into it", door)
		}
		minted := heldPatch(t, map[string]any{"name": "minted " + door})
		if err := r.ariaCall(t, local, r.child, rpc.MethodCast, r.child, rpc.CastRequest{RolePatch: minted}); err == nil {
			t.Errorf("%s door: a figaro holding no create minted a role through cast", door)
		}
	}
	if v, _ := keyOn(t, r.grantRig, role, "target-aria"); string(v) != string(parent) {
		t.Fatalf("the seat moved: target-aria %s", v)
	}
	if err := r.ariaCall(t, true, r.parent, rpc.MethodStudy, r.sibling, rpc.StudyRequest{FormID: pub}); err != nil {
		t.Fatalf("canary: a figaro holding set on the observer was refused a study: %v", err)
	}
	if err := r.ariaCall(t, true, r.parent, rpc.MethodDrop, r.sibling, rpc.StudyRequest{FormID: pub}); err != nil {
		t.Fatalf("canary: a figaro holding set on the observer was refused a drop: %v", err)
	}
}

func studiesOn(t *testing.T, r lineageRig, id string) string {
	t.Helper()
	v, _ := keyOn(t, r.grantRig, id, form.StudiesKey)
	return string(v)
}

// Defends the study read filter (P2+P5 review, F6): a key is admitted only
// when a read glob reaches everything under it, a standing study ends at the
// observer's next prompt once its read is revoked, and a fork keeps only the
// studies its own grants can read.
func TestAStudyReadsOnlyWhatItsObserverMayRead(t *testing.T) {
	r := newLineageRig(t, "")
	made, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"allowed": map[string]any{"top": "x", "deep": map[string]any{"secret": "y"}}})})
	if err != nil {
		t.Fatal(err)
	}
	f := made.(rpc.FormCreateResponse).FormID
	r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"*:aria:self", "read:aria:" + f + "#allowed.top", "read:aria:" + f + "#allowed.deep.**"}})
	if err := r.ariaCall(t, true, r.child, rpc.MethodStudy, r.child, rpc.StudyRequest{FormID: f, Keys: []string{"allowed.top"}}); err == nil {
		t.Error("a read of allowed.top admitted a study of everything under it")
	}
	if err := r.ariaCall(t, true, r.child, rpc.MethodStudy, r.child, rpc.StudyRequest{FormID: f, Keys: []string{"allowed.deep"}}); err != nil {
		t.Fatalf("a read of allowed.deep.** was refused a study of allowed.deep: %v", err)
	}
	if !strings.Contains(studiesOn(t, r, r.child), f) {
		t.Fatalf("the admitted study is missing: %s", studiesOn(t, r, r.child))
	}

	if _, err := r.h[rpc.MethodStudy](asOwner(), withAria(t, r.parent, rpc.StudyRequest{FormID: f})); err != nil {
		t.Fatal(err)
	}
	kid := r.forkAsFigaro(t, r.parent)
	if strings.Contains(studiesOn(t, r, kid), f) {
		t.Errorf("a figaro's fork kept a study its grants cannot read: %s", studiesOn(t, r, kid))
	}
	ownerKid, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: r.parent})
	if err != nil {
		t.Fatal(err)
	}
	if id := ownerKid.(rpc.ForkResponse).Alternative; !strings.Contains(studiesOn(t, r, id), f) {
		t.Errorf("the owner's fork lost a study it may read: %s", studiesOn(t, r, id))
	}

	free, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	m := free.FigaroID
	r.ownerSets(t, m, map[string]any{form.ScopesKey: []string{"*:aria:self", "read:aria:" + f}})
	if err := r.ariaCall(t, true, m, rpc.MethodStudy, m, rpc.StudyRequest{FormID: f}); err != nil {
		t.Fatal(err)
	}
	r.ownerSets(t, m, map[string]any{form.ScopesKey: []string{"*:aria:self"}})
	if _, err := r.h[rpc.MethodQua](asOwner(), withAria(t, m, rpc.QuaRequest{Text: "next turn"})); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(studiesOn(t, r, m), f) {
		t.Errorf("a revoked read left the study standing: %s", studiesOn(t, r, m))
	}
}
