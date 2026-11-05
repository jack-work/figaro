package angelus_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func formAccessOf(t *testing.T, v any) *rpc.FormAccess {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var resp rpc.FormResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Access
}

// The access a form read carries must say what the write guard will refuse:
// the client greys those keys instead of offering an edit that bounces.
func TestAFormReadCarriesTheCallersAccess(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}

	owner, err := d.call(asOwner(), rpc.MethodForm, withAria(t, inBox.FigaroID, rpc.FormRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	a := formAccessOf(t, owner)
	if a == nil || a.Mode != rpc.FormWritable {
		t.Fatalf("owner access = %+v, want writable", a)
	}
	if _, denied := a.Refusal("system.model"); denied {
		t.Error("the owner is told system.model is not theirs to set")
	}
	if d, denied := a.Refusal("system.studies"); !denied || d.Kind != rpc.DenySystemManaged {
		t.Errorf("system.studies for the owner = %+v %v, want system-managed", d, denied)
	}

	var boxed rpc.FormResponse
	if err := boxOnAria(t, d.dial(t), rpc.MethodForm, inBox.FigaroID, rpc.FormRequest{}, &boxed); err != nil {
		t.Fatal(err)
	}
	if boxed.Access == nil || boxed.Access.Mode != rpc.FormWritable {
		t.Fatalf("box access = %+v, want writable with denials", boxed.Access)
	}
	for path, kind := range map[string]rpc.KeyDenyKind{
		"system.model":          rpc.DenyOwnerHeld,
		"system.tools":          rpc.DenyOwnerHeld,
		"target-aria":           rpc.DenyBoxHeld,
		"system.ttl":            rpc.DenyBoxUnlisted,
		"system.added_tomorrow": rpc.DenyBoxUnlisted,
	} {
		if d, denied := boxed.Access.Refusal(path); !denied || d.Kind != kind {
			t.Errorf("box %s = %+v %v, want %s", path, d, denied, kind)
		}
	}
	for _, path := range []string{"system.thinking_effort", "mantra", "notes.today"} {
		if d, denied := boxed.Access.Refusal(path); denied {
			t.Errorf("box %s is denied (%+v) but the guard lets a box write it", path, d)
		}
	}
}
