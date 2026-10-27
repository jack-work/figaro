package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

const creatorPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.creator]
grants = [{ verbs = ["create"], on = ["*"] }, { verbs = ["use"], on = ["sandbox:local"] }, { verbs = ["read"], on = ["owner:self"] }]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "creator"
principals = ["user:guest"]
`

func patchOf(key, val string) *form.Patch {
	raw, _ := json.Marshal(val)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil)
	return &p
}

// A form born of a parent starts as a copy of it, so copying is reading.
func TestFormsBornOfAParentNeedReadOnIt(t *testing.T) {
	r := newRigWith(t, creatorPolicy)
	guest := as("user", "guest")
	theirs, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: patchOf("mantra", "the owner's")})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := r.call(guest, rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: patchOf("mantra", "the guest's")})
	if err != nil {
		t.Fatal(err)
	}
	theirID, myID := theirs.(rpc.FormCreateResponse).FormID, mine.(rpc.FormCreateResponse).FormID

	if _, err := r.call(guest, rpc.MethodFormCreate, rpc.FormCreateRequest{Parent: theirID, Patch: patchOf("x", "y")}); err == nil || !strings.Contains(err.Error(), "reads it") {
		t.Fatalf("form.create copied a form the caller cannot read: %v", err)
	}
	if _, err := r.call(guest, rpc.MethodFormBind, rpc.FormBindRequest{Parent: theirID}); err == nil || !strings.Contains(err.Error(), "reads it") {
		t.Fatalf("form.bind forked a form the caller cannot read: %v", err)
	}
	if _, err := r.call(guest, rpc.MethodFormCreate, rpc.FormCreateRequest{Parent: myID, Patch: patchOf("x", "y")}); err != nil {
		t.Fatalf("copying its own form: %v", err)
	}
	if _, err := r.call(guest, rpc.MethodFormBind, rpc.FormBindRequest{Parent: myID}); err != nil {
		t.Fatalf("binding its own form: %v", err)
	}
}
