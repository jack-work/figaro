package angelus

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestAriasSnapshotCarriesTheRoleOnlyWhereOneIsHeld(t *testing.T) {
	snap := ariasSnapshot([]rpc.FigaroInfoResponse{
		{ID: "a", Handle: "alpha", Mantra: "one", Roles: []string{"@boss", "@ee927"}},
		{ID: "b", Handle: "bravo", Mantra: "two"},
	}, 0)
	if v := snap.Lookup("arias.alpha.role"); v == nil || *v != "@boss @ee927" {
		t.Fatalf("alpha's role = %v, want @boss @ee927", v)
	}
	if v := snap.Lookup("arias.bravo.role"); v != nil {
		t.Fatalf("bravo holds no role but carries %q", *v)
	}
}
