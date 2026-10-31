package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

func boxMintsForm(t *testing.T, c *jkrpc.Client, name string) string {
	t.Helper()
	var made rpc.FormCreateResponse
	if err := c.Call(context.Background(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch(name)}, &made); err != nil {
		t.Fatalf("the box minting a form: %v", err)
	}
	return made.FormID
}

func namePatch(v string) *form.Patch {
	raw, _ := json.Marshal(v)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": raw}, nil)
	return &p
}

func unsetPatch(key string) form.Patch {
	return form.Build(form.Snapshot{}, nil, []string{key})
}

func (d doorRig) keyOf(t *testing.T, id, key string) string {
	t.Helper()
	snap, err := d.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup(key); v != nil {
		return *v
	}
	return ""
}

// The VM test on spain saw `figaro form new` from box0 mint a form the box
// could not then read; a form a box mints is held in that box.
func TestAFormABoxMintsIsHeldInItsBox(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)

	held := boxMintsForm(t, c, "inside")
	if got := d.sandboxOf(t, held); got != "judge" {
		t.Fatalf("a form the box minted is held in %q, want judge", got)
	}
	if err := boxOnAria(t, c, rpc.MethodContext, held, struct{}{}, nil); err != nil {
		t.Fatalf("the box reading its own form: %v", err)
	}
	if err := setKey(t, c, held, "name", "renamed"); err != nil {
		t.Fatalf("the box setting its own form: %v", err)
	}
	if err := boxOnAria(t, c, rpc.MethodStudy, inBox.FigaroID, rpc.StudyRequest{FormID: held}, nil); err != nil {
		t.Fatalf("the box studying its own form: %v", err)
	}
	if err := boxOnAria(t, c, rpc.MethodCast, inBox.FigaroID, rpc.CastRequest{FormID: held}, nil); err != nil {
		t.Fatalf("the box casting its aria into its own form: %v", err)
	}
	if got := d.keyOf(t, held, "target-aria"); got != inBox.FigaroID {
		t.Fatalf("cast pointed the role at %q", got)
	}

	if err := setKey(t, c, held, "system.sandbox", "local"); err == nil {
		t.Fatal("the box moved its form out of the box")
	}
	if err := boxOnAria(t, c, rpc.MethodSet, held, rpc.SetRequest{Patch: unsetPatch("system.sandbox")}, nil); err == nil {
		t.Fatal("the box cleared the hold on its form")
	}
	if got := d.sandboxOf(t, held); got != "judge" {
		t.Fatalf("a refused change landed: held in %q", got)
	}

	owners, err := d.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch("the owner's")})
	if err != nil {
		t.Fatal(err)
	}
	ownersForm := owners.(rpc.FormCreateResponse).FormID
	if got := d.sandboxOf(t, ownersForm); got != "" {
		t.Fatalf("a form the owner minted is held in %q", got)
	}
	if err := boxOnAria(t, c, rpc.MethodContext, ownersForm, struct{}{}, nil); err == nil {
		t.Fatal("the box read the owner's form")
	}

	var role rpc.CastResponse
	if err := boxOnAria(t, c, rpc.MethodCast, inBox.FigaroID, rpc.CastRequest{RolePatch: namePatch("a seat")}, &role); err != nil {
		t.Fatalf("the box minting a role for its own aria: %v", err)
	}
	if got := d.sandboxOf(t, role.RoleID); got != "judge" {
		t.Fatalf("a role the box minted by cast is held in %q, want judge", got)
	}
}

// A form carrying target-aria is a role anyone may send to by its id. One held
// in a box must not become a seat for an aria outside it, and the box must not
// dress one up to pull host traffic in unmarked.
func TestARoleHeldInABoxPointsOnlyIntoIt(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	held := boxMintsForm(t, c, "El Inspector")

	raw, _ := json.Marshal(outside.FigaroID)
	pointOut := form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil)
	if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, held, rpc.SetRequest{Patch: pointOut})); err == nil || !strings.Contains(err.Error(), "held in box judge") {
		t.Fatalf("the owner pointed a role held in the box at an aria outside it: %v", err)
	}
	if _, err := d.h[rpc.MethodCast](asOwner(), withAria(t, outside.FigaroID, rpc.CastRequest{FormID: held})); err == nil {
		t.Fatal("the owner cast an aria outside the box into a role held in it")
	}
	copyOut := form.Merge(*namePatch("copy"), pointOut)
	if _, err := d.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Parent: held, Patch: &copyOut}); err == nil {
		t.Fatal("a copy of a role held in the box points outside it")
	}
	if got := d.keyOf(t, held, "target-aria"); got != "" {
		t.Fatalf("a refused pointing landed: %q", got)
	}

	if err := boxOnAria(t, c, rpc.MethodCast, inBox.FigaroID, rpc.CastRequest{FormID: held}, nil); err != nil {
		t.Fatalf("the box seating its own aria: %v", err)
	}
	if got := d.keyOf(t, held, "target-aria"); got != inBox.FigaroID {
		t.Fatalf("role points at %q", got)
	}

	out, err := d.call(asOwner(), rpc.MethodList, rpc.ListRequest{Global: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range out.(rpc.ListResponse).Figaros {
		if row.ID == held {
			if row.Sandbox != "judge" || row.TargetAria != inBox.FigaroID {
				t.Fatalf("the listing shows the held role as %+v", row)
			}
			return
		}
	}
	t.Fatal("the held role is not listed")
}

func TestABoxMintsAtMostItsShareOfForms(t *testing.T) {
	d := boxDoorRig(t, "true")
	c := d.dial(t)
	for i := 0; i < 256; i++ {
		boxMintsForm(t, c, "f")
	}
	err := c.Call(context.Background(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch("one more")}, nil)
	if err == nil || !strings.Contains(err.Error(), "the most a box may mint") {
		t.Fatalf("form 257: %v", err)
	}
	if _, err := d.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch("the owner's")}); err != nil {
		t.Fatalf("the box's cap reached the owner: %v", err)
	}
}
