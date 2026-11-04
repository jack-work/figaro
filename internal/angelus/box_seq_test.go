package angelus_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// Client ids are chosen by the caller, so a box could take the owner's id and
// spend its next seq first. If seqs were keyed by client alone, the owner's
// write with that seq would be answered Duplicate and never applied, and the
// owner's pit would believe it landed. Seqs are keyed by the authenticated
// principal too: the box can only collide with its own writes.
func TestABoxCannotSpendTheOwnersSeq(t *testing.T) {
	d := boxDoorRig(t, "true")
	c := d.dial(t)
	held := boxMintsForm(t, c, "shared")

	set := func(v string) rpc.SetRequest {
		raw, _ := json.Marshal(v)
		return rpc.SetRequest{
			Patch:  form.Build(form.Snapshot{}, map[string]json.RawMessage{"emergency": raw}, nil),
			Client: "owner-pit", Seq: 1,
		}
	}
	var boxed rpc.SetResponse
	if err := boxOnAria(t, c, rpc.MethodSet, held, set("box"), &boxed); err != nil {
		t.Fatalf("the box writing its own form: %v", err)
	}
	got, err := d.h[rpc.MethodSet](asOwner(), withAria(t, held, set("owner")))
	if err != nil {
		t.Fatalf("the owner's write: %v", err)
	}
	if resp := got.(rpc.SetResponse); resp.Outcome != rpc.OutcomeApplied {
		t.Fatalf("the owner's seq 1 was answered %q after the box spent the same client id and seq", resp.Outcome)
	}
	if v := d.keyOf(t, held, "emergency"); v != "owner" {
		t.Fatalf("emergency = %q, want the owner's write", v)
	}

	var boxLoad rpc.FormResponse
	if err := boxOnAria(t, c, rpc.MethodForm, held, rpc.FormRequest{Client: "owner-pit"}, &boxLoad); err != nil {
		t.Fatal(err)
	}
	ownerLoad, err := d.h[rpc.MethodForm](asOwner(), withAria(t, held, rpc.FormRequest{Client: "owner-pit"}))
	if err != nil {
		t.Fatal(err)
	}
	owner := ownerLoad.(rpc.FormResponse)
	if boxLoad.Origin == "" || boxLoad.Origin == owner.Origin {
		t.Fatalf("box and owner share an origin tag %q: a box delta could settle the owner's pending edit", owner.Origin)
	}
	if boxLoad.LastSeq != 1 || owner.LastSeq != 1 {
		t.Fatalf("LastSeq box=%d owner=%d, want each to see only its own seq 1", boxLoad.LastSeq, owner.LastSeq)
	}
}
