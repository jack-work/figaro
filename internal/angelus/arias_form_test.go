package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func readArias(t *testing.T, r grantRig, ctx context.Context, id string) (form.Snapshot, error) {
	t.Helper()
	out, err := r.h[rpc.MethodForm](ctx, withAria(t, id, rpc.FormRequest{}))
	if err != nil {
		return form.Snapshot{}, err
	}
	switch resp := out.(type) {
	case rpc.FormResponse:
		return resp.Snapshot, nil
	case *rpc.FormResponse:
		return resp.Snapshot, nil
	}
	t.Fatalf("form read answered %T", out)
	return form.Snapshot{}, nil
}

func ariaIDsIn(snap form.Snapshot) string {
	var ids []string
	for k, v := range snap.All() {
		if strings.HasSuffix(k, ".id") {
			ids = append(ids, strings.Trim(string(v), `"`))
		}
	}
	return strings.Join(ids, ",")
}

// @arias is computed for each caller from the arias that caller may read: one
// principal never sees another's rows, and there is no per-principal id on the
// wire to name instead. Naming anything else under @arias is not found.
func TestEachCallerReadsTheirOwnArias(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	owned, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	remote := as("user", "guest")
	theirs, err := r.create(remote, "mock")
	if err != nil {
		t.Fatal(err)
	}

	ownerView, err := readArias(t, r, asOwner(), rpc.AriasFormID)
	if err != nil {
		t.Fatal(err)
	}
	if ids := ariaIDsIn(ownerView); !strings.Contains(ids, owned.FigaroID) {
		t.Errorf("the owner's @arias = %s, want its own %s", ids, owned.FigaroID)
	}
	if _, err := r.call(remote, rpc.MethodAttach, rpc.AttachRequest{FigaroID: rpc.AriasFormID}); err != nil {
		t.Errorf("a principal holding a role cannot attach @arias: %v", err)
	}
	remoteView, err := readArias(t, r, remote, rpc.AriasFormID)
	if err != nil {
		t.Fatalf("a principal holding a role cannot read its @arias: %v", err)
	}
	ids := ariaIDsIn(remoteView)
	if strings.Contains(ids, owned.FigaroID) {
		t.Errorf("the guest's @arias shows the owner's aria: %s", ids)
	}
	if !strings.Contains(ids, theirs.FigaroID) {
		t.Errorf("the guest's @arias = %s, want its own %s", ids, theirs.FigaroID)
	}

	for _, guess := range []string{
		"@arias/user:SELF", "@arias~0", "@arias.", "@arias@user:guest", "@ariasx",
		"@arias\x00user:guest",
	} {
		if _, err := readArias(t, r, remote, guess); err == nil {
			t.Errorf("reading %q was answered: only @arias names the caller's own", guess)
		}
	}
	set := form.Build(form.Snapshot{}, map[string]json.RawMessage{"peers": json.RawMessage("1")}, nil)
	if _, err := r.h[rpc.MethodSet](remote, withAria(t, rpc.AriasFormID, rpc.SetRequest{Patch: set})); err == nil {
		t.Error("@arias took a write")
	}
}

// A principal with no role here reads nothing, as with list.
func TestAStrangerCannotReadArias(t *testing.T) {
	r := newGrantRig(t)
	if _, err := r.create(asOwner(), "mock"); err != nil {
		t.Fatal(err)
	}
	if _, err := readArias(t, r, as("user", "stranger"), rpc.AriasFormID); err == nil {
		t.Error("a principal holding no role read @arias")
	}
}

// A fork nests under its parent, so the form's nesting is the fork tree.
func TestAForkNestsUnderItsParent(t *testing.T) {
	r := newGrantRig(t)
	parent, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: parent.FigaroID})
	if err != nil {
		t.Fatal(err)
	}
	child := out.(rpc.ForkResponse).Alternative
	snap, err := readArias(t, r, asOwner(), rpc.AriasFormID)
	if err != nil {
		t.Fatal(err)
	}
	var parentPath, childPath string
	for k, v := range snap.All() {
		switch strings.Trim(string(v), `"`) {
		case parent.FigaroID:
			parentPath = strings.TrimSuffix(k, ".id")
		case child:
			childPath = strings.TrimSuffix(k, ".id")
		}
	}
	if parentPath == "" || !strings.HasPrefix(childPath, parentPath+".forks.") {
		t.Errorf("child at %q, parent at %q: want the child under the parent's forks", childPath, parentPath)
	}
}

// A read of @arias that does not follow it still sees the list as it is now,
// not as the first read built it.
func TestAReadOfAriasIsCurrent(t *testing.T) {
	r := newGrantRig(t)
	first, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := readArias(t, r, asOwner(), rpc.AriasFormID); !strings.Contains(ariaIDsIn(snap), first.FigaroID) {
		t.Fatal("the first read does not list the aria")
	}
	second, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := readArias(t, r, asOwner(), rpc.AriasFormID); !strings.Contains(ariaIDsIn(snap), second.FigaroID) {
		t.Error("a later read does not list an aria made since the first")
	}
}
