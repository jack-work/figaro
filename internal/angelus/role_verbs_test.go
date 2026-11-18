package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func (r licenseRig) castAs(t *testing.T, figaro, target, role string) error {
	t.Helper()
	raw := withAria(t, target, rpc.CastRequest{FormID: role})
	if figaro != "" {
		raw = claimed(t, figaro, raw)
	}
	_, err := r.h[rpc.MethodCast](localOwner(), raw)
	return err
}

func (r licenseRig) scope(t *testing.T, id string, scopes ...string) {
	t.Helper()
	if _, err := r.backend.ApplyForm(id, *heldPatch(t, map[string]any{form.ScopesKey: scopes})); err != nil {
		t.Fatal(err)
	}
}

func (r licenseRig) role(t *testing.T, holder string) string {
	t.Helper()
	keys := map[string]json.RawMessage{"name": json.RawMessage(`"seat"`)}
	if holder != "" {
		keys["target-aria"], _ = json.Marshal(holder)
	}
	id, _, err := r.backend.CreateForm("", form.Build(form.Snapshot{}, keys, nil))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The v0.74.1 hole: a role nobody holds could be taken by any figaro that may
// write the role form. Taking an empty seat now needs cast written down.
func TestAnEmptySeatIsTakenOnlyWithAWrittenCastGrant(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	role := r.role(t, "")
	err := r.castAs(t, r.x, r.x, role)
	if err == nil || !strings.Contains(err.Error(), "held by nobody") {
		t.Fatalf("an unscoped figaro took an empty seat: %v", err)
	}
	r.scope(t, r.x, "*:*", "cast:"+role)
	if err := r.castAs(t, r.x, r.x, role); err != nil {
		t.Fatalf("a figaro with cast written took no empty seat: %v", err)
	}
	other := r.role(t, "")
	if err := r.castAs(t, "", r.root, other); err != nil {
		t.Fatalf("the owner could not fill an empty seat: %v", err)
	}
}

func TestACastNeedsTheCastVerbOnTheRole(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	role := r.role(t, r.x)
	r.scope(t, r.x, "read:*", "send:*", "fork:*", "set:*", "delete:*", "create:*")
	err := r.castAs(t, r.x, r.x, role)
	if err == nil || !strings.Contains(err.Error(), "cast") {
		t.Fatalf("a figaro without cast was cast: %v", err)
	}
	r.scope(t, r.x, "read:*", "send:*", "fork:*", "set:*", "delete:*", "create:*", "cast:"+role)
	if err := r.castAs(t, r.x, r.x, role); err != nil {
		t.Fatalf("cast:%s did not reach its role: %v", role, err)
	}
}

func TestASucceedNeedsTheSucceedVerbOnTheSeat(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	role := r.role(t, r.x)
	r.scope(t, r.x, "read:*", "send:*", "fork:*", "set:*", "delete:*", "create:*", "kill:*", "adopt:*", "use:*", "cast:*")
	fork := func() error {
		_, err := r.h[rpc.MethodFork](localOwner(), claimed(t, r.x, mustJSON(t, rpc.ForkRequest{FigaroID: r.x, Seat: role, Abdicates: true})))
		return err
	}
	if err := fork(); err == nil || !strings.Contains(err.Error(), "succeed") {
		t.Fatalf("a figaro without succeed forked its seat's successor: %v", err)
	}
	seat := func() error {
		_, err := r.h[rpc.MethodSeat](localOwner(), claimed(t, r.x, withAria(t, r.x, rpc.SeatRequest{FigaroID: r.x, Successor: r.root})))
		return err
	}
	if err := seat(); err == nil || !strings.Contains(err.Error(), "succeed") {
		t.Fatalf("a figaro without succeed seated a successor: %v", err)
	}
	r.scope(t, r.x, "read:*", "send:*", "fork:*", "set:*", "delete:*", "create:*", "kill:*", "adopt:*", "use:*", "cast:*", "succeed:"+role)
	if err := fork(); err != nil {
		t.Fatalf("succeed:%s did not reach its seat: %v", role, err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWithKeyGrantsOffAnEmptySeatIsTakenAndCounted(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, false)
	role := r.role(t, "")
	if err := r.castAs(t, r.x, r.x, role); err != nil {
		t.Fatalf("an empty seat was refused with enforce_key_grants off: %v", err)
	}
	if n := r.a.Policies.WouldRefused(authz.KeyGrants); n != 1 {
		t.Fatalf("would-refuse counter = %d, want 1", n)
	}
}
