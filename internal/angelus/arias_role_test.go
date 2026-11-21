package angelus

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestAriasSnapshotCarriesTheRoleOnlyWhereOneIsHeld(t *testing.T) {
	snap := ariasSnapshot([]rpc.FigaroInfoResponse{
		{ID: "a", Handle: "alpha", Mantra: "one", Roles: []string{"@boss", "@ee927"}},
		{ID: "b", Handle: "bravo", Mantra: "two"},
	}, 0, 0)
	if v := snap.Lookup("arias.alpha.role"); v == nil || *v != "@boss @ee927" {
		t.Fatalf("alpha's role = %v, want @boss @ee927", v)
	}
	if v := snap.Lookup("arias.bravo.role"); v != nil {
		t.Fatalf("bravo holds no role but carries %q", *v)
	}
}

func TestAriasSnapshotMarksARetiredFigaro(t *testing.T) {
	snap := ariasSnapshot([]rpc.FigaroInfoResponse{
		{ID: "a", Handle: "alpha", RetiredAt: 1700000000000},
		{ID: "b", Handle: "bravo"},
	}, 0, 0)
	if v, ok := snap.Get("arias.alpha.retired_at"); !ok || string(v) != "1700000000000" {
		t.Fatalf("alpha's retired_at = %s, want 1700000000000", v)
	}
	if v, ok := snap.Get("arias.bravo.retired_at"); ok {
		t.Fatalf("bravo is live but carries retired_at %s", v)
	}
}

func TestARetireChangesTheAriasPrint(t *testing.T) {
	live := []rpc.FigaroInfoResponse{{ID: "a", Handle: "alpha"}}
	retired := []rpc.FigaroInfoResponse{{ID: "a", Handle: "alpha", RetiredAt: 1700000000000}}
	if ariasPrint(live, 0) == ariasPrint(retired, 0) {
		t.Fatal("a retire leaves the @arias print unchanged, so the form is not rebuilt")
	}
}
