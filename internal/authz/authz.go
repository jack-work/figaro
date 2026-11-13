// Package authz is the authentication and authorization seam for the RPC surface.
package authz

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
	"go.opentelemetry.io/otel/attribute"
)

// Principal is stamped from the connection; FigaroID and Label are what
// the params asserted, and are not verified.
type Identity struct {
	Principal rpc.Principal
	FigaroID  string
	Label     string
}

type Authenticator interface {
	Authenticate(peer rpc.Principal, params json.RawMessage) Identity
}

type AriaHeader struct{}

func (AriaHeader) Authenticate(peer rpc.Principal, params json.RawMessage) Identity {
	return Identity{Principal: peer, FigaroID: rpc.CallerOf(params), Label: rpc.LabelOf(params)}
}

type callerKey struct{}

type caller struct {
	subject jkauthz.Subject
	session *Session
	key     *KeySession
	bearer  string
}

func Authenticated(authn Authenticator, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		return next(withIdentity(ctx, authn.Authenticate(PrincipalOf(ctx), params)), params)
	}
}

type identityKey struct{}

func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, &id)
}

func identityOf(ctx context.Context) (Identity, bool) {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	if id == nil || id.Principal != PrincipalOf(ctx) {
		return Identity{}, false
	}
	return *id, true
}

func FigaroOf(ctx context.Context) string {
	id, _ := identityOf(ctx)
	return id.FigaroID
}

func Admit(ctx context.Context, conn net.Conn) (context.Context, error) {
	p, err := peer(conn)
	if err != nil {
		return nil, err
	}
	return WithPrincipal(ctx, p), nil
}

func WithSubject(ctx context.Context, s jkauthz.Subject) context.Context {
	return context.WithValue(ctx, callerKey{}, caller{subject: s})
}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, callerKey{}, caller{subject: jkauthz.Subject{Principal: s.Principal()}, session: s})
}

func WithPrincipal(ctx context.Context, p rpc.Principal) context.Context {
	return WithSubject(ctx, jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.Kind(p.Kind), ID: p.ID, Node: p.Node}})
}

func WithVerifiedBearer(ctx context.Context, s jkauthz.Subject, token string) context.Context {
	return context.WithValue(ctx, callerKey{}, caller{subject: s, bearer: token})
}

func IssuerOf(ctx context.Context) string {
	c, _ := ctx.Value(callerKey{}).(caller)
	if c.session == nil {
		return ""
	}
	return c.session.Issuer()
}

func ExpiryOf(ctx context.Context) (time.Time, bool) {
	c, _ := ctx.Value(callerKey{}).(caller)
	if c.session == nil {
		return time.Time{}, false
	}
	exp := c.session.Expiry()
	return exp, !exp.IsZero()
}

func BearerOf(ctx context.Context) (string, bool) {
	c, _ := ctx.Value(callerKey{}).(caller)
	switch {
	case c.session != nil:
		return c.session.Token(), true
	case c.key != nil:
		return c.key.token, true
	}
	return c.bearer, c.bearer != ""
}

func SubjectOf(ctx context.Context) (jkauthz.Subject, error) {
	c, _ := ctx.Value(callerKey{}).(caller)
	switch {
	case c.session != nil:
		return c.session.Subject(ctx)
	case c.key != nil:
		return c.key.Subject()
	}
	return c.subject, nil
}

func PrincipalOf(ctx context.Context) rpc.Principal {
	c, _ := ctx.Value(callerKey{}).(caller)
	p := c.subject.Principal
	return rpc.Principal{Kind: rpc.PrincipalKind(p.Kind), ID: p.ID, Node: p.Node}
}

type Request struct {
	Identity Identity
	Subject  jkauthz.Subject
	Endpoint Endpoint
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

func Guard(handlers map[string]jkrpc.HandlerFunc, authn Authenticator, policy Policy) map[string]jkrpc.HandlerFunc {
	out := make(map[string]jkrpc.HandlerFunc, len(handlers))
	for name, h := range handlers {
		method, next := name, h
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			subject, err := SubjectOf(ctx)
			if err != nil {
				return nil, &jkrpc.Error{Code: ErrCode, Message: err.Error()}
			}
			id, ok := identityOf(ctx)
			if !ok {
				id = authn.Authenticate(PrincipalOf(ctx), params)
				ctx = withIdentity(ctx, id)
			}
			req := Request{
				Identity: id,
				Subject:  subject,
				Endpoint: EndpointOf(ctx),
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
				ctx = withIdentity(ctx, id)
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

func PrincipalText(p rpc.Principal) string {
	if p.Kind == "" || p.ID == "" {
		return ""
	}
	return jkauthz.Principal{Kind: jkauthz.Kind(p.Kind), ID: p.ID, Node: p.Node}.String()
}

func checkedByHandler(method string) bool {
	switch method {
	case rpc.MethodImportStatus, rpc.MethodNodeLearn, rpc.MethodTroupeEntries, rpc.MethodTroupeSign, rpc.MethodTroupeIdentity, rpc.MethodTroupeLeave, rpc.MethodTroupePolicySet,
		rpc.MethodTroupeAPIKeyMint, rpc.MethodTroupeAPIKeyRevoke, rpc.MethodTroupeAPIKeyList,
		rpc.MethodGCPlan, rpc.MethodGCApply, rpc.MethodGCUndo:
		return true
	}
	return false
}

func filtersPerRow(r Request) bool {
	switch r.Method {
	case rpc.MethodList, rpc.MethodNodeResolve:
		return true
	case rpc.MethodForm, rpc.MethodFollow, rpc.MethodUnfollow, rpc.MethodAttach:
		return r.Aria == rpc.AriasFormID || r.Aria == rpc.TopologyFormID || r.Aria == rpc.TroupeFormID || r.Aria == rpc.RecentFormID
	case rpc.MethodSet:
		return r.Aria == rpc.TopologyFormID
	}
	return false
}

func Enforce(p *Policies) Policy {
	return PolicyFunc(func(r Request) Decision {
		verb, ok := rpc.VerbOf(r.Method)
		if !ok {
			return Deny("%s has no verb, so no policy can allow it", r.Method)
		}
		figaro, dropped := r.Identity.FigaroID, false
		if (r.Endpoint == EndpointBox || r.Endpoint == EndpointHost) && !p.pinnedToBox(r.Subject, figaro) {
			if box, _ := BoxOf(r.Subject.Principal); r.Endpoint == EndpointBox && verb != rpc.VerbNone && p.claimRequiredIn(box) {
				if figaro == "" {
					return Deny("%s: box %s's door takes only calls that name their figaro (sandbox.boxes.%s.figaro_claim = \"required\")", r.Method, box, box)
				}
				return Deny("%s: figaro %s does not run in box %s, whose door takes only calls that name one that does (sandbox.boxes.%s.figaro_claim = \"required\")", r.Method, figaro, box, box)
			}
			figaro, dropped = "", figaro != ""
		}
		if figaro != "" && p.dropsClaim(r.Endpoint, figaro, r.Method) {
			figaro, dropped = "", true
		}
		decided := func(d Decision) Decision {
			d.DropClaim = dropped
			return d
		}
		if checkedByHandler(r.Method) && !p.pool.Load() {
			return decided(Allow())
		}
		if verb == rpc.VerbNone && r.Endpoint.Trusted() {
			return Allow()
		}
		if verb == rpc.VerbNone || (filtersPerRow(r) && !p.pool.Load()) {
			if !p.HoldsRole(r.Subject) {
				return Deny("%s: %s holds no role here", r.Method, r.Subject.Principal)
			}
			if r.Method == rpc.MethodList && !p.NamesFigaros(r.Subject, rpc.VerbRead) {
				return Deny("%s: %s holds no grant that reads a figaro", r.Method, r.Subject.Principal)
			}
			return decided(Allow())
		}
		res := Daemon
		if r.Aria != "" {
			res = Aria(r.Aria)
		}
		if verb == rpc.VerbLease || verb == rpc.VerbSay {
			if r.Endpoint == EndpointNode {
				return Deny("%s: %s is asked of the daemon that holds the pool, never through a node door", r.Method, verb)
			}
		}
		if verb == rpc.VerbLease {
			pool, err := poolNamed(r.Params)
			if err != nil {
				return Deny("%s: %v", r.Method, err)
			}
			res = Pool(pool)
		}
		if verb == rpc.VerbSay {
			box, _ := BoxOf(r.Subject.Principal)
			res = Lease(box)
		}
		on := []Resource{res}
		if r.Method == rpc.MethodDecide {
			var err error
			if on, err = decideResources(r.Aria, r.Params); err != nil {
				return Deny("%s: %v", r.Method, err)
			}
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
	})
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

func (p *Policies) dropsClaim(d Endpoint, figaro, method string) bool {
	if !d.dropsClaimsNotHeld() || p.holdsFigaro(figaro) {
		return false
	}
	slog.Info("authz: claim for a figaro not held here dropped", "door", d.String(), "figaro", figaro, "method", method)
	return true
}

func (p *Policies) DroppingClaimsNotHeld(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if id, ok := identityOf(ctx); ok && id.FigaroID != "" && p.dropsClaim(EndpointOf(ctx), id.FigaroID, method) {
			id.FigaroID = ""
			ctx = withIdentity(ctx, id)
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

func (p *Policies) pinnedToBox(s jkauthz.Subject, figaro string) bool {
	box, ok := BoxOf(s.Principal)
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
	verbs, _ := rpc.VerbsOf(r.Method)
	for _, v := range verbs {
		if v != refused && view.Allow(r.Subject, v, res).Allowed {
			return true
		}
	}
	return false
}
