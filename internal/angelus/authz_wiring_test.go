package angelus

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// These go through the REAL handler map built by NewHandlers: the thing most
// likely to break is the wiring, not the rule.

func authzHandlers(t *testing.T, policy string, live *liveForkFigaro) *Handlers {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	if live != nil {
		if err := a.Registry.Register(live); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	return NewHandlers(ServerConfig{
		Angelus: a,
		Config:  &config.Loaded{Config: config.Config{Authz: config.AuthzConfig{Policy: policy}}},
		Ctx:     context.Background(),
	})
}

func selfFork(t *testing.T) []byte {
	t.Helper()
	params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "aria0001"}, "aria0001", nil)
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	return params
}

// A config that says nothing must behave exactly as figaro did before.
func TestForkPolicyOffByDefault(t *testing.T) {
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	if err := a.Registry.Register(&liveForkFigaro{id: "aria0001", turnActive: true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	// It may fail for want of that aria, but it must NOT fail with a denial.
	_, ferr := hs.Map[rpc.MethodFork](context.Background(), selfFork(t))
	if ferr != nil && strings.Contains(ferr.Error(), "deadlock") {
		t.Fatalf("default config denied the fork: %v", ferr)
	}
}

func TestForkDeniedForSelfDuringTurn(t *testing.T) {
	hs := authzHandlers(t, "default", &liveForkFigaro{id: "aria0001", turnActive: true})
	_, ferr := hs.Map[rpc.MethodFork](context.Background(), selfFork(t))
	if ferr == nil {
		t.Fatal("want denial")
	}
	for _, want := range []string{"deadlock", "setsid", "FIGARO_ARIA", "aria0001"} {
		if !strings.Contains(ferr.Error(), want) {
			t.Fatalf("denial missing %q:\n%s", want, ferr)
		}
	}
}

// The legitimate cases must reach the handler (and then fail on the missing
// aria, which is fine: what matters is that the failure is not a denial).
func TestForkAllowedWhenNotASelfForkMidTurn(t *testing.T) {
	cases := []struct {
		name           string
		target, caller string
		turnActive     bool
	}{
		{"idle self-fork is the documented workaround", "aria0001", "aria0001", false},
		{"another aria forking this one", "aria0001", "aria0002", true},
		{"anonymous caller", "aria0001", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := authzHandlers(t, "default", &liveForkFigaro{id: "aria0001", turnActive: tc.turnActive})
			params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: tc.target}, tc.caller, nil)
			if err != nil {
				t.Fatalf("WithCaller: %v", err)
			}
			_, ferr := hs.Map[rpc.MethodFork](context.Background(), params)
			if ferr != nil && strings.Contains(ferr.Error(), "deadlock") {
				t.Fatalf("wrongly denied: %v", ferr)
			}
		})
	}
}

func TestEveryServedMethodIsGuarded(t *testing.T) {
	hs := authzHandlers(t, "default", nil)
	for _, m := range []string{
		rpc.MethodCreate, rpc.MethodFork, rpc.MethodPromote, rpc.MethodKill,
		rpc.MethodList, rpc.MethodAttach, rpc.MethodBind, rpc.MethodResolve,
		rpc.MethodUnbind, rpc.MethodStatus, rpc.MethodSaveBindings, rpc.MethodIR,
		rpc.MethodProviderLedger,
	} {
		if hs.Map[m] == nil {
			t.Fatalf("method %s missing from the guarded map", m)
		}
	}
}

// A dormant or unknown aria has no turn in flight, so forking it is free.
func TestTurnActivePredicateHandlesUnknownAria(t *testing.T) {
	hs := authzHandlers(t, "", nil)
	if hs.h.turnActive("nobody") {
		t.Fatal("unknown aria reported as mid-turn")
	}
	if (&handlers{}).turnActive("nobody") {
		t.Fatal("nil registry reported as mid-turn")
	}
}

func TestUnknownPolicyNameFallsBackToAllowAll(t *testing.T) {
	hs := authzHandlers(t, "no-such-policy", &liveForkFigaro{id: "aria0001", turnActive: true})
	_, ferr := hs.Map[rpc.MethodFork](context.Background(), selfFork(t))
	if ferr != nil && strings.Contains(ferr.Error(), "deadlock") {
		t.Fatalf("unknown policy name gated a request: %v", ferr)
	}
}

func TestAuthzErrorCodeIsInTheApplicationRange(t *testing.T) {
	if authz.ErrCode > -32000 || authz.ErrCode < -32099 {
		t.Fatalf("ErrCode %d is outside the reserved application range", authz.ErrCode)
	}
}
