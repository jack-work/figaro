package authz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

func enforced(t *testing.T) map[string]jkrpc.HandlerFunc {
	t.Helper()
	p := loaded(t, sandboxPolicy)
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	return Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodHello:     ok,
		rpc.MethodQua:       ok,
		rpc.MethodKill:      ok,
		rpc.MethodGC:        ok,
		"figaro.undeclared": ok,
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
