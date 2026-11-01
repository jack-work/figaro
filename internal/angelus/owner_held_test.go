package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// Owner-held keys choose what an aria runs on and what it may do: its
// provider, model, budgets, credo, tools and methods. A box, the attacker of
// the chat design, must not set any of them on its own aria, by set, by a
// prompt's form patch, at birth or at a fork.
func TestABoxCannotSetOwnerHeldKeys(t *testing.T) {
	d := boxDoorRig(t, "true")
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
	d := boxDoorRig(t, "true")
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
