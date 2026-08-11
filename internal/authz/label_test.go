package authz

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
)

// THE LABEL MUST NEVER AUTHORIZE.
//
// x-caller is set from an environment variable by whoever is calling. If it
// could stand in for an authenticated aria id, every rule would be one
// `FIGARO_CALLER=…` away from bypass, and the first rule we have is the one
// that stops a fork deadlocking. This is the test that says so.
func TestLabelCannotAuthorize(t *testing.T) {
	// A caller asserts a label that names the very aria it is targeting, with
	// no aria credential at all.
	params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "aria0001"}, "", &rpc.CallerRef{Label: "aria0001"})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	id := AriaHeader{Enabled: true}.Authenticate(rpc.MethodFork, params)

	if id.Authenticated {
		t.Fatalf("a label authenticated the caller: %+v", id)
	}
	if id.FigaroID != "" {
		t.Fatalf("a label leaked into FigaroID: %+v", id)
	}
	if !id.Anonymous() {
		t.Fatal("a labelled caller is still anonymous for policy purposes")
	}

	// And therefore the self-fork rule does not fire: SelfTargeted requires an
	// authenticated identity, so an assertion cannot impersonate its target.
	req := Request{Identity: id, Method: rpc.MethodFork, Params: params}
	if req.SelfTargeted() {
		t.Fatal("an asserted label was treated as self-targeting")
	}
	if d := NoSelfForkDuringTurn(func(string) bool { return true }).Check(req); !d.Allow {
		t.Fatalf("a label drove an authorization decision: %q", d.Reason)
	}
}

// Attribution is not gated by authentication. A human at a terminal is never
// authenticated and is exactly the caller the model most needs named; turning
// the provider off withholds AUTHORITY, not identity.
func TestLabelSurvivesADisabledProvider(t *testing.T) {
	params, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "t"}, "aria0001", &rpc.CallerRef{Label: "Jack"})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	off := AriaHeader{Enabled: false}.Authenticate(rpc.MethodFork, params)
	if off.Authenticated || off.FigaroID != "" {
		t.Fatalf("disabled provider authenticated: %+v", off)
	}
	if off.Label != "Jack" {
		t.Fatalf("label lost with the provider disabled: %+v", off)
	}
	if got := off.Attribution(); got != "Jack" {
		t.Fatalf("Attribution = %q, want Jack", got)
	}
}

// The two render differently ON PURPOSE, and the difference is what a reader
// (human or model) uses to tell proof from assertion.
func TestAttributionDistinguishesProofFromAssertion(t *testing.T) {
	authed := Identity{FigaroID: "76062b18", Authenticated: true}
	if got := authed.Attribution(); got != "aria 76062b18" {
		t.Fatalf("authenticated Attribution = %q, want 'aria 76062b18'", got)
	}
	asserted := Identity{Label: "Jack"}
	if got := asserted.Attribution(); got != "Jack" {
		t.Fatalf("asserted Attribution = %q, want Jack", got)
	}
	// Unknown renders as nothing, so a caller shows no attribution rather than
	// guessing one.
	if got := (Identity{}).Attribution(); got != "" {
		t.Fatalf("unknown Attribution = %q, want empty", got)
	}
	// An authenticated aria that ALSO asserts a label is rendered by its proof.
	both := Identity{FigaroID: "76062b18", Authenticated: true, Label: "Jack"}
	if got := both.Attribution(); got != "aria 76062b18" {
		t.Fatalf("proof did not win over assertion: %q", got)
	}
}

// End-to-end through the raw wire: a forged label cannot produce an
// "aria <id>" attribution, because the prefix is stripped on the way out.
func TestForgedLabelCannotRenderAsAnAria(t *testing.T) {
	raw, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: "t"}, "", &rpc.CallerRef{Label: "aria 76062b18"})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	id := AriaHeader{Enabled: true}.Authenticate(rpc.MethodFork, raw)
	if got := id.Attribution(); got == "aria 76062b18" {
		t.Fatalf("a forged label rendered as an authenticated aria: %q", got)
	}
	if got := id.Attribution(); got != "76062b18" {
		t.Fatalf("Attribution = %q, want the stripped label", got)
	}
}

func TestPolicySeesLabelButNeedNotTrustIt(t *testing.T) {
	// The policy is HANDED the label (a future rule may want to log it), but
	// nothing in the shipped rules keys on it. This pins the plumbing without
	// blessing its use.
	var seen Identity
	p := PolicyFunc(func(r Request) Decision { seen = r.Identity; return Allow() })
	params, _ := rpc.WithCaller(rpc.ForkRequest{FigaroID: "t"}, "", &rpc.CallerRef{Label: "Jack"})
	var raw json.RawMessage = params
	p.Check(Request{
		Identity: AriaHeader{Enabled: true}.Authenticate(rpc.MethodFork, raw),
		Method:   rpc.MethodFork, Params: raw,
	})
	if seen.Label != "Jack" {
		t.Fatalf("policy did not receive the label: %+v", seen)
	}
	if seen.Authenticated {
		t.Fatalf("policy received it as authenticated: %+v", seen)
	}
}
