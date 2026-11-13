package angelus_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// Removing a key rebuilds its tree node from the in-order successor. With
// "system" as that successor, a successor dropped to a bare leaf nulled the
// whole system branch: the write guard saw system.* change and refused the
// box. The box's own unset must succeed and leave system.* byte for byte.
func TestABoxUnsetLeavesTheSystemBranchWhole(t *testing.T) {
	d := boxEndpointRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	onIt := func(req rpc.SetRequest) error { return boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, req, nil) }
	before, ok := keyOn(t, d.grantRig, inBox.FigaroID, "system")
	if !ok {
		t.Fatal("the box aria has no system branch")
	}
	var keys []string
	for i := 0; i < 12; i++ {
		keys = append(keys, fmt.Sprintf("s%02d", i))
	}
	for _, k := range keys {
		if err := onIt(rpc.SetRequest{Patch: *heldPatch(t, map[string]any{k: "x"})}); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	for i := len(keys) - 1; i >= 0; i-- {
		k := keys[i]
		if err := onIt(rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{k})}); err != nil {
			t.Fatalf("unset %s, whose successor is system: %v", k, err)
		}
		after, ok := keyOn(t, d.grantRig, inBox.FigaroID, "system")
		if !ok || !bytes.Equal(before, after) {
			t.Fatalf("unset %s changed system:\nbefore %s\nafter  %s", k, before, after)
		}
		if _, still := keyOn(t, d.grantRig, inBox.FigaroID, k); still {
			t.Fatalf("%s is still there", k)
		}
	}
}
