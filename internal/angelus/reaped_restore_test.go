package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func killSoft(t *testing.T, d doorRig, id string) {
	t.Helper()
	if _, err := d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: id}); err != nil {
		t.Fatal(err)
	}
	if !reaped(t, d.a, id) {
		t.Fatalf("%s was not soft-deleted", id)
	}
}

func TestTheOwnerRestoresAReapedAriaInOneCall(t *testing.T) {
	d := boxDoorRig(t, "true")
	made, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	killSoft(t, d, made.FigaroID)
	out, err := d.call(asOwner(), rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: made.FigaroID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var resp rpc.ReapedRestoreResponse
	_ = json.Unmarshal(raw, &resp)
	if resp.Root != made.FigaroID || len(resp.Restored) != 1 {
		t.Errorf("restore = %+v", resp)
	}
	if reaped(t, d.a, made.FigaroID) {
		t.Error("the aria is still reaped after its restore")
	}
	if _, err := d.call(asOwner(), rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: made.FigaroID}); err == nil || !strings.Contains(err.Error(), "not reaped") {
		t.Errorf("restoring a live aria = %v, want 'not reaped'", err)
	}
	if _, err := d.call(asOwner(), rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: "../etc"}); err == nil {
		t.Error("a malformed row name was accepted")
	}
}

// A reaped group holding an aria outside the box: the box may set the root,
// so the call passes the door, but restore checks every node it would touch.
func TestABoxRestoreChecksEveryNodeOfTheGroup(t *testing.T) {
	d := boxDoorRig(t, "true")
	root, fork := boxAriaWithForkOutside(t, d, true)
	if _, err := d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: root, Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if !reaped(t, d.a, fork) || !reaped(t, d.a, root) {
		t.Fatal("the owner's kill -r did not reap both")
	}
	c := d.dial(t)
	if err := c.Call(context.Background(), rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: root}, nil); err == nil {
		t.Error("a box restored a group that holds an aria outside its box")
	}
	if !reaped(t, d.a, fork) || !reaped(t, d.a, root) {
		t.Error("a refused restore changed the group")
	}
}

// system.reaped is held from boxes (kill writes it): a box may not undo the
// reap of its own aria, whose purge is what the box's grace promises.
func TestABoxCannotRestoreItsOwnReapedAria(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	killSoft(t, d, inBox.FigaroID)
	err = d.dial(t).Call(context.Background(), rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: inBox.FigaroID}, nil)
	if err == nil {
		t.Error("a box restored its own reaped aria")
	}
	if !reaped(t, d.a, inBox.FigaroID) {
		t.Error("the box's restore undid the reap")
	}
}
