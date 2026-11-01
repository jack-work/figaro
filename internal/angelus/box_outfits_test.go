package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

const judgeWearsOnlyJudged = "outfits = [\"judged\"]\n"

// Values from an outfit a box names count as the owner's, so with owner-held
// keys alone a box could still dress its arias in any outfit the owner keeps.
// A box with an outfit list dresses only in those, at birth and after.
func TestABoxDressesOnlyInTheOutfitsItsBoxNames(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgeWearsOnlyJudged)
	c := d.dial(t)
	var born rpc.CreateResponse
	if err := c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"judged"}}, &born); err != nil {
		t.Fatalf("canary: the listed outfit was refused, so the refusals below prove nothing: %v", err)
	}
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "may not dress in outfit") {
			t.Errorf("%s: %v", what, err)
		}
	}
	refused("create in an unlisted outfit", c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"judged-elsewhere"}}, nil))
	if err := c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{}, nil); err == nil || !strings.Contains(err.Error(), "mints only in a named outfit: give -O with one of: judged") {
		t.Errorf("create in no outfit: %v", err)
	}
	refused("fork", c.Call(t.Context(), rpc.MethodFork, rpc.ForkRequest{FigaroID: born.FigaroID, Outfits: []string{"mock"}}, nil))
	refused("set", boxOnAria(t, c, rpc.MethodSet, born.FigaroID, rpc.SetRequest{Outfits: []string{"mock"}}, nil))
	refused("qua", boxOnAria(t, c, rpc.MethodQua, born.FigaroID, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Outfits: []string{"mock"}}}, nil))
	refused("form.create", c.Call(t.Context(), rpc.MethodFormCreate, rpc.FormCreateRequest{Outfits: []string{"mock"}, Patch: heldPatch(t, map[string]any{"note": "x"})}, nil))

	if _, err := d.create(asOwner(), "judged-elsewhere"); err != nil {
		t.Errorf("the owner's choice of outfit is not the box's to limit: %v", err)
	}
}

// form.bind mints an aria as surely as create does, so a box binds only in a
// named outfit from its list, whether or not it names a parent of its own.
func TestABoxBindsOnlyInANamedOutfitFromItsList(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgeWearsOnlyJudged)
	c := d.dial(t)
	var own rpc.FormCreateResponse
	if err := c.Call(t.Context(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"note": "x"})}, &own); err != nil {
		t.Fatal(err)
	}
	for what, req := range map[string]rpc.FormBindRequest{
		"no parent, no outfit":      {},
		"its own form, no outfit":   {Parent: own.FormID},
		"its own form, another one": {Parent: own.FormID, Outfits: []string{"mock"}},
		"no parent, another outfit": {Outfits: []string{"judged-elsewhere"}},
	} {
		if err := c.Call(t.Context(), rpc.MethodFormBind, req, nil); err == nil || !strings.Contains(err.Error(), "judged") {
			t.Errorf("bind with %s: %v", what, err)
		}
	}
	if err := c.Call(t.Context(), rpc.MethodFormBind, rpc.FormBindRequest{Outfits: []string{"judged"}}, nil); err != nil {
		t.Fatalf("canary: a bind in the listed outfit was refused, so the refusals above prove nothing: %v", err)
	}
}

// A head fork of a conversation carries its parent's dress. Any other fork
// mints a fresh one: of an unbound form the box made, or at the genesis root,
// it would carry no outfit at all, so it needs a listed outfit like create.
func TestABoxForksBareOnlyAtTheHeadOfAConversation(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", "outfits = [\"judged-chat\"]\n")
	c := d.dial(t)
	var chat rpc.CreateResponse
	if err := c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"judged-chat"}}, &chat); err != nil {
		t.Fatal(err)
	}
	var own rpc.FormCreateResponse
	if err := c.Call(t.Context(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"note": "x"})}, &own); err != nil {
		t.Fatal(err)
	}
	for what, req := range map[string]rpc.ForkRequest{
		"its own unbound form":        {FigaroID: own.FormID},
		"the genesis root of an aria": {FigaroID: chat.FigaroID, AtLT: 1},
		"inside an aria":              {FigaroID: chat.FigaroID, AtLT: 3},
	} {
		if err := c.Call(t.Context(), rpc.MethodFork, req, nil); err == nil || !strings.Contains(err.Error(), "mints only in a named outfit") {
			t.Errorf("a bare fork of %s: %v", what, err)
		}
	}
	for what, req := range map[string]rpc.ForkRequest{
		"the head of an aria":       {FigaroID: chat.FigaroID},
		"its own form, dressed":     {FigaroID: own.FormID, Outfits: []string{"judged-chat"}},
		"the genesis root, dressed": {FigaroID: chat.FigaroID, AtLT: 1, Outfits: []string{"judged-chat"}},
	} {
		var out rpc.ForkResponse
		if err := c.Call(t.Context(), rpc.MethodFork, req, &out); err != nil {
			t.Errorf("canary: a fork of %s was refused: %v", what, err)
			continue
		}
		if v, _ := keyOn(t, d.grantRig, out.Alternative, "system.ttl"); string(v) != `"24h"` {
			t.Errorf("a fork of %s carries system.ttl %s, want the outfit's", what, v)
		}
	}
}
