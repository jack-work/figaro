package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

const lifetimeLentPolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.lent]
permissions = ["create:*", "read:*", "listen:*", "send:*", "fork:*", "set:*", "use:sandbox:local"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "lent"
principals = ["user:guest"]
`

// El Inspector XI: system.ttl and system.reap_if_empty decide when the sweep
// deletes a node and its whole subtree, and system.keepalive and
// system.retired decide whether a ttl the owner set waits for the aria's use
// or reaps at the deadline. A non-owner unboxed principal with set on the
// owner's aria (the shape of a lent troupe grant) could put a short ttl on it
// and have its history deleted, mark an empty one for reaping, or bring an
// owner's ttl forward. All four are owner-held: the guest is refused by set, a prompt's patch, a fork and a
// birth, while the same guest may still set an open key, so the refusal is
// the key's and not a missing grant.
func TestAnUnboxedGuestCannotGiveTheOwnersAriaALifetime(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	id := resp.FigaroID

	if _, err := r.h[rpc.MethodSet](guest, withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"note": "mine"})})); err != nil {
		t.Fatalf("the guest cannot set an open key, so this test proves nothing: %v", err)
	}

	for key, val := range map[string]any{
		store.SystemTTLKey:         "1m",
		store.SystemReapIfEmptyKey: true,
		store.SystemKeepaliveKey:   "0",
		store.SystemRetiredKey:     "2026-11-07T00:00:00Z",
		store.SystemRetirementKey:  map[string]any{"context_threshold": "1%", "ttl": "1m"},
	} {
		if _, held := form.OwnerHeld(key); !held {
			t.Errorf("%s is not owner-held", key)
		}
		p := heldPatch(t, map[string]any{key: val})
		owner := func(what string, err error) {
			t.Helper()
			if err == nil || !strings.Contains(err.Error(), "owner-held") {
				t.Errorf("%s %s: %v", what, key, err)
			}
		}
		_, err := r.h[rpc.MethodSet](guest, withAria(t, id, rpc.SetRequest{Patch: *p}))
		owner("set", err)
		_, err = r.h[rpc.MethodQua](guest, withAria(t, id, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Patch: p}}))
		owner("prompt patch", err)
		raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: id, Patch: p})
		_, err = r.h[rpc.MethodFork](guest, raw)
		owner("fork patch", err)
		raw, _ = json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd, Patch: p})
		_, err = r.h[rpc.MethodCreate](guest, raw)
		owner("birth patch", err)
		if v, ok := keyOn(t, r, id, key); ok {
			t.Errorf("%s = %s after the guest's attempts", key, v)
		}

		if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *p})); err != nil {
			t.Errorf("the owner cannot set %s: %v", key, err)
		}
	}
}

// figaro retire works inside a box on the box's own arias, so system.retired
// stays writable for a box there, while every other non-owner is refused.
func TestABoxStillRetiresItsOwnAria(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	retired := heldPatch(t, map[string]any{store.SystemRetiredKey: "2026-11-07T00:00:00Z"})
	if err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: *retired}, nil); err != nil {
		t.Fatalf("the box could not retire its own aria: %v", err)
	}
	if _, ok := keyOn(t, d.grantRig, inBox.FigaroID, store.SystemRetiredKey); !ok {
		t.Fatal("the box's retire did not land")
	}
	for key, val := range map[string]any{store.SystemKeepaliveKey: "0", store.SystemTTLKey: "1m"} {
		err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{key: val})}, nil)
		if err == nil || !strings.Contains(err.Error(), "may not set") {
			t.Errorf("the box set %s: %v", key, err)
		}
	}
}

// El Inspector D7: system.succession_failed is what holds a refused succession
// off (the worker pool's per-day bound is a refusal), and system.retirement.page
// says who hears of it. Anyone but the owner who could unset the first or
// repoint the second would walk through the bound, or silence it.
func TestOnlyTheOwnerClearsAFailedSuccessionOrMovesItsPage(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	id := resp.FigaroID
	if _, held := form.OwnerHeld(store.SystemSuccessionFailedKey); !held {
		t.Fatalf("%s is not owner-held", store.SystemSuccessionFailedKey)
	}
	seed := heldPatch(t, map[string]any{
		store.SystemSuccessionFailedKey: "4 successions a day, all spent",
		store.SystemRetirementKey:       map[string]any{"context_threshold": 1000, "action": "succeed", "page": "@pool"},
	})
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *seed})); err != nil {
		t.Fatalf("the owner cannot record a failed succession: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](guest, withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"note": "mine"})})); err != nil {
		t.Fatalf("the guest cannot set an open key, so this test proves nothing: %v", err)
	}

	unset := form.Build(form.Snapshot{}, nil, []string{store.SystemSuccessionFailedKey})
	for what, p := range map[string]form.Patch{
		"unset succession_failed":   unset,
		"rewrite succession_failed": *heldPatch(t, map[string]any{store.SystemSuccessionFailedKey: ""}),
		"repoint page":              *heldPatch(t, map[string]any{store.SystemRetirementKey + ".page": "@nobody"}),
		"drop page":                 form.Build(form.Snapshot{}, nil, []string{store.SystemRetirementKey + ".page"}),
	} {
		_, err := r.h[rpc.MethodSet](guest, withAria(t, id, rpc.SetRequest{Patch: p}))
		if err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("%s by the guest: %v", what, err)
		}
		_, err = r.h[rpc.MethodQua](guest, withAria(t, id, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Patch: &p}}))
		if err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("%s by the guest's prompt patch: %v", what, err)
		}
	}
	if v, ok := keyOn(t, r, id, store.SystemSuccessionFailedKey); !ok || !strings.Contains(string(v), "all spent") {
		t.Errorf("%s = %q after the guest's attempts", store.SystemSuccessionFailedKey, v)
	}
	if v, _ := keyOn(t, r, id, store.SystemRetirementKey); !strings.Contains(string(v), "@pool") {
		t.Errorf("%s = %s after the guest's attempts", store.SystemRetirementKey, v)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: unset})); err != nil {
		t.Errorf("the owner cannot clear a failed succession: %v", err)
	}
}
