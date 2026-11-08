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
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.lent]
grants = [{ verbs = ["create", "read", "listen", "send", "fork", "set"], on = ["*"] }, { verbs = ["use"], on = ["sandbox:local"] }]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "lent"
principals = ["user:guest"]
`

// El Inspector XI: system.ttl and system.reap_if_empty decide when the sweep
// deletes a node and its whole subtree. A non-owner unboxed principal with set
// on the owner's aria (the shape of a lent troupe grant) could put a short ttl
// on it and have its history deleted, or mark an empty one for reaping. Both
// are owner-held: the guest is refused by set, a prompt's patch, a fork and a
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
