// Package authz is the authentication and authorization seam for the RPC surface.
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
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

type principalKey struct{}

// Admit stamps the peer's credentials onto the context, or refuses the connection.
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

// ErrCode is in the JSON-RPC 2.0 application range.
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
