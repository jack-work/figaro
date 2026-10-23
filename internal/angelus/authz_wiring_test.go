package angelus

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

func authzHandlers(t *testing.T) *Handlers {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	return NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
}

func TestEveryServedMethodIsGuarded(t *testing.T) {
	hs := authzHandlers(t)
	for _, m := range []string{
		rpc.MethodCreate, rpc.MethodFork, rpc.MethodPromote, rpc.MethodDemote, rpc.MethodKill,
		rpc.MethodList, rpc.MethodAttach, rpc.MethodBind, rpc.MethodResolve,
		rpc.MethodUnbind, rpc.MethodStatus, rpc.MethodSaveBindings, rpc.MethodIR,
		rpc.MethodProviderLedger, rpc.MethodHello,
	} {
		if hs.Map[m] == nil {
			t.Fatalf("method %s missing from the guarded map", m)
		}
	}
}

func TestHelloStampsTheConnectionPrincipal(t *testing.T) {
	hs := authzHandlers(t)
	if _, err := hs.Map[rpc.MethodHello](context.Background(), []byte(`{"version":99}`)); err == nil {
		t.Fatal("version 99 accepted")
	}
	out, err := hs.Map[rpc.MethodHello](context.Background(), []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	got := out.(rpc.HelloResponse)
	if got.Version != rpc.ProtocolVersion || got.Principal != (rpc.Principal{}) {
		t.Fatalf("hello = %+v", got)
	}
	if got.Node == "" || got.Node != hs.h.angelus.Backend.NodeID() {
		t.Fatalf("hello node = %q, want the store's %q", got.Node, hs.h.angelus.Backend.NodeID())
	}
}

func TestAuthzErrorCodeIsInTheApplicationRange(t *testing.T) {
	if authz.ErrCode > -32000 || authz.ErrCode < -32099 {
		t.Fatalf("ErrCode %d is outside the reserved application range", authz.ErrCode)
	}
}
