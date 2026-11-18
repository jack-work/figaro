package form

import (
	"encoding/json"
	"testing"
)

// Defends the birth side of give-only-what-you-hold: a policy written when its
// holder could give that much, and the holder's own limit lowered since, gives
// a child the holder's own, not the policy's.
func TestAChildIsBornWithNoMoreMaxChildrenThanItsParentHolds(t *testing.T) {
	parent := Snapshot{}.Apply(Build(Snapshot{}, map[string]json.RawMessage{
		FanoutMaxChildrenKey: json.RawMessage(`1`),
		ChildPolicyKey:       json.RawMessage(`{"system.fanout.max_children":2,"mantra":"lane"}`),
	}, nil))
	if err := CheckChildPolicyFanout(parent); err == nil {
		t.Fatal("a policy above its holder's own passed the write check")
	}
	p, err := ChildPolicyPatch(parent, true)
	if err != nil {
		t.Fatal(err)
	}
	born := Snapshot{}.Apply(p)
	if n, ok, _ := FanoutMaxChildren(born); !ok || n != 1 {
		t.Fatalf("born with max_children %d (set %v), want the parent's own 1", n, ok)
	}
	if v, _ := born.Get("mantra"); string(v) != `"lane"` {
		t.Fatalf("the rest of the policy was lost: mantra %s", v)
	}
}

// Defends the birth merge: a child policy is read key path by key path, so a
// nested object never lands as a whole object on the child, an owner-held
// key nested in one is still seen as owner-held, and one path named twice is
// refused rather than settled by spelling.
func TestAChildPolicyIsAppliedKeyByKey(t *testing.T) {
	parent := Snapshot{}.Apply(Build(Snapshot{}, map[string]json.RawMessage{
		"system.model":  json.RawMessage(`"opus"`),
		"system.fanout": json.RawMessage(`{"max_children":4,"quota":9}`),
		ChildPolicyKey:  json.RawMessage(`{"system":{"scopes":["send:parent"],"model":"cheap"},"system.fanout":{}}`),
	}, nil))
	p, err := ChildPolicyPatch(parent, true)
	if err != nil {
		t.Fatal(err)
	}
	child := parent.Apply(p)
	if got, _ := child.Get("system.fanout"); string(got) != `{"max_children":4,"quota":9}` {
		t.Errorf("an empty object in the policy replaced the child's fanout: %s", got)
	}
	if got, _ := child.Get(ScopesKey); string(got) != `["send:parent"]` {
		t.Errorf("child scopes %s", got)
	}
	if got := ChildPolicyOwnerKeysChanged(Snapshot{}, parent); len(got) != 1 || got[0] != "system.model" {
		t.Errorf("owner-held keys a nested policy carries: %v, want [system.model]", got)
	}
	twice := parent.Apply(Build(Snapshot{}, map[string]json.RawMessage{
		ChildPolicyKey: json.RawMessage(`{"system":{"scopes":["send:parent"]},"system.scopes":["read:*"]}`),
	}, nil))
	if _, _, err := ChildPolicy(twice); err == nil {
		t.Error("a policy naming system.scopes twice was read")
	}
}
