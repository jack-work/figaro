// Package authz is the authentication and authorization seam for the figaro
// RPC surface.
//
// It exists to keep access decisions out of handler bodies. The shape is the
// one a server uses: a pluggable AUTHENTICATOR turns a request's credential
// into an Identity, and a single POLICY maps (identity, method, payload) to
// allow or deny-with-reason. Config selects both. Handlers do not consult
// booleans, do not know which rules exist, and cannot be individually forgotten
// when a rule is added — Guard wraps the whole handler map at once.
//
// Two properties are deliberate:
//
//   - The authenticator is DISABLEABLE. That is the entire point of moving
//     caller identity onto the wire (see rpc.CallerKey): FIGARO_ARIA cannot be
//     turned off, so a server has no state in which it may doubt it, and a
//     credential that cannot be doubted is not authenticating anything. With a
//     switch, "was this request authenticated" becomes a real question.
//
//   - The policy is ONE function, not a set of hooks. A rule that needs to run
//     everywhere gets to, and a future policy can be data (a table) or code (a
//     func) without touching a single call site.
package authz

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/jkrpc"
)

// Identity is the outcome of authentication: who the server is willing to say
// is calling. The zero Identity is the anonymous caller — a human at a
// terminal, an external script, or a request whose credential was rejected.
// Anonymous is a legitimate identity, not an error; whether it is sufficient
// for a given method is the policy's decision.
type Identity struct {
	// FigaroID is the calling aria, or "" for anonymous. AUTHENTICATED: a
	// policy may key on it.
	FigaroID string
	// Authenticated distinguishes "no credential presented" from "credential
	// presented and accepted". It is not simply FigaroID != "": a disabled
	// authenticator produces an anonymous identity even when a credential was
	// on the wire, and the difference is auditable.
	Authenticated bool
	// Label is an ASSERTED caller name (rpc.CallerLabelKey / FIGARO_CALLER),
	// carried for ATTRIBUTION ONLY.
	//
	// IT MUST NEVER REACH AN AUTHORIZATION DECISION. Anyone who can set an
	// environment variable can set it to anything, so a policy keyed on it is
	// one `FIGARO_CALLER=…` away from being bypassed. It is a separate field
	// from FigaroID rather than a fallback into it precisely so that rule is
	// enforced by the type and not by everyone remembering it — a rule that
	// lives only in a comment is a rule that gets broken.
	Label string
}

// Attribution renders who is speaking, for the model and for the UI.
//
// An authenticated aria renders "aria <id>"; an asserted label renders BARE.
// The asymmetry is the point: rpc.SanitizeLabel strips the reserved "aria "
// prefix from labels, so an assertion can never dress itself as an
// authenticated identity. Empty means unknown, and callers render nothing
// rather than guessing.
func (i Identity) Attribution() string {
	if i.Authenticated {
		return rpc.Attribution(i.FigaroID, i.Label)
	}
	// Not authenticated: whatever id was presented carries no weight here, so
	// only the asserted label can speak.
	return rpc.Attribution("", i.Label)
}

// Anonymous reports whether no aria was authenticated.
func (i Identity) Anonymous() bool { return i.FigaroID == "" }

// String renders an identity for error text and logs.
func (i Identity) String() string {
	if i.Anonymous() {
		return "anonymous"
	}
	return i.FigaroID
}

// Authenticator consumes a request's credential and produces an Identity. It
// sees the raw params because it runs BEFORE dispatch has chosen a request
// type, which is also what lets one authenticator serve every method.
type Authenticator interface {
	Authenticate(method string, params json.RawMessage) Identity
}

// AriaHeader authenticates the x-internal-figaro-id params field written by
// rpc.WithCaller. Enabled is the switch that makes it a credential: when false
// every request is anonymous no matter what it presented, and the policy sees a
// server that has chosen not to trust the wire.
//
// The id is validated by rpc.CallerOf on the way in — it reaches paths that
// name on-disk aria directories, so it is never taken on faith.
//
// This is trust-on-assertion, not proof: an aria's own shell-out is the only
// thing that normally sets FIGARO_ARIA, but nothing stops a process on the
// same machine from claiming any id. That is honest for a unix socket whose
// security model is filesystem permissions (0600), and it is exactly why this
// is an interface: a transport that can actually prove peer identity
// (SO_PEERCRED, or a token over HTTP) drops in here without any policy or
// handler changing.
type AriaHeader struct {
	Enabled bool
}

// Authenticate implements Authenticator.
//
// The asserted Label is read REGARDLESS of Enabled. Attribution is not gated
// by authentication: a human at a terminal is never authenticated and is
// exactly the caller whose name the model most needs. Disabling the provider
// withholds AUTHORITY, not identity — with it off, a presented aria id is
// ignored for policy purposes but the request is still attributable.
func (a AriaHeader) Authenticate(_ string, params json.RawMessage) Identity {
	// LabelOf, not the whole ref: the DUKE placeholder has no name until a
	// server resolves it against an aria's form, and authz has no
	// form. Attribution is resolved at the agent; this layer only ever
	// needs the explicitly asserted name, and needs it for display alone.
	id := Identity{Label: rpc.LabelOf(params)}
	if !a.Enabled {
		return id
	}
	if figaroID := rpc.CallerOf(params); figaroID != "" {
		id.FigaroID = figaroID
		id.Authenticated = true
	}
	return id
}

// Request is everything a policy is allowed to see. Params stays raw so a rule
// can look at whatever its method carries without authz importing every
// request type — and so adding a rule never widens this struct.
type Request struct {
	Identity Identity
	Method   string
	Params   json.RawMessage
}

// Target decodes the aria a request is aimed at, for the common rule shape
// "may this identity act on that aria". Most figaro requests carry it as
// figaro_id. Empty when the method has no target.
func (r Request) Target() string {
	if len(r.Params) == 0 {
		return ""
	}
	var t struct {
		FigaroID string `json:"figaro_id"`
	}
	if err := json.Unmarshal(r.Params, &t); err != nil {
		return ""
	}
	return t.FigaroID
}

// SelfTargeted reports whether an authenticated caller is acting on itself.
// Anonymous callers are never self-targeted: with no identity there is no
// "self" to compare against, and treating "" == "" as a match would deny
// every anonymous request to a method with no target.
func (r Request) SelfTargeted() bool {
	if r.Identity.Anonymous() {
		return false
	}
	return r.Identity.FigaroID == r.Target()
}

// Decision is a policy's answer. A denial must carry prose the CALLER can act
// on, so Reason is not optional in practice — see the self-fork rule, whose
// whole value is the instructions it returns.
type Decision struct {
	Allow  bool
	Reason string
}

// Allow is the permissive decision.
func Allow() Decision { return Decision{Allow: true} }

// Deny refuses with actionable prose.
func Deny(reason string, args ...any) Decision {
	return Decision{Allow: false, Reason: fmt.Sprintf(reason, args...)}
}

// Policy is THE seam. Every gated behaviour on the RPC surface consults this
// and nothing else.
type Policy interface {
	Check(Request) Decision
}

// PolicyFunc adapts a function to Policy, so a policy can be code.
type PolicyFunc func(Request) Decision

// Check implements Policy.
func (f PolicyFunc) Check(r Request) Decision { return f(r) }

// AllowAll is the default policy: it gates nothing. Default config selects it
// so this whole package is behaviour-neutral until something opts in.
func AllowAll() Policy {
	return PolicyFunc(func(Request) Decision { return Allow() })
}

// Rules is a data-driven policy: an ordered list consulted until one denies.
// It is the "table" half of the promise that a policy can be data or code —
// a Rule is itself a PolicyFunc, so the two compose without a second concept.
//
// FIRST DENIAL WINS, and an empty table allows. Ordering is the caller's, so a
// broad rule can be placed after a narrow exception.
type Rules []Rule

// Rule is one named entry in a table. The name appears in logs, not in the
// caller-facing reason — the reason is the rule's own prose.
type Rule struct {
	Name  string
	Check func(Request) Decision
}

// Check implements Policy.
func (rs Rules) Check(r Request) Decision {
	for _, rule := range rs {
		if rule.Check == nil {
			continue
		}
		if d := rule.Check(r); !d.Allow {
			return d
		}
	}
	return Allow()
}

// ErrCode is the JSON-RPC error code for an authorization denial. It sits in
// the -32000..-32099 application range reserved by JSON-RPC 2.0, alongside
// figaro's other typed codes in internal/rpc.
const ErrCode = -32020

// Guard wraps a handler map so every method consults the policy first. Wrapping
// the MAP rather than each handler is the point: a rule cannot be forgotten at
// one call site, and the set of guarded methods is exactly the set of served
// methods, by construction.
//
// A denial returns a typed *jkrpc.Error carrying the reason verbatim, so the
// prose a rule wrote is what the caller reads.
func Guard(handlers map[string]jkrpc.HandlerFunc, authn Authenticator, policy Policy) map[string]jkrpc.HandlerFunc {
	if authn == nil && policy == nil {
		return handlers
	}
	if authn == nil {
		authn = AriaHeader{Enabled: false}
	}
	if policy == nil {
		policy = AllowAll()
	}
	out := make(map[string]jkrpc.HandlerFunc, len(handlers))
	for name, h := range handlers {
		method, next := name, h
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			req := Request{
				Identity: authn.Authenticate(method, params),
				Method:   method,
				Params:   params,
			}
			if d := policy.Check(req); !d.Allow {
				return nil, &jkrpc.Error{Code: ErrCode, Message: d.Reason}
			}
			return next(ctx, params)
		}
	}
	return out
}
