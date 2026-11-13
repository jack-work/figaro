package angelus_test

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/workerpool"
	"github.com/jack-work/jkauthz"
)

func provisionedRig(t *testing.T) (grantRig, string) {
	t.Helper()
	r, box := leasedRig(t)
	workers, err := angelus.OpenWorkerPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.a.Workers = workers
	r.a.Policies.SetLeases(workers)
	if err := workers.Ledger.Begin("groups", "slot0", "chat:10", 1, brokerHolder); err != nil {
		t.Fatal(err)
	}
	return r, box
}

func asProvisionerServing(box string) context.Context {
	return authz.WithSubject(context.Background(), authz.ProvisionerServing(box))
}

func (r grantRig) note(t *testing.T, ctx context.Context, id string) error {
	t.Helper()
	_, err := r.h[rpc.MethodSet](ctx, withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"note": "from the provisioner"})}))
	return err
}

func (r grantRig) killNow(ctx context.Context, id string) error {
	_, err := r.call(ctx, rpc.MethodKill, rpc.KillRequest{FigaroID: id, Now: true})
	return err
}

// The ledger row is the grant's lifetime, and the box name is how the
// grant finds its row. A released generation of a worker stays released
// while the next generation of the same worker is minting.
func TestALeaseIsInForceFromMintingUntilItIsReleased(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, fourWorkerPoolConfig)
	dir := t.TempDir()
	before := ledgerIn(t, dir)
	leaseTo(t, before, "slot0", "chat:10", 1, workerpool.Live)
	leaseTo(t, before, "slot1", "chat:11", 1, workerpool.Minting)
	leaseTo(t, before, "slot2", "chat:12", 4, workerpool.Releasing)
	leaseTo(t, before, "slot3", "chat:13", 2, workerpool.Released)
	leaseTo(t, before, "slot3", "chat:14", 3, workerpool.Minting)
	workers, err := angelus.OpenWorkerPools(dir, r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for box, want := range map[string]bool{
		"slot0-g1": true, "slot1-g1": true, "slot2-g4": true, "slot3-g3": true,
		"slot3-g2": false, "slot0-g2": false, "slot2-g3": false, "slot0": false, "slot0-g": false, "groups": false, "chat:10": false, "": false,
	} {
		if got := workers.InForce(box); got != want {
			t.Errorf("lease %q in force: %v, want %v", box, got, want)
		}
	}
}

// The grant through the daemon's own guard and handlers, with the ledger
// as the fact behind it. Serving its lease the provisioner writes and
// deletes that lease's figaros; it bears nothing through figaro.create,
// reads nothing, and touches no figaro of the owner's. Serving a lease
// that is not in force, or none, it does not reach even the leased
// figaro, and once the row is released the grant is gone.
func TestThroughTheHandlersTheProvisionerReachesOnlyItsLeasesFigaros(t *testing.T) {
	r, box := provisionedRig(t)
	born := func(outfit string) string {
		t.Helper()
		resp, err := r.create(asOwner(), outfit)
		if err != nil {
			t.Fatal(err)
		}
		return resp.FigaroID
	}
	leased, kept, owners := born("boxed"), born("boxed"), born("mock")
	serving := asProvisionerServing(box)

	for _, outfit := range []string{"boxed", "mock"} {
		if resp, err := r.create(serving, outfit); err == nil {
			t.Errorf("%s with outfit %s was served to the provisioner: it bore %s", rpc.MethodCreate, outfit, resp.FigaroID)
		}
	}
	if err := r.note(t, serving, owners); err == nil {
		t.Error("the provisioner wrote the owner's figaro")
	}
	if err := r.killNow(serving, owners); err == nil || !ariaExists(r.a, owners) {
		t.Errorf("the provisioner deleted the owner's figaro (%v)", err)
	}
	if _, err := r.h[rpc.MethodForm](serving, withAria(t, leased, struct{}{})); err == nil {
		t.Error("the provisioner read the form of a figaro it may only write and delete")
	}
	for name, ctx := range map[string]context.Context{
		"the slot's next generation": asProvisionerServing("slot0-g2"),
		"no lease":                   authz.WithSubject(context.Background(), jkauthz.Subject{Principal: authz.ProvisionerPrincipal()}),
		"this lease and another":     authz.WithSubject(context.Background(), jkauthz.Subject{Principal: authz.ProvisionerPrincipal(), Groups: []string{box + "@" + authz.PoolScope, "slot0-g2@" + authz.PoolScope}}),
	} {
		if err := r.note(t, ctx, leased); err == nil {
			t.Errorf("serving %s the provisioner wrote the leased figaro", name)
		}
		if err := r.killNow(ctx, leased); err == nil || !ariaExists(r.a, leased) {
			t.Errorf("serving %s the provisioner deleted the leased figaro (%v)", name, err)
		}
	}

	if err := r.note(t, serving, leased); err != nil {
		t.Errorf("the provisioner serving %s could not write its figaro: %v", box, err)
	}
	if _, err := r.a.Workers.Ledger.Release("groups", "chat:10", 1); err != nil {
		t.Fatal(err)
	}
	if err := r.killNow(serving, leased); err != nil || ariaExists(r.a, leased) {
		t.Errorf("with the lease releasing the provisioner could not delete its figaro: %v", err)
	}
	if err := r.a.Workers.Ledger.Released("groups", "chat:10", 1); err != nil {
		t.Fatal(err)
	}
	if err := r.killNow(serving, kept); err == nil || !ariaExists(r.a, kept) {
		t.Errorf("after the release the provisioner still deleted a figaro (%v)", err)
	}
	if err := r.note(t, serving, kept); err == nil {
		t.Error("after the release the provisioner still wrote a figaro")
	}
}
