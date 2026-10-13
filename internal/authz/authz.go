// Package authz is the authentication and authorization seam for the figaro
// RPC surface.
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

// Identity is who the server says is calling. Principal is stamped from the
// connection; FigaroID and Label are what the params asserted.
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

type principalKey struct{}

// Admit reads the peer's credentials off an accepted connection and returns
// the context its handlers run under, or the reason it is refused.
func Admit(ctx context.Context, conn net.Conn) (context.Context, error) {
	p, err := peer(conn)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

func PrincipalOf(ctx context.Context) rpc.Principal {
	p, _ := ctx.Value(principalKey{}).(rpc.Principal)
	return p
}

type Request struct {
	Identity Identity
	Method   string
	Params   json.RawMessage
}

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

func (r Request) SelfTargeted() bool {
	return r.Identity.FigaroID != "" && r.Identity.FigaroID == r.Target()
}

type Decision struct {
	Allow  bool
	Reason string
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

type Rules []Rule

type Rule struct {
	Name  string
	Check func(Request) Decision
}

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

// ErrCode sits in the -32000..-32099 application range JSON-RPC 2.0 reserves.
const ErrCode = -32020

func Guard(handlers map[string]jkrpc.HandlerFunc, authn Authenticator, policy Policy) map[string]jkrpc.HandlerFunc {
	out := make(map[string]jkrpc.HandlerFunc, len(handlers))
	for name, h := range handlers {
		method, next := name, h
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			req := Request{
				Identity: authn.Authenticate(PrincipalOf(ctx), params),
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
