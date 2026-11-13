package angelus_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/figwal/xwal"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/provider"
)

// El Inspector XVI, A1: after a crash each figaro's leaf is repaired on its
// first use. A figaro whose repair fails is refused: never treated as not
// held (a door would then drop its claim and run the call as the door's own
// principal), never as holding no narrowing.

func mockProviders(string, provider.Knobs) (provider.Provider, error) {
	return &mockProviderForIntegration{}, nil
}

type crashed struct {
	r       grantRig
	f, t1   string
	failing *atomic.Bool
}

func crashedWithAFailingRepair(t *testing.T) crashed {
	t.Helper()
	storeDir := t.TempDir()
	before := newRigOnStore(t, storeDir, guardPolicy, "", mockProviders)
	target, err := before.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	f, err := before.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	_ = before.backend.Close()
	if err := os.WriteFile(filepath.Join(storeDir, ".unclean"), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failing := &atomic.Bool{}
	failing.Store(true)
	t.Cleanup(xwal.FailLeafRepairsForTest(func(trunk string) error {
		if trunk == f.FigaroID && failing.Load() {
			return errors.New("injected I/O error")
		}
		return nil
	}))
	return crashed{r: newRigOnStore(t, storeDir, guardPolicy, "", mockProviders), f: f.FigaroID, t1: target.FigaroID, failing: failing}
}

func (c crashed) readT1(t *testing.T, ctx context.Context, claim string) error {
	t.Helper()
	raw := withAria(t, c.t1, rpc.ReadRequest{FigaroID: c.t1})
	if claim != "" {
		raw = claimed(t, claim, raw)
	}
	_, err := c.r.h[rpc.MethodRead](ctx, raw)
	return err
}

func TestAFigaroWhoseRepairFailedIsRefusedAtEveryDoor(t *testing.T) {
	c := crashedWithAFailingRepair(t)
	if err := c.readT1(t, asOwner(), ""); err != nil {
		t.Fatalf("control: the owner with no claim cannot read: %v", err)
	}
	for _, d := range []authz.Door{authz.DoorLocal, authz.DoorNode} {
		if err := c.readT1(t, authz.WithDoor(asOwner(), d), ""); err != nil {
			t.Fatalf("control: the %s door's own principal cannot read: %v", d, err)
		}
		if err := c.readT1(t, authz.WithDoor(asOwner(), d), c.f); err == nil {
			t.Errorf("(a) %s door: a call claiming a figaro whose repair failed ran as the door's principal", d)
		}
	}
	if err := c.readT1(t, authz.WithDoor(asOwner(), authz.DoorInProcess), c.f); err == nil {
		t.Error("(b) in process: a call claiming a figaro whose repair failed ran")
	}
	if _, err := c.r.h[rpc.MethodRead](asOwner(), withAria(t, c.f, rpc.ReadRequest{FigaroID: c.f})); err == nil || !strings.Contains(err.Error(), "injected I/O error") {
		t.Errorf("reading the figaro itself: %v, want the repair error", err)
	}
}

func TestAFigaroWhoseRepairFailedGainsNoChild(t *testing.T) {
	c := crashedWithAFailingRepair(t)
	if _, err := c.r.h[rpc.MethodFork](asOwner(), withAria(t, c.f, rpc.ForkRequest{FigaroID: c.f})); err == nil {
		t.Error("(c) a figaro whose repair failed was forked")
	}
	for _, n := range c.r.backend.Nodes() {
		if n.Parent == c.f {
			t.Errorf("(c) %s sits under a figaro whose repair failed, and would read its scopes", n.ID)
		}
	}
}

func TestAFigaroWhoseRepairFailedIsStillListedAndServesOnceTheRepairSucceeds(t *testing.T) {
	c := crashedWithAFailingRepair(t)
	out, err := c.r.call(asOwner(), rpc.MethodList, rpc.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, row := range out.(rpc.ListResponse).Figaros {
		listed = listed || row.ID == c.f
	}
	if !listed {
		t.Error("ls dropped a figaro whose repair failed")
	}
	if _, ok := c.r.backend.Node(c.f); !ok {
		t.Error("the index no longer holds a figaro whose repair failed")
	}
	if err := c.readT1(t, authz.WithDoor(asOwner(), authz.DoorLocal), c.f); err == nil {
		t.Fatal("refused before the repair succeeds")
	}
	c.failing.Store(false)
	if err := c.readT1(t, authz.WithDoor(asOwner(), authz.DoorLocal), c.f); err != nil {
		t.Errorf("(d) once the repair succeeds the claim is served: %v", err)
	}
	if _, err := c.r.h[rpc.MethodRead](asOwner(), withAria(t, c.f, rpc.ReadRequest{FigaroID: c.f})); err != nil {
		t.Errorf("(d) the figaro itself reads after its repair: %v", err)
	}
}
