package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

const mockPoolConfig = `
[slot.pools.groups]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
say_url = "https://broker.example/groups/say"
slots = ["slot0"]
`

func leasedRig(t *testing.T) (grantRig, string) {
	t.Helper()
	r := newRigConfigured(t, grantPolicy, mockPoolConfig)
	box, err := r.a.Settings.LeaseBox("groups", "slot0", 1)
	if err != nil {
		t.Fatal(err)
	}
	outfit := "[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \"" + box + "\"\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.policy), "outfits", "boxed.toml"), []byte(outfit), 0o600); err != nil {
		t.Fatal(err)
	}
	return r, box
}

// A leased figaro is born into a box with no socket, keeps no host path on
// its form, and a kill of it deletes at once whatever grace is asked. With no
// answer from the leased-box table the kill would keep a released group's
// text restorable for the daemon's three days.
func TestAFigaroInALeasedBoxIsBornSocketlessAndKilledWithNoGrace(t *testing.T) {
	r, box := leasedRig(t)
	for name, grace := range map[string]time.Duration{"no grace asked": 0, "a month asked": 30 * 24 * time.Hour} {
		leased, err := r.create(asOwner(), "boxed")
		if err != nil {
			t.Fatalf("%s: a figaro in a leased box was not born: %v", name, err)
		}
		if got := r.sandboxOf(t, leased.FigaroID); got != box {
			t.Fatalf("%s: born in sandbox %q, want %q", name, got, box)
		}
		snap, err := r.backend.FormState(leased.FigaroID)
		if err != nil {
			t.Fatal(err)
		}
		if cwd := snap.Lookup("system.cwd"); cwd != nil && *cwd != "" {
			t.Errorf("%s: a leased figaro's form carries the host path %q", name, *cwd)
		}
		if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: leased.FigaroID, GraceMS: grace.Milliseconds()}); err != nil {
			t.Fatal(err)
		}
		if ariaExists(r.a, leased.FigaroID) {
			t.Errorf("%s: a killed figaro of a leased box is still in the store", name)
		}
	}
	outside, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: outside.FigaroID}); err != nil {
		t.Fatal(err)
	}
	if !reaped(t, r.a, outside.FigaroID) {
		t.Error("a figaro outside the leased box was deleted at once: the test cannot tell the box's grace from no grace at all")
	}
}

// The handlers take a freshly loaded config on every reload, and the leases
// are in no file.
func TestALiveLeaseIsStillABoxAfterTheHandlersReload(t *testing.T) {
	r, box := leasedRig(t)
	if _, err := r.h[rpc.MethodConfigure](asOwner(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	leased, err := r.create(asOwner(), "boxed")
	if err != nil {
		if strings.Contains(err.Error(), "unknown provider") {
			t.Fatalf("the reload forgot the lease: %v", err)
		}
		t.Fatal(err)
	}
	if got := r.sandboxOf(t, leased.FigaroID); got != box {
		t.Fatalf("born in sandbox %q, want %q", got, box)
	}
}
