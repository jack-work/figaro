package authz

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

// With no policy at all, the local door and in-process calls pass as they
// always have; any other door passes only the owner, never everyone.
func TestWithNoPolicyOnlyTheOwnerPassesOffTheLocalDoor(t *testing.T) {
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodGC: ok}, AriaHeader{}, LocalOrOwner(me))
	at := func(d Door, p jkauthz.Principal) context.Context {
		return WithDoor(WithSubject(context.Background(), subject(p)), d)
	}
	for name, c := range map[string]struct {
		ctx  context.Context
		pass bool
	}{
		"a stranger in process":     {WithSubject(context.Background(), subject(other)), true},
		"a stranger at the local":   {at(DoorLocal, other), true},
		"the owner at the web door": {at(DoorWeb, me), true},
		"a stranger at the web":     {at(DoorWeb, other), false},
		"a stranger at a node door": {at(DoorNode, other), false},
		"a stranger at a new door":  {at(DoorWeb+1, other), false},
		"nobody at the web door":    {at(DoorWeb, jkauthz.Principal{}), false},
	} {
		if err := call(t, h, c.ctx, rpc.MethodGC, ""); (err == nil) != c.pass {
			t.Errorf("%s: err = %v, want pass %v", name, err, c.pass)
		}
	}
	if err := call(t, Guard(map[string]jkrpc.HandlerFunc{rpc.MethodGC: ok}, AriaHeader{}, LocalOrOwner(jkauthz.Principal{})), at(DoorWeb, jkauthz.Principal{}), rpc.MethodGC, ""); err == nil {
		t.Fatal("with no owner known, the zero subject passed the web door")
	}
}
