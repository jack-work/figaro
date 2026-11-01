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
