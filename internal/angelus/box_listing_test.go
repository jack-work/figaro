package angelus_test

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// An outfit anchor holds the outfit's dress, credo included. A box may list
// the anchors of outfits that dress into its own box and no others: before,
// every box saw every anchor's id, so a receipt box listed the chat outfit.
func TestABoxListsOnlyTheAnchorsItMayRead(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgeWearsOnlyJudged)
	if _, err := d.create(asOwner(), "judged"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.create(asOwner(), "mock"); err != nil {
		t.Fatal(err)
	}

	out, err := d.call(asOwner(), rpc.MethodList, rpc.ListRequest{Global: true})
	if err != nil {
		t.Fatal(err)
	}
	ownBox, elsewhere := map[string]bool{}, map[string]bool{}
	for _, row := range out.(rpc.ListResponse).Figaros {
		if row.State != "anchor" {
			continue
		}
		if row.ID != "null" && d.keyOf(t, row.ID, "system.sandbox") == "judge" {
			ownBox[row.ID] = true
		} else {
			elsewhere[row.ID] = true
		}
	}
	if len(ownBox) == 0 || len(elsewhere) == 0 {
		t.Fatalf("canary: the owner should list anchors in and out of the box, got in %v, out %v", ownBox, elsewhere)
	}

	c := d.dial(t)
	var boxed rpc.ListResponse
	if err := c.Call(t.Context(), rpc.MethodList, rpc.ListRequest{Global: true}, &boxed); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range boxed.Figaros {
		if row.State != "anchor" {
			continue
		}
		seen[row.ID] = true
		if elsewhere[row.ID] {
			t.Errorf("the box lists anchor %s, which dresses outside it", row.ID)
		}
	}
	for id := range ownBox {
		if !seen[id] {
			t.Errorf("the box lost anchor %s, which dresses into it", id)
		}
	}

	if err := c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"judged"}}, nil); err != nil {
		t.Fatalf("the box no longer dresses in its own outfit: %v", err)
	}
}
