// Package authz is the authentication and authorization seam for the RPC surface.
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
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
	Door     Door
	Method   string
	Aria     string
	Params   json.RawMessage
}

type Decision struct {
	Allow  bool
	Reason string
	Hidden bool
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
			req := Request{
				Identity: authn.Authenticate(PrincipalOf(ctx), params),
				Subject:  subject,
				Door:     DoorOf(ctx),
				Method:   method,
				Aria:     rpc.AriaOf(params),
				Params:   params,
			}
			if d := policy.Check(req); !d.Allow {
				if d.Hidden {
					return nil, NotFound(method, req.Aria)
				}
				return nil, &jkrpc.Error{Code: ErrCode, Message: d.Reason}
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
		if checkedByHandler(r.Method) {
			return Allow()
		}
		if verb == rpc.VerbNone && r.Door.Trusted() {
			return Allow()
		}
		if verb == rpc.VerbNone || filtersPerRow(r) {
			if !p.HoldsRole(r.Subject) {
				return Deny("%s: %s holds no role here", r.Method, r.Subject.Principal)
			}
			return Allow()
		}
		res := Daemon
		if r.Aria != "" {
			res = Aria(r.Aria)
		}
		figaro := r.Identity.FigaroID
		if d := p.AllowFigaro(r.Subject, figaro, verb, res); !d.Allowed {
			if d.Fenced() {
				slog.Info("authz: fence refused", "figaro", figaro, "method", r.Method, "verb", verb, "on", res.String(), "reason", d.Reason())
			}
			if res.Kind == ResourceAria && (verb == rpc.VerbRead || !p.AllowFigaro(r.Subject, figaro, rpc.VerbRead, res).Allowed) {
				return Decision{Hidden: true}
			}
			return Deny("%s: %s", r.Method, d.Reason())
		}
		return Allow()
	})
}
