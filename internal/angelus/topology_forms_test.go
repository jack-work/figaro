package angelus_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func ownerMintsForm(t *testing.T, r grantRig, name string) string {
	t.Helper()
	v, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch(name)})
	if err != nil {
		t.Fatal(err)
	}
	return v.(rpc.FormCreateResponse).FormID
}

// A v0.65.0 regression: @topology moves a form as well as an aria, so the
// owner can seat one role under another, and "" puts it back on top.
func TestTheOwnerDrawsARoleUnderAnotherRole(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	roleA, roleB := ownerMintsForm(t, r, "role a"), ownerMintsForm(t, r, "role b")
	top := presentedUnder(t, r, roleA)

	if err := setTopology(t, r, asOwner(), map[string]string{roleA: roleB}); err != nil {
		t.Fatalf("the owner drawing @roleA under @roleB: %v", err)
	}
	if up := presentedUnder(t, r, roleA); up != roleB {
		t.Fatalf("@roleA is drawn under %q, want @roleB %s", up, roleB)
	}

	aria, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, r, asOwner(), map[string]string{aria.FigaroID: roleA}); err != nil {
		t.Fatalf("the owner drawing an aria under @roleA: %v", err)
	}
	if up := presentedUnder(t, r, aria.FigaroID); up != roleA {
		t.Fatalf("the aria is drawn under %q, want @roleA %s", up, roleA)
	}

	if err := setTopology(t, r, asOwner(), map[string]string{roleA: ""}); err == nil || !strings.Contains(err.Error(), "cannot be drawn at the top") {
		t.Fatalf("drawing @roleA, now stored under @roleB, at the top = %v, want refused: the drawn tree has no top for it", err)
	}
	if up := presentedUnder(t, r, roleA); up != roleB {
		t.Fatalf("a refused draw moved @roleA under %q", up)
	}

	roleC := ownerMintsForm(t, r, "role c")
	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:*", "adopt:*")
	if err := setTopology(t, r, mallory(), map[string]string{roleC: roleB}); err != nil {
		t.Fatalf("a drawn-only move of @roleC under @roleB: %v", err)
	}
	if n, _ := r.backend.Node(roleC); n.Parent != top {
		t.Fatalf("a drawn-only move stored @roleC under %q", n.Parent)
	}
	if err := setTopology(t, r, mallory(), map[string]string{roleC: ""}); err != nil {
		t.Fatalf("drawing @roleC back at the top: %v", err)
	}
	if up := presentedUnder(t, r, roleC); up != top {
		t.Fatalf("@roleC drawn at the top sits under %q, want %q", up, top)
	}
}

// The kill floor covers a moved form as it covers an aria: set on both ends
// does not draw a form elsewhere.
func TestDrawingAFormNeedsKillOnIt(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	roleA, roleB := ownerMintsForm(t, r, "role a"), ownerMintsForm(t, r, "role b")
	was := presentedUnder(t, r, roleA)

	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:aria:"+roleB)
	if err := setTopology(t, r, mallory(), map[string]string{roleA: roleB}); err == nil || !strings.Contains(err.Error(), "kill on aria:"+roleA) {
		t.Fatalf("drawing @roleA with set but no kill on it = %v, want refused on kill", err)
	}
	if up := presentedUnder(t, r, roleA); up != was {
		t.Fatalf("a refused draw moved @roleA under %q", up)
	}

	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:aria:"+roleA, "adopt:aria:"+roleB)
	if err := setTopology(t, r, mallory(), map[string]string{roleA: roleB}); err != nil {
		t.Fatalf("drawing @roleA with kill on it: %v", err)
	}
	if up := presentedUnder(t, r, roleA); up != roleB {
		t.Fatalf("@roleA is drawn under %q, want @roleB %s", up, roleB)
	}
}

// A box draws the forms it minted, and no form it lacks kill on.
func TestABoxDrawsOnlyItsOwnForms(t *testing.T) {
	d := boxEndpointRig(t, "true")
	if err := wire.Install(d.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	owners := ownerMintsForm(t, d.grantRig, "the owner's")
	was := presentedUnder(t, d.grantRig, owners)

	c := d.dial(t)
	var made rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}}, &made); err != nil {
		t.Fatal(err)
	}
	mine := boxMintsForm(t, c, "the box's")

	if err := boxArrange(t, c, map[string]string{mine: made.FigaroID}); err != nil {
		t.Fatalf("the box drawing its own form under its aria: %v", err)
	}
	if up := presentedUnder(t, d.grantRig, mine); up != made.FigaroID {
		t.Fatalf("the box's form is drawn under %q, want %s", up, made.FigaroID)
	}

	if err := boxArrange(t, c, map[string]string{owners: made.FigaroID}); err == nil {
		t.Fatal("the box drew a form it has no kill on")
	}
	if up := presentedUnder(t, d.grantRig, owners); up != was {
		t.Fatalf("a refused box draw moved the owner's form under %q", up)
	}
}

func heldRow(t *testing.T, r grantRig, id string) rpc.FigaroInfoResponse {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodList, rpc.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range out.(rpc.ListResponse).Figaros {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("%s is not listed", id)
	return rpc.FigaroInfoResponse{}
}

// El Inspector XIV H1: a role drawn under its old holder survives the
// holder's lifetime. The role points at the successor, so only the role
// form itself in the delete set can hold the sweep.
func TestTheSweepHoldsARoleFormInItsDeleteSet(t *testing.T) {
	for _, isRole := range []bool{true, false} {
		r := newRigWith(t, guardPolicy)
		if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
			t.Fatal(err)
		}
		holder, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		successor, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		keys := map[string]json.RawMessage{"name": json.RawMessage(`"the role"`)}
		if isRole {
			keys["target-aria"], _ = json.Marshal(successor.FigaroID)
		}
		patch := form.Build(form.Snapshot{}, keys, nil)
		v, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: &patch})
		if err != nil {
			t.Fatal(err)
		}
		role := v.(rpc.FormCreateResponse).FormID
		if err := setTopology(t, r, asOwner(), map[string]string{role: holder.FigaroID}); err != nil {
			t.Fatalf("drawing the role under its old holder: %v", err)
		}
		if n, _ := r.backend.Node(role); n.Parent != holder.FigaroID {
			t.Fatalf("the role is stored under %q, want the holder %s, so its delete set would miss it", n.Parent, holder.FigaroID)
		}

		age(t, r.a, holder.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		idle(t, r.a, role)
		r.a.SweepLifetimesNow()
		r.a.SweepLifetimesNow()
		if !isRole {
			if !reaped(t, r.a, holder.FigaroID) {
				t.Fatal("canary: the sweep kept a holder with only a plain form under it, so the hold below proves nothing")
			}
			continue
		}
		if !alive(t, r.a, holder.FigaroID) {
			t.Fatal("the sweep took the old holder and the role form drawn under it")
		}
		if _, ok := r.backend.Node(role); !ok {
			t.Fatal("the sweep took the role form")
		}
		if row := heldRow(t, r, holder.FigaroID); row.HeldBy != role || !strings.Contains(row.HeldWhy, "role "+role+" is in the set") {
			t.Fatalf("the listing does not show the holder held by its role: by=%q why=%q", row.HeldBy, row.HeldWhy)
		}
	}
}

// El Inspector XIV nit (a): a kill -r that takes a role form says so, now or
// after its grace.
func TestAKillThatTakesARoleNamesIt(t *testing.T) {
	for _, now := range []bool{true, false} {
		r := newRigWith(t, guardPolicy)
		if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
			t.Fatal(err)
		}
		holder, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		successor, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		target, _ := json.Marshal(successor.FigaroID)
		patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": target}, nil)
		v, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: &patch})
		if err != nil {
			t.Fatal(err)
		}
		role := v.(rpc.FormCreateResponse).FormID
		if err := setTopology(t, r, asOwner(), map[string]string{role: holder.FigaroID}); err != nil {
			t.Fatal(err)
		}
		r.a.SweepLifetimesNow()

		out, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: holder.FigaroID, Recursive: true, Now: now, GraceMS: 3600_000})
		if err != nil {
			t.Fatalf("now=%t: kill -r of the holder: %v", now, err)
		}
		resp := out.(rpc.KillResponse)
		if len(resp.Roles) != 1 || resp.Roles[0] != role {
			t.Errorf("now=%t: the kill names roles %v, want [%s]", now, resp.Roles, role)
		}
		if now && !slices.Contains(resp.Removed, role) {
			t.Errorf("the kill --now removed %v, without the role %s", resp.Removed, role)
		}
	}
}

// El Inspector: kill -r --now A with role R1 drawn under A and role R2 drawn
// under R1 printed only 'took role @R2'. Both roles named one holder, and the
// target-keyed role index kept one role per target. Every role form in the
// set is named, whether or not the roles share a holder.
func TestAKillNamesARoleThatParentsAnotherRole(t *testing.T) {
	for _, shared := range []bool{false, true} {
		r := newRigWith(t, guardPolicy)
		if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
			t.Fatal(err)
		}
		holder, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		successor := func() string {
			s, err := r.create(asOwner(), "mock")
			if err != nil {
				t.Fatal(err)
			}
			return s.FigaroID
		}
		mintRole := func(holderOf string) string {
			target, _ := json.Marshal(holderOf)
			patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": target}, nil)
			v, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: &patch})
			if err != nil {
				t.Fatal(err)
			}
			return v.(rpc.FormCreateResponse).FormID
		}
		first := successor()
		second := first
		if !shared {
			second = successor()
		}
		r1, r2 := mintRole(first), mintRole(second)
		if err := setTopology(t, r, asOwner(), map[string]string{r1: holder.FigaroID, r2: r1}); err != nil {
			t.Fatal(err)
		}
		r.a.SweepLifetimesNow()
		if shared {
			forms, _ := r.backend.RoleForms()
			if forms[r1] != first || forms[r2] != first {
				t.Fatalf("the index %v does not hold both roles on %s: the shared case is not covered", forms, first)
			}
		}

		out, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: holder.FigaroID, Recursive: true, Now: true})
		if err != nil {
			t.Fatalf("shared=%t: kill -r --now of the holder: %v", shared, err)
		}
		resp := out.(rpc.KillResponse)
		got := slices.Sorted(slices.Values(resp.Roles))
		if want := slices.Sorted(slices.Values([]string{r1, r2})); !slices.Equal(got, want) {
			t.Errorf("shared=%t: the kill names roles %v, want %v (removed %v)", shared, got, want, resp.Removed)
		}
	}
}
