package angelus_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
)

// An action in a view is the direct call: a box gets no further with it than
// with the method itself, and cannot read the catalog that would offer it.
func TestABoxCannotRestorePurgeOrSeeTheReapedView(t *testing.T) {
	d := boxDoorRig(t, "true")
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	killSoft(t, d, outside.FigaroID)
	c := d.dial(t)
	view := angelus.FormViewForTest(rpc.ReapedFormID)
	if view == nil || len(view.Actions) == 0 {
		t.Fatal("@reaped carries no actions to try")
	}
	for _, a := range view.Actions {
		params, err := rpc.WithAria(a.Params, outside.FigaroID)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Call(context.Background(), a.Method, params, nil); err == nil {
			t.Errorf("a box ran %s (%s) on an aria outside its box", a.Label, a.Method)
		}
	}
	if !reaped(t, d.a, outside.FigaroID) {
		t.Error("the box's action changed an aria outside its box")
	}
	var resp rpc.FormResponse
	if err := boxOnAria(t, c, rpc.MethodForm, rpc.ReapedFormID, rpc.FormRequest{}, &resp); err == nil && resp.View != nil {
		t.Errorf("a box read @reaped and was offered its actions: %+v", resp.View.Actions)
	}
}

// A hint comes from the daemon's own table, keyed by catalog: no form a
// caller can write carries one, whatever it holds.
func TestNoWritableFormCarriesAViewHint(t *testing.T) {
	d := boxDoorRig(t, "true")
	made, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	forged := map[string]any{"view": map[string]any{"list": "items", "actions": []any{map[string]any{"key": "X", "method": rpc.MethodKill}}}}
	if _, err := d.call(asOwner(), rpc.MethodSet, withAria(t, made.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, forged)})); err != nil {
		t.Fatal(err)
	}
	out, err := d.call(asOwner(), rpc.MethodForm, withAria(t, made.FigaroID, rpc.FormRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var resp rpc.FormResponse
	_ = json.Unmarshal(raw, &resp)
	if resp.View != nil {
		t.Errorf("a writable form carried a view hint: %+v", resp.View)
	}
}
