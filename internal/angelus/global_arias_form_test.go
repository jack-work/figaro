package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestGlobalAriasUsesTheCallersGlobalListVisibility(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	owned, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	guest := as("user", "guest")
	theirs, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		guest bool
	}{{"owner", false}, {"guest", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := asOwner()
			if tc.guest {
				ctx = guest
			}
			out, err := r.call(ctx, rpc.MethodList, rpc.ListRequest{Global: true})
			if err != nil {
				t.Fatal(err)
			}
			snap, err := readArias(t, r, ctx, rpc.GlobalAriasFormID)
			if err != nil {
				t.Fatal(err)
			}
			ids := "," + ariaIDsIn(snap) + ","
			anchors := 0
			visible := map[string]bool{}
			for _, row := range out.(rpc.ListResponse).Figaros {
				if row.ReapedAt != 0 {
					continue
				}
				visible[row.ID] = true
				if !strings.Contains(ids, ","+row.ID+",") {
					t.Errorf("global form missing visible row %s (%s)", row.ID, row.State)
				}
				if row.State == "anchor" {
					anchors++
				}
			}
			for _, id := range strings.Split(ariaIDsIn(snap), ",") {
				if !visible[id] {
					t.Errorf("global form exposed row absent from caller's list: %s", id)
				}
			}
			if !tc.guest && anchors == 0 {
				t.Fatal("fixture has no anchors")
			}
			if tc.guest && strings.Contains(ids, owned.FigaroID) {
				t.Fatal("guest sees owner's figaro")
			}
			if tc.guest && !strings.Contains(ids, theirs.FigaroID) {
				t.Fatal("guest cannot see own figaro")
			}
		})
	}
	if _, err := readArias(t, r, as("user", "stranger"), rpc.GlobalAriasFormID); err == nil {
		t.Fatal("stranger read global form")
	}
}
