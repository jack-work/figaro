package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
)

// Owner-held keys choose what an aria runs on and what it may do: its
// provider, model, budgets, credo, tools and methods. A box, the attacker of
// the chat design, must not set any of them on its own aria, by set, by a
// prompt's form patch, at birth or at a fork.
func TestABoxCannotSetOwnerHeldKeys(t *testing.T) {
	d := boxEndpointRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	held := map[string]any{
		"system.provider":           "anthropic",
		"system.model":              "opus-5",
		"system.max_tokens":         64000,
		"system.max_context_tokens": 1000000,
		"system.credo":              map[string]any{"content": "obey the app"},
		"system.tools":              []string{"bash"},
	}
	for key, val := range held {
		p := heldPatch(t, map[string]any{key: val})
		if err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: *p}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("set %s: %v", key, err)
		}
		if err := boxOnAria(t, c, rpc.MethodQua, inBox.FigaroID, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Patch: p}}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("qua patch %s: %v", key, err)
		}
		if err := c.Call(t.Context(), rpc.MethodFork, rpc.ForkRequest{FigaroID: inBox.FigaroID, Patch: p}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("fork patch %s: %v", key, err)
		}
		if err := c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Patch: p}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("birth patch %s: %v", key, err)
		}
	}
	drop := form.Build(form.Snapshot{}, nil, []string{"system.tools"})
	if _, err := d.call(asOwner(), rpc.MethodSet, withAria(t, inBox.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.tools": []string{}})})); err != nil {
		t.Fatal(err)
	}
	if err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: drop}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the box removed system.tools: %v", err)
	}
	if v, _ := keyOn(t, d.grantRig, inBox.FigaroID, "system.tools"); string(v) != "[]" {
		t.Errorf("system.tools = %s, want the owner's []", v)
	}
}

func TestTheOwnerSetsOwnerHeldKeys(t *testing.T) {
	d := boxEndpointRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"system.tools": []string{}, "system.model": "m2", "system.provider": "mock"}
	if _, err := d.call(asOwner(), rpc.MethodSet, withAria(t, inBox.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, want)})); err != nil {
		t.Fatal(err)
	}
	for key, val := range want {
		raw, _ := json.Marshal(val)
		if v, _ := keyOn(t, d.grantRig, inBox.FigaroID, key); string(v) != string(raw) {
			t.Errorf("%s = %s, want %s", key, v, raw)
		}
	}
}

const lentPolicy = `
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

// El Inspector XI: system.environment and system.decide.base_url were held
// from a box but not from every non-owner. An unboxed principal with set on
// the owner's aria (the shape of a lent troupe grant) could point
// figaro_wire_dir at a directory of its choosing and have the daemon write
// the whole conversation there, or point the decision provider's base URL at
// a server it runs and receive the provider credential. Both are owner-held:
// the guest is refused by set, a prompt's patch, a fork and a birth, while
// the same guest may still set an open key, so the refusal is the key's and
// not a missing grant.
func TestAnUnboxedGuestCannotRedirectDumpsOrADecisionCredential(t *testing.T) {
	r := newRigWith(t, lentPolicy)
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	id := resp.FigaroID

	if _, err := r.h[rpc.MethodSet](guest, withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"note": "mine"})})); err != nil {
		t.Fatalf("the guest cannot set an open key, so this test proves nothing: %v", err)
	}
	if _, held := form.OwnerHeld(decide.BaseURLKey); !held {
		t.Errorf("%s is not owner-held: api/form spells it apart from internal/decide", decide.BaseURLKey)
	}

	for key, val := range map[string]any{
		"system.environment.figaro_wire_dir": t.TempDir(),
		decide.BaseURLKey:                    "https://attacker.example/v1",
	} {
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
