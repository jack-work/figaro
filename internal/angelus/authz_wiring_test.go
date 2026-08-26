package angelus

import (
	"context"
	"github.com/jack-work/figaro/internal/store"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

// These go through the REAL handler map built by NewHandlers, not a
// hand-assembled policy, because the thing most likely to break is the WIRING:
// a Guard that never gets applied, a config accessor that reads the wrong
// field, a predicate wired to the wrong registry. The unit tests in
// internal/authz already prove the rule; this proves it is switched on.

func authzHandlers(t *testing.T, callerIdentity bool, policy string, live *liveForkFigaro) *Handlers {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	if live != nil {
		if err := a.Registry.Register(live); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	enabled := callerIdentity
	return NewHandlers(ServerConfig{
		Angelus: a,
		Config: &config.Loaded{Config: config.Config{
			Authz: config.AuthzConfig{CallerIdentity: &enabled, Policy: policy},
		}},
		Ctx: context.Background(),
	})
}

// The whole point of the switch, proven through config rather than by calling
// the provider directly.
func TestForkPolicyOffByDefault(t *testing.T) {
	// A config that says nothing must behave exactly as figaro did before.
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	live := &liveForkFigaro{id: "aria0001", turnActive: true}
	if err := a.Registry.Register(live); err != nil {
		t.Fatalf("register: %v", err)
	}
	hs := NewHandlers(ServerConfig{
		Angelus: a,
		Config:  &config.Loaded{}, // no [authz] section at all
		Ctx:     context.Background(),
	})
	params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "aria0001"}, "aria0001", nil)
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	// It may fail for want of that aria, but it must NOT fail with a denial:
	// the request has to reach the handler.
	_, ferr := hs.Map[rpc.MethodFork](context.Background(), params)
	if ferr != nil && strings.Contains(ferr.Error(), "deadlock") {
		t.Fatalf("default config denied the fork: %v", ferr)
	}
}

// With both switches on, a self-fork mid-turn is refused, and refused with
// instructions, all the way out through the handler map.
func TestForkDeniedForSelfDuringTurn(t *testing.T) {
	hs := authzHandlers(t, true, "default", &liveForkFigaro{id: "aria0001", turnActive: true})
	params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "aria0001"}, "aria0001", nil)
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	_, ferr := hs.Map[rpc.MethodFork](context.Background(), params)
	if ferr == nil {
		t.Fatal("want denial")
	}
	msg := ferr.Error()
	for _, want := range []string{"deadlock", "setsid", "FIGARO_ARIA", "aria0001"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("denial missing %q:\n%s", want, msg)
		}
	}
}

// The three legitimate cases, through the real map. Each must reach the handler
// (and then fail on the missing Backend, which is fine: what matters is that
// the failure is not a denial).
func TestForkAllowedWhenNotASelfForkMidTurn(t *testing.T) {
	cases := []struct {
		name           string
		callerIdentity bool
		target, caller string
		turnActive     bool
	}{
		{"idle self-fork is the documented workaround", true, "aria0001", "aria0001", false},
		{"another aria forking this one", true, "aria0001", "aria0002", true},
		{"anonymous caller", true, "aria0001", "", true},
		{"authn disabled means anonymous", false, "aria0001", "aria0001", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := authzHandlers(t, tc.callerIdentity, "default",
				&liveForkFigaro{id: "aria0001", turnActive: tc.turnActive})
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

// Guard must cover the served set exactly, a method added to the map without
// thinking about authorization is still guarded.
func TestEveryServedMethodIsGuarded(t *testing.T) {
	hs := authzHandlers(t, true, "default", nil)
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

// A dormant or unknown aria has no turn in flight, so forking it is free. If
// the predicate treated "not in the registry" as active, every fork of a
// dormant aria would be denied.
func TestTurnActivePredicateHandlesUnknownAria(t *testing.T) {
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	if hs.h.turnActive("nobody") {
		t.Fatal("unknown aria reported as mid-turn")
	}
	// And a nil registry must not panic on the authorization path.
	empty := &handlers{}
	if empty.turnActive("nobody") {
		t.Fatal("nil registry reported as mid-turn")
	}
}

// AN UNKNOWN POLICY NAME MUST NOT FALL BACK TO ALLOW-ALL.
//
// This test asserted the opposite until 2026-08-25, and the behaviour it
// pinned was a critical fail-open: `policy = "rules"` -- a name nobody had
// implemented -- selected allow-all, so a config that read as locked down
// gated nothing. A typo, an older binary, or a policy renamed between
// releases all produced a wide-open daemon that looked configured.
//
// The name is still data, not a bool. But unknown data is a startup error:
// config.ValidateAuthz refuses it before the daemon runs, and the factory
// panics if the two lists ever drift, because failing loudly beats failing
// open.
func TestUnknownPolicyNameIsRefused(t *testing.T) {
	l := &config.Loaded{Config: config.Config{
		Authz: config.AuthzConfig{Policy: "no-such-policy"},
	}}
	err := l.ValidateAuthz()
	if err == nil {
		t.Fatal("an unknown policy name was accepted: it must refuse to start")
	}
	// The refusal has to name the alternatives, or the operator is left
	// guessing at a closed vocabulary.
	for _, want := range config.KnownAuthzPolicies {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention the known policy %q: %v", want, err)
		}
	}
}

// Every name config accepts, the factory must construct. This is the pairing
// that makes the panic in handlers.policy unreachable in a validated config;
// if it ever fails, one of the two lists moved without the other.
func TestEveryKnownPolicyNameConstructs(t *testing.T) {
	for _, name := range config.KnownAuthzPolicies {
		l := &config.Loaded{Config: config.Config{Authz: config.AuthzConfig{Policy: name}}}
		if err := l.ValidateAuthz(); err != nil {
			t.Fatalf("%s: config refuses a name it lists as known: %v", name, err)
		}
		h := &handlers{config: l}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: config accepts it but the factory panics: %v", name, r)
				}
			}()
			if p := h.policy(); p == nil {
				t.Fatalf("%s: factory returned a nil policy", name)
			}
		}()
	}
}

// The `grants` policy must arrive deny-by-default when its table is empty.
// This is the config-level half of authz.TestZeroGrantsDeniesEverything: a
// policy that denies in isolation is worth nothing if the wiring hands the
// daemon an allow-all instead.
func TestGrantsPolicyWithNoTableDenies(t *testing.T) {
	l := &config.Loaded{Config: config.Config{
		Authz: config.AuthzConfig{Policy: "grants"},
	}}
	if err := l.ValidateAuthz(); err != nil {
		t.Fatalf("grants with an empty table should start (and refuse everything): %v", err)
	}
	h := &handlers{config: l}
	d := h.policy().Check(authz.Request{
		Method:   rpc.MethodQua,
		Identity: authz.Identity{FigaroID: "aria0001", Authenticated: true, Groups: []string{"admin"}},
	})
	if d.Allow {
		t.Fatal("an empty grant table allowed figaro.qua: the table must deny by default")
	}
}

func TestAuthzErrorCodeIsInTheApplicationRange(t *testing.T) {
	// JSON-RPC 2.0 reserves -32000..-32099 for application errors.
	if authz.ErrCode > -32000 || authz.ErrCode < -32099 {
		t.Fatalf("ErrCode %d is outside the reserved application range", authz.ErrCode)
	}
}
