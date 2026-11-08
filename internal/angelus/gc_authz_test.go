package angelus_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

const lentKillPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[roles.lent]
grants = [{ verbs = ["read", "listen", "send", "fork", "set", "kill"], on = ["unboxed"] }]

[[bind]]
role = "lent"
principals = ["troupe:kelliher"]
`

func gcPlanAs(r grantRig, ctx context.Context, sel rpc.GCSelector) (rpc.GCPlanResponse, error) {
	r.a.SeedRolesNow()
	out, err := r.call(ctx, rpc.MethodGCPlan, rpc.GCPlanRequest{Selector: sel})
	if err != nil {
		return rpc.GCPlanResponse{}, err
	}
	return out.(rpc.GCPlanResponse), nil
}

// El Inspector XI, condition 3: gc is a mass kill. Unboxed it is the
// owner's; the troupe's lent kill grant reaches single figaros, never gc.
func TestANonOwnerHoldingKillIsRefusedGC(t *testing.T) {
	r := newRigWith(t, lentKillPolicy)
	made, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	idleFor(t, r.a, made.FigaroID, 40*24*time.Hour)
	troupe := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalKind(authz.KindTroupe), ID: "kelliher"})
	victim, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(troupe, rpc.MethodKill, rpc.KillRequest{FigaroID: victim.FigaroID}); err != nil {
		t.Fatalf("the lent grant does not reach kill, so this test proves nothing: %v", err)
	}
	sel := rpc.GCSelector{IdleMS: month.Milliseconds()}
	if _, err := gcPlanAs(r, troupe, sel); err == nil {
		t.Fatal("troupe:kelliher planned a gc")
	}
	plan, err := gcPlanAs(r, asOwner(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("the owner's plan chose %+v", plan.Candidates)
	}
	if _, err := r.call(troupe, rpc.MethodGCApply, rpc.GCApplyRequest{Run: plan.Run}); err == nil {
		t.Fatal("troupe:kelliher applied the owner's plan")
	}
	if m, _ := r.backend.Meta(made.FigaroID); m == nil || m.ReapedAtMS != 0 {
		t.Fatal("a refused apply reaped")
	}
	if _, err := r.call(asOwner(), rpc.MethodGCApply, rpc.GCApplyRequest{Run: plan.Run}); err != nil {
		t.Fatalf("the owner's own apply after a refused one: %v", err)
	}
}

func TestAGuestIsRefusedGC(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	guest := as("user", "guest")
	if _, err := r.create(guest, "mock"); err != nil {
		t.Fatal(err)
	}
	if _, err := gcPlanAs(r, guest, rpc.GCSelector{Empty: true}); err == nil {
		t.Fatal("a guest planned a gc")
	}
}

func TestAPlanBelongsToItsCaller(t *testing.T) {
	r := newRigWith(t, lentKillPolicy)
	plan, err := gcPlanAs(r, asOwner(), rpc.GCSelector{Empty: true})
	if err != nil {
		t.Fatal(err)
	}
	other := as("user", strconv.Itoa(os.Getuid()+1))
	if _, err := r.call(other, rpc.MethodGCApply, rpc.GCApplyRequest{Run: plan.Run}); err == nil {
		t.Fatal("another principal applied the owner's plan")
	}
}

// A boxed caller plans and applies only its own box's figaros; the owner
// sees box figaros only by naming the box.
func TestABoxGCsOnlyItsBox(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{inBox.FigaroID, outside.FigaroID} {
		idleFor(t, d.a, id, 40*24*time.Hour)
	}
	d.a.SeedRolesNow()
	sel := rpc.GCSelector{IdleMS: month.Milliseconds()}
	c := d.dial(t)
	var plan rpc.GCPlanResponse
	if err := c.Call(context.Background(), rpc.MethodGCPlan, rpc.GCPlanRequest{Selector: sel}, &plan); err != nil {
		t.Fatal(err)
	}
	if got := chosen(&plan); len(got) != 1 || got[0] != inBox.FigaroID {
		t.Fatalf("the box chose %v, want only %s", got, inBox.FigaroID)
	}
	for _, k := range plan.Kept {
		if k.ID == outside.FigaroID {
			t.Fatal("the box's plan named an aria outside it")
		}
	}
	if err := c.Call(context.Background(), rpc.MethodGCPlan, rpc.GCPlanRequest{Selector: rpc.GCSelector{Box: "other"}}, nil); err == nil {
		t.Fatal("a box planned in another box")
	}

	owned, err := gcPlanAs(d.grantRig, asOwner(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if got := chosen(&owned); len(got) != 1 || got[0] != outside.FigaroID {
		t.Fatalf("the owner without --box chose %v", got)
	}
	named, err := gcPlanAs(d.grantRig, asOwner(), rpc.GCSelector{IdleMS: month.Milliseconds(), Box: "judge"})
	if err != nil {
		t.Fatal(err)
	}
	if got := chosen(&named); len(got) != 1 || got[0] != inBox.FigaroID {
		t.Fatalf("the owner with --box judge chose %v", got)
	}

	var res rpc.GCApplyResponse
	if err := c.Call(context.Background(), rpc.MethodGCApply, rpc.GCApplyRequest{Run: plan.Run}, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Reaped) != 1 {
		t.Fatalf("the box's apply reaped %+v", res)
	}
	if m, _ := d.backend.Meta(inBox.FigaroID); m == nil || m.ReapedAtMS == 0 {
		t.Fatal("the box's own aria was not reaped")
	}
	if m, _ := d.backend.Meta(outside.FigaroID); m == nil || m.ReapedAtMS != 0 {
		t.Fatal("the box's apply reaped an aria outside it")
	}
}

// El Inspector XI, condition 4: system.keep is owner-held, so neither a box
// nor a guest can set it or take it away.
func TestSystemKeepIsOwnerHeld(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	if err := setKey(t, c, inBox.FigaroID, store.SystemKeepKey, "true"); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Fatalf("the box set system.keep: %v", err)
	}
	if _, err := d.call(asOwner(), rpc.MethodSet, withAria(t, inBox.FigaroID, rpc.SetRequest{Patch: *keyPatch(store.SystemKeepKey, true)})); err != nil {
		t.Fatal(err)
	}
	drop := form.Build(form.Snapshot{}, nil, []string{store.SystemKeepKey})
	if err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: drop}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Fatalf("the box removed system.keep: %v", err)
	}

	r := newRigWith(t, doorPolicy)
	guest := as("user", "guest")
	mine, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(guest, rpc.MethodSet, withAria(t, mine.FigaroID, rpc.SetRequest{Patch: *keyPatch(store.SystemKeepKey, true)})); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Fatalf("a guest set system.keep on its own aria: %v", err)
	}
}
