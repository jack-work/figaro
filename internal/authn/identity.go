package authn

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

// Principal is stamped from the connection; FigaroID and Label are what
// the params asserted, and are not verified.
type Identity struct {
	Principal rpc.Principal
	FigaroID  string
	Label     string
	Claimed   bool
}

type Authenticator interface {
	Authenticate(peer rpc.Principal, params json.RawMessage) Identity
}

type AriaHeader struct{}

func (AriaHeader) Authenticate(peer rpc.Principal, params json.RawMessage) Identity {
	figaro := rpc.CallerOf(params)
	return Identity{Principal: peer, FigaroID: figaro, Label: rpc.LabelOf(params), Claimed: figaro != ""}
}

type callerKey struct{}

type caller struct {
	subject jkauthz.Subject
	tool    string
	session *Session
	key     *KeySession
	bearer  string
}

func Authenticated(authn Authenticator, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		params = PinnedToTool(ctx, params)
		return next(WithIdentity(ctx, authn.Authenticate(PrincipalOf(ctx), params)), params)
	}
}

type identityKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, &id)
}

func IdentityOf(ctx context.Context) (Identity, bool) {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	if id == nil || id.Principal != PrincipalOf(ctx) {
		return Identity{}, false
	}
	return *id, true
}

func FigaroOf(ctx context.Context) string {
	if f, ok := ToolOf(ctx); ok {
		return f
	}
	id, _ := IdentityOf(ctx)
	return id.FigaroID
}

func Claimed(ctx context.Context) bool {
	id, _ := IdentityOf(ctx)
	return id.Claimed
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

func WithToolOf(ctx context.Context, figaro string) context.Context {
	c, _ := ctx.Value(callerKey{}).(caller)
	c.tool = figaro
	return context.WithValue(ctx, callerKey{}, c)
}

func ToolOf(ctx context.Context) (string, bool) {
	c, _ := ctx.Value(callerKey{}).(caller)
	return c.tool, c.tool != ""
}

func PinnedToTool(ctx context.Context, params json.RawMessage) json.RawMessage {
	f, ok := ToolOf(ctx)
	if !ok {
		return params
	}
	fields := map[string]json.RawMessage{}
	if len(params) > 0 && string(params) != "null" {
		if json.Unmarshal(params, &fields) != nil {
			return params
		}
	}
	if claimed := rpc.CallerOf(params); claimed != "" && claimed != f {
		slog.Info("authz: a tool caller's claim replaced by the figaro whose cgroup it runs in", "claimed", claimed, "figaro", f)
	}
	id, _ := json.Marshal(f)
	fields[rpc.CallerKey] = id
	delete(fields, rpc.CallerLabelKey)
	out, err := json.Marshal(fields)
	if err != nil {
		return params
	}
	return out
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

func PrincipalText(p rpc.Principal) string {
	if p.Kind == "" || p.ID == "" {
		return ""
	}
	return jkauthz.Principal{Kind: jkauthz.Kind(p.Kind), ID: p.ID, Node: p.Node}.String()
}

type admittedUntilKey struct{}

func WithAdmittedUntil(ctx context.Context, t time.Time) context.Context {
	if held, ok := ctx.Value(admittedUntilKey{}).(time.Time); ok && held.Before(t) {
		return ctx
	}
	return context.WithValue(ctx, admittedUntilKey{}, t)
}

func Admission(ctx context.Context) (time.Time, func(context.Context) (jkauthz.Subject, error)) {
	until, _ := ctx.Value(admittedUntilKey{}).(time.Time)
	c, _ := ctx.Value(callerKey{}).(caller)
	switch {
	case c.session != nil:
		return earliest(until, c.session.Expiry()), c.session.Subject
	case c.key != nil:
		k := c.key
		return earliest(until, k.expiry()), func(context.Context) (jkauthz.Subject, error) { return k.Subject() }
	}
	return until, nil
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}
