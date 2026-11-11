package angelus_test

import (
	"bytes"
	"context"
	"encoding/json"
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
permissions = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[roles.lent]
permissions = ["read:unboxed", "listen:unboxed", "send:unboxed", "fork:unboxed", "set:unboxed", "kill:unboxed"]

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

// El Inspector XII, change 2: the reap keys schedule a purge with no kill
// and no grace, so no caller writes them by a set, the owner included. Only
// kill, gc and reaped restore write them, through their own paths.
func TestNoCallerSetsTheReapKeys(t *testing.T) {
	reapKeys := map[string]any{
		store.SystemReapedKey:      "x",
		store.SystemReapedUntilKey: "2020-01-01T00:00:00Z",
		store.SystemReapedByKey:    "gc:forged",
	}
	r := newRigWith(t, lentKillPolicy)
	made, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	troupe := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalKind(authz.KindTroupe), ID: "kelliher"})
	if _, err := r.call(troupe, rpc.MethodSet, withAria(t, made.FigaroID, rpc.SetRequest{Patch: *mantraPatch("lent set works")})); err != nil {
		t.Fatalf("the lent set grant does not reach set, so this test proves nothing: %v", err)
	}
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	for key, val := range reapKeys {
		set := rpc.SetRequest{Patch: *keyPatch(key, val)}
		for who, ctx := range map[string]context.Context{"owner": asOwner(), "troupe": troupe} {
			if _, err := r.call(ctx, rpc.MethodSet, withAria(t, made.FigaroID, set)); err == nil {
				t.Errorf("%s set %s", who, key)
			}
		}
		if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, made.FigaroID, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{key})})); err == nil {
			t.Errorf("the owner unset %s", key)
		}
		nested := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system": json.RawMessage(`{"` + strings.TrimPrefix(key, "system.") + `":"x"}`)}, nil)
		if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, made.FigaroID, rpc.SetRequest{Patch: nested})); err == nil {
			t.Errorf("the owner set %s inside a whole system object", key)
		}
		if _, err := r.call(asOwner(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd, Patch: keyPatch(key, val)}); err == nil {
			t.Errorf("the owner set %s at birth", key)
		}
		if _, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: made.FigaroID, Patch: keyPatch(key, val)}); err == nil {
			t.Errorf("the owner set %s on a fork", key)
		}
		if err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, set, nil); err == nil {
			t.Errorf("the box set %s", key)
		}
	}
	if m, _ := r.backend.Meta(made.FigaroID); m == nil || m.ReapedAtMS != 0 {
		t.Fatalf("a refused set reaped: %+v", m)
	}

	if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: made.FigaroID}); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.backend.Meta(made.FigaroID); m == nil || m.ReapedAtMS == 0 || m.ReapedUntilMS == 0 {
		t.Fatalf("kill did not write the reap keys: %+v", m)
	}
	if _, err := r.call(asOwner(), rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: made.FigaroID}); err != nil {
		t.Fatal(err)
	}
	snap, err := r.backend.FormState(made.FigaroID)
	if err != nil {
		t.Fatal(err)
	}
	for key := range reapKeys {
		if snap.Lookup(key) != nil {
			t.Errorf("restore left %s", key)
		}
	}
}

func TestRestoreKeepIsTheOwners(t *testing.T) {
	r := newRigWith(t, lentKillPolicy)
	made, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, made.FigaroID, rpc.SetRequest{Patch: *keyPatch(store.SystemTTLKey, "30d")})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: made.FigaroID}); err != nil {
		t.Fatal(err)
	}
	troupe := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalKind(authz.KindTroupe), ID: "kelliher"})
	if _, err := r.call(troupe, rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: made.FigaroID, Keep: true}); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Fatalf("a lent set restored with --keep, removing the owner's ttl: %v", err)
	}
	if snap, _ := r.backend.FormState(made.FigaroID); snap.Lookup(store.SystemTTLKey) == nil {
		t.Fatal("the refused --keep dropped system.ttl")
	}
	if _, err := r.call(troupe, rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: made.FigaroID}); err != nil {
		t.Fatalf("a lent set restore without --keep: %v", err)
	}
}

// JSON lets a key arrive escaped; the owner's fast path must not let
// "system.re\u0061ped_until" through as a key it never saw.
func TestAnEscapedReapKeyIsRefusedFromTheOwner(t *testing.T) {
	r := newRigWith(t, lentKillPolicy)
	made, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range reapKeyNames {
		raw, err := json.Marshal(rpc.SetRequest{Patch: *keyPatch(key, "2020-01-01T00:00:00Z")})
		if err != nil {
			t.Fatal(err)
		}
		escaped := bytes.ReplaceAll(raw, []byte("reaped"), []byte(`re\u0061ped`))
		if bytes.Equal(escaped, raw) {
			t.Fatalf("the patch for %s never spells the key: %s", key, raw)
		}
		params, err := rpc.WithAria(escaped, made.FigaroID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.h[rpc.MethodSet](asOwner(), params); err == nil {
			t.Errorf("the owner set %s spelled with an escape", key)
		}
	}
	if m, _ := r.backend.Meta(made.FigaroID); m == nil || m.ReapedAtMS != 0 || m.ReapedUntilMS != 0 {
		t.Fatalf("an escaped key landed: %+v", m)
	}
}

var reapKeyNames = []string{store.SystemReapedKey, store.SystemReapedUntilKey, store.SystemReapedByKey}
