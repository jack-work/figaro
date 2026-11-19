package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

const servicePolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.svc]
permissions = ["create:*", "set:owner:self", "read:owner:self"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "svc"
principals = ["user:svc"]
`

// #155: a mapped-uid service sets the lifetime of the forms it creates, at
// birth and on renewal, and still holds no other owner-held key on them nor
// any lifetime on a form it did not create.
func TestAFormsCreatorSetsItsLifetime(t *testing.T) {
	r := newRigWith(t, servicePolicy)
	svc := authz.WithDoor(as("user", "svc"), authz.DoorLocal)
	created := func(ctx context.Context, kv map[string]any) (string, error) {
		kv["name"] = "x"
		raw, _ := json.Marshal(rpc.FormCreateRequest{Patch: heldPatch(t, kv)})
		out, err := r.h[rpc.MethodFormCreate](ctx, raw)
		if err != nil {
			return "", err
		}
		return out.(rpc.FormCreateResponse).FormID, nil
	}
	set := func(ctx context.Context, id string, kv map[string]any) error {
		_, err := r.h[rpc.MethodSet](ctx, withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, kv)}))
		return err
	}

	id, err := created(svc, map[string]any{"system.ttl": "2h", "system.keepalive": "1h"})
	if err != nil {
		t.Fatalf("the creator was refused a lifetime at birth: %v", err)
	}
	if v, _ := keyOn(t, r, id, "system.ttl"); string(v) != `"2h"` {
		t.Errorf("system.ttl = %s, want \"2h\"", v)
	}
	if err := set(svc, id, map[string]any{"system.ttl": "6h"}); err != nil {
		t.Errorf("the creator was refused a renewal: %v", err)
	}
	if err := set(svc, id, map[string]any{"system.keepalive": "0"}); err != nil {
		t.Errorf("the creator was refused a keepalive: %v", err)
	}
	if err := set(svc, id, map[string]any{"system.model": "m2"}); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the creator set system.model: %v", err)
	}
	if _, err := created(svc, map[string]any{"system.model": "m2"}); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the creator set system.model at birth: %v", err)
	}

	wide := newRigWith(t, strings.Replace(servicePolicy, `"set:owner:self", "read:owner:self"`, `"set:*", "read:*"`, 1))
	raw, _ := json.Marshal(rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"name": "y"})})
	out, err := wide.h[rpc.MethodFormCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	theirs := out.(rpc.FormCreateResponse).FormID
	if _, err := wide.h[rpc.MethodSet](svc, withAria(t, theirs, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"mantra": "m"})})); err != nil {
		t.Fatalf("the grant to set the owner's form does not reach it: %v", err)
	}
	if _, err := wide.h[rpc.MethodSet](svc, withAria(t, theirs, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.ttl": "1h"})})); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the service set the lifetime of a form the owner created: %v", err)
	}
}
