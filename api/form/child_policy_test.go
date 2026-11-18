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
