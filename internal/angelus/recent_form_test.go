package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/recent"
)

// @recent lists the caller's figaros flat: a fork is a row of its own, not
// nested under its parent, and each row names itself by its handle's first
// word and carries its id and last activity.
func TestRecentListsEveryFigaroFlat(t *testing.T) {
	r := newGrantRig(t)
	parent, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	child := ariasFork(t, r, parent.FigaroID)
	snap, err := readArias(t, r, asOwner(), rpc.RecentFormID)
	if err != nil {
		t.Fatal(err)
	}
	for k := range snap.All() {
		if strings.Count(k, ".") != 1 {
			t.Errorf("@recent holds %q: want one row per figaro, nothing nested", k)
		}
	}
	rows := recent.Decode(snap)
	seen := map[string]recent.Row{}
	for _, row := range rows {
		seen[row.ID] = row
	}
	for _, id := range []string{parent.FigaroID, child} {
		row, ok := seen[id]
		if !ok {
			t.Fatalf("@recent = %+v, missing %s", rows, id)
		}
		if row.LastActive == 0 {
			t.Errorf("%s has no last activity", row.Name)
		}
		if row.Handle == "" || !strings.HasPrefix(row.Handle, row.Name) || strings.Contains(row.Name, handle.Separator) && row.Name != row.Handle {
			t.Errorf("row %q for handle %q: want its first word, or its whole handle", row.Name, row.Handle)
		}
	}
}

// Like @arias, @recent is computed for each caller from what that caller may
// read.
func TestEachCallerReadsTheirOwnRecent(t *testing.T) {
	r := newRigWith(t, endpointPolicy)
	owned, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	guest := as("user", "guest")
	theirs, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	view, err := readArias(t, r, guest, rpc.RecentFormID)
	if err != nil {
		t.Fatalf("a principal holding a role cannot read its @recent: %v", err)
	}
	ids := ariaIDsIn(view)
	if strings.Contains(ids, owned.FigaroID) {
		t.Errorf("the guest's @recent shows the owner's figaro: %s", ids)
	}
	if !strings.Contains(ids, theirs.FigaroID) {
		t.Errorf("the guest's @recent = %s, want its own %s", ids, theirs.FigaroID)
	}
	if _, err := readArias(t, r, as("user", "stranger"), rpc.RecentFormID); err == nil {
		t.Error("a principal holding no role read @recent")
	}
}

// @recent is built only once someone asks for it; asked after the caller's
// @arias is already watched, it still answers with every figaro, and later
// figaros appear in it.
func TestRecentAskedAfterAriasIsBuilt(t *testing.T) {
	r := newGrantRig(t)
	first, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readArias(t, r, asOwner(), rpc.AriasFormID); err != nil {
		t.Fatal(err)
	}
	snap, err := readArias(t, r, asOwner(), rpc.RecentFormID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ariaIDsIn(snap), first.FigaroID) {
		t.Fatalf("@recent read after @arias = %s, want %s", ariaIDsIn(snap), first.FigaroID)
	}
	second, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := readArias(t, r, asOwner(), rpc.RecentFormID); !strings.Contains(ariaIDsIn(snap), second.FigaroID) {
		t.Errorf("a later @recent read does not list %s", second.FigaroID)
	}
}
