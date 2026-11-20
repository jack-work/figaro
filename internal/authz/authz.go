package authz

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
	"go.opentelemetry.io/otel/attribute"
)

type Request struct {
	Identity authn.Identity
	Subject  jkauthz.Subject
	Door     authn.Door
	Method   string
	Aria     string
	Params   json.RawMessage
}

type Decision struct {
	Allow     bool
	Reason    string
	Hidden    bool
	DropClaim bool
}

func NotFound(method, id string) error {
	return fmt.Errorf("%s: aria %s not found", method, id)
}

func Allow() Decision { return Decision{Allow: true} }

func Deny(reason string, args ...any) Decision {
	return Decision{Allow: false, Reason: fmt.Sprintf(reason, args...)}
}

type Policy interface {
	Check(Request) Decision
}

type PolicyFunc func(Request) Decision

func (f PolicyFunc) Check(r Request) Decision { return f(r) }

func AllowAll() Policy {
	return PolicyFunc(func(Request) Decision { return Allow() })
}

// ErrCode is in the JSON-RPC 2.0 application range.
const ErrCode = -32020

func Guard(handlers map[string]jkrpc.HandlerFunc, authenticator authn.Authenticator, policy Policy) map[string]jkrpc.HandlerFunc {
	out := make(map[string]jkrpc.HandlerFunc, len(handlers))
	for name, h := range handlers {
		method, next := name, h
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			params = authn.PinnedToTool(ctx, params)
			subject, err := authn.SubjectOf(ctx)
			if err != nil {
				return nil, &jkrpc.Error{Code: ErrCode, Message: err.Error()}
			}
			id, ok := authn.IdentityOf(ctx)
			if !ok {
				id = authenticator.Authenticate(authn.PrincipalOf(ctx), params)
				ctx = authn.WithIdentity(ctx, id)
			}
			req := Request{
				Identity: id,
				Subject:  subject,
				Door:     authn.DoorOf(ctx),
				Method:   method,
				Aria:     rpc.AriaOf(params),
				Params:   params,
			}
			d := policy.Check(req)
			if !d.Allow {
				if d.Hidden {
					return nil, NotFound(method, req.Aria)
				}
				return nil, &jkrpc.Error{Code: ErrCode, Message: d.Reason}
			}
			if d.DropClaim {
				id.FigaroID = ""
				ctx = authn.WithIdentity(ctx, id)
			}
			start := time.Now()
			res, err := next(ctx, params)
			status := "success"
			if err != nil {
				status = "failure"
			}
			figOtel.RecordRPC(ctx, time.Since(start), attribute.String("method", method), attribute.String("status", status))
			return res, err
		}
	}
	return out
}

func Enforce(p *Policies) Policy { return PolicyFunc(p.Call) }

func (p *Policies) Call(r Request) Decision {
	action, ok := actions[r.Method]
	verb := action.Verb
	if !ok {
		return Deny("%s has no verb, so no policy can allow it", r.Method)
	}
	figaro, dropped := r.Identity.FigaroID, false
	if (r.Door == authn.DoorBox || r.Door == authn.DoorHost) && !p.PinnedToBox(r.Subject, figaro) {
		if box, _ := authn.BoxOf(r.Subject.Principal); r.Door == authn.DoorBox && verb != rpc.VerbNone && p.claimRequiredIn(box) {
			if figaro == "" {
				return Deny("%s: box %s's door takes only calls that name their figaro (sandbox.boxes.%s.figaro_claim = \"required\")", r.Method, box, box)
			}
			return Deny("%s: figaro %s does not run in box %s, whose door takes only calls that name one that does (sandbox.boxes.%s.figaro_claim = \"required\")", r.Method, figaro, box, box)
		}
		figaro, dropped = "", figaro != ""
	}
	if figaro != "" && p.dropsClaim(r.Door, figaro, r.Method) {
		figaro, dropped = "", true
	}
	decided := func(d Decision) Decision {
		d.DropClaim = dropped
		return d
	}
	if action.Handler && !p.pool.Load() {
		return decided(Allow())
	}
	if verb == rpc.VerbNone && r.Door.Trusted() {
		return Allow()
	}
	if verb == rpc.VerbNone || (action.filtersRows(r.Aria) && !p.pool.Load()) {
		if !p.HoldsRole(r.Subject) {
			return Deny("%s: %s holds no role here", r.Method, r.Subject.Principal)
		}
		if r.Method == rpc.MethodList && !p.NamesFigaros(r.Subject, rpc.VerbRead) {
			return Deny("%s: %s holds no grant that reads a figaro", r.Method, r.Subject.Principal)
		}
		return decided(Allow())
	}
	if verb == rpc.VerbLease || verb == rpc.VerbSay {
		if r.Door == authn.DoorNode {
			return Deny("%s: %s is asked of the daemon that holds the pool, never through a node door", r.Method, verb)
		}
	}
	on, err := action.on(r)
	if err != nil {
		return Deny("%s: %v", r.Method, err)
	}
	view := p.Figaro(figaro)
	for _, res := range on {
		if d := allowedOn(view, r, figaro, verb, res); !d.Allow {
			return d
		}
	}
	if r.Method == rpc.MethodDecide && r.Aria != "" && !view.Allow(r.Subject, rpc.VerbRead, Aria(r.Aria)).Allowed {
		return Decision{Hidden: true}
	}
	return decided(Allow())
}

func allowedOn(view FigaroView, r Request, figaro string, verb rpc.Verb, res Resource) Decision {
	d := view.Allow(r.Subject, verb, res)
	if d.Allowed || allowedByAnother(view, r, verb, res) {
		return Allow()
	}
	if d.Narrowed() {
		slog.Info("authz: figaro refused", "figaro", figaro, "method", r.Method, "verb", verb, "on", res.String(), "reason", d.Reason())
	}
	if d.Unreadable() {
		return Deny("%s: %s", r.Method, d.Reason())
	}
	if res.Kind == ResourceAria && (verb == rpc.VerbRead || !view.Allow(r.Subject, rpc.VerbRead, res).Allowed) {
		return Decision{Hidden: true}
	}
	return Deny("%s: %s", r.Method, d.Reason())
}

func decideResources(aria string, params json.RawMessage) ([]Resource, error) {
	var req rpc.DecideRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
	}
	overrides := req.Provider != "" || req.Model != "" || req.API != ""
	var on []Resource
	switch {
	case aria != "":
		on = append(on, Aria(aria))
	case !overrides:
		on = append(on, Outfit(cmp.Or(req.Outfit, outfit.NameDefault)))
	}
	if overrides {
		on = append(on, Daemon)
	}
	return on, nil
}

func (p *Policies) dropsClaim(d authn.Door, figaro, method string) bool {
	if !d.DropsClaimsNotHeld() || p.holdsFigaro(figaro) {
		return false
	}
	slog.Info("authz: claim for a figaro not held here dropped", "door", d.String(), "figaro", figaro, "method", method)
	return true
}

func (p *Policies) DroppingClaimsNotHeld(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if id, ok := authn.IdentityOf(ctx); ok && id.FigaroID != "" && p.dropsClaim(authn.DoorOf(ctx), id.FigaroID, method) {
			id.FigaroID = ""
			ctx = authn.WithIdentity(ctx, id)
		}
		return next(ctx, params)
	}
}

func (p *Policies) holdsFigaro(figaro string) bool {
	ff, _, ok := p.figaroFacts()
	if !ok {
		return false
	}
	_, held := ff.Ancestry(figaro)
	return held
}

func poolNamed(params json.RawMessage) (string, error) {
	var req struct {
		Pool string `json:"pool"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return "", fmt.Errorf("parse params: %w", err)
		}
	}
	if req.Pool == "" {
		return "", errors.New("the call names no pool")
	}
	return req.Pool, nil
}

func (p *Policies) PinnedToBox(s jkauthz.Subject, figaro string) bool {
	box, ok := authn.BoxOf(s.Principal)
	if !ok || figaro == "" {
		return false
	}
	m, _ := p.schema.Selectors.(selectors)
	if m.facts == nil {
		return false
	}
	held, read := m.facts.SandboxOf(figaro)
	return read && held == box
}

func allowedByAnother(view FigaroView, r Request, refused rpc.Verb, res Resource) bool {
	verbs, _ := VerbsOf(r.Method)
	for _, v := range verbs {
		if v != refused && view.Allow(r.Subject, v, res).Allowed {
			return true
		}
	}
	return false
}
