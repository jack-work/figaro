package authz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

func enforced(t *testing.T) map[string]jkrpc.HandlerFunc {
	t.Helper()
	p := loaded(t, sandboxPolicy)
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	return Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodHello:       ok,
		rpc.MethodList:        ok,
		rpc.MethodNodeResolve: ok,
		rpc.MethodQua:         ok,
		rpc.MethodKill:        ok,
		rpc.MethodGC:          ok,
		"figaro.undeclared":   ok,
	}, AriaHeader{}, Enforce(p))
}

func call(t *testing.T, h map[string]jkrpc.HandlerFunc, ctx context.Context, method, aria string) error {
	t.Helper()
	params := json.RawMessage(`{}`)
	if aria != "" {
		params = json.RawMessage(`{"figaro_id":"` + aria + `"}`)
	}
	_, err := h[method](ctx, params)
	return err
}

func TestEveryCallIsDecidedOnItsVerbAndTheAriaItNames(t *testing.T) {
	h := enforced(t)
	boxCtx := WithSubject(context.Background(), subject(box))
	meCtx := WithSubject(context.Background(), subject(me))

	if err := call(t, h, boxCtx, rpc.MethodQua, "aaaa0001"); err != nil {
		t.Fatalf("a sandbox sending to its own aria: %v", err)
	}
	if err := call(t, h, boxCtx, rpc.MethodQua, "aaaa0002"); err == nil || !strings.Contains(err.Error(), rpc.MethodQua) {
		t.Fatalf("a sandbox sending to someone else's aria: %v", err)
	}
	if err := call(t, h, boxCtx, rpc.MethodKill, "aaaa0001"); err == nil {
		t.Fatal("a sandbox killed")
	}
	if err := call(t, h, boxCtx, rpc.MethodGC, ""); err == nil {
		t.Fatal("a sandbox ran an admin method")
	}
	if err := call(t, h, meCtx, rpc.MethodGC, ""); err != nil {
		t.Fatalf("the owner: %v", err)
	}
	if err := call(t, h, boxCtx, rpc.MethodHello, ""); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := call(t, h, meCtx, "figaro.undeclared", ""); err == nil {
		t.Fatal("a method with no verb was served")
	}
	if err := call(t, h, context.Background(), rpc.MethodQua, "aaaa0002"); err == nil {
		t.Fatal("a caller nobody admitted was served")
	}
}

// A caller that may not read an aria cannot tell it from one that does not
// exist: both answer not found, in the same words. Only a caller that may
// read it is told which verb the policy withholds.
func TestAnAriaTheCallerCannotReadIsNotFound(t *testing.T) {
	h := enforced(t)
	boxCtx := WithSubject(context.Background(), subject(box))

	hidden := call(t, h, boxCtx, rpc.MethodQua, "aaaa0002")
	absent := call(t, h, boxCtx, rpc.MethodQua, "ffff0009")
	if hidden == nil || absent == nil {
		t.Fatalf("hidden=%v absent=%v", hidden, absent)
	}
	if hidden.Error() != NotFound(rpc.MethodQua, "aaaa0002").Error() {
		t.Fatalf("hidden: %v", hidden)
	}
	if strings.Replace(hidden.Error(), "aaaa0002", "ffff0009", 1) != absent.Error() {
		t.Fatalf("an unreadable aria and a missing one answer differently: %q, %q", hidden, absent)
	}
	var jerr *jkrpc.Error
	if errors.As(hidden, &jerr) {
		t.Fatalf("the refusal kept the policy's error code: %v", jerr)
	}

	err := call(t, h, boxCtx, rpc.MethodKill, "aaaa0001")
	if !errors.As(err, &jerr) || jerr.Code != ErrCode || !strings.Contains(err.Error(), rpc.MethodKill) {
		t.Fatalf("a reader lacking kill should hear the policy: %v", err)
	}
}

// list and node.resolve filter their rows by read, so they need no read on
// the daemon; they still need a role, so a subject the policy does not know
// learns nothing, not even an empty answer.
func TestTheRowFilteredMethodsNeedARole(t *testing.T) {
	h := enforced(t)
	for _, m := range []string{rpc.MethodList, rpc.MethodNodeResolve} {
		for _, d := range []Door{DoorInProcess, DoorLocal, DoorWeb} {
			if err := call(t, h, WithDoor(WithSubject(context.Background(), subject(box)), d), m, ""); err != nil {
				t.Errorf("%s at the %s door, for a role with no read on the daemon: %v", m, d, err)
			}
			err := call(t, h, WithDoor(WithSubject(context.Background(), subject(other)), d), m, "")
			if err == nil || !strings.Contains(err.Error(), "holds no role") {
				t.Errorf("%s at the %s door, for a subject with no role: %v", m, d, err)
			}
		}
	}
}

// hello names the node and the caller's principal. Off the local door it
// is answered only to a subject the policy binds to a role.
func TestHelloOffTheLocalDoorNeedsARole(t *testing.T) {
	h := enforced(t)
	at := func(d Door, p jkauthz.Principal) context.Context {
		return WithDoor(WithSubject(context.Background(), subject(p)), d)
	}
	for name, c := range map[string]struct {
		ctx  context.Context
		pass bool
	}{
		"a stranger in process":         {at(DoorInProcess, other), true},
		"a stranger at the local door":  {at(DoorLocal, other), true},
		"a role holder at the web door": {at(DoorWeb, box), true},
		"a stranger at the web door":    {at(DoorWeb, other), false},
		"a stranger at a node door":     {at(DoorNode, other), false},
		"a stranger at a new door":      {at(DoorWeb+1, other), false},
	} {
		if err := call(t, h, c.ctx, rpc.MethodHello, ""); (err == nil) != c.pass {
			t.Errorf("%s: err = %v, want pass %v", name, err, c.pass)
		}
	}
}
