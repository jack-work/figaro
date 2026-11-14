package authz

import (
	"context"
	"fmt"

	"github.com/jack-work/jkauthz"
)

type Endpoint uint8

const (
	EndpointUnknown Endpoint = iota
	EndpointInProcess
	EndpointLocal
	EndpointNode
	EndpointWeb
	EndpointBox
	EndpointHost
)

func (d Endpoint) String() string {
	switch d {
	case EndpointLocal:
		return "local"
	case EndpointNode:
		return "node"
	case EndpointWeb:
		return "web"
	case EndpointBox:
		return "box"
	case EndpointHost:
		return "host"
	case EndpointInProcess:
		return "in-process"
	case EndpointUnknown:
		return "unknown"
	}
	return fmt.Sprintf("endpoint %d", d)
}

func (d Endpoint) SeatsOnlyPolicySubjects() bool {
	return d != EndpointInProcess && d != EndpointLocal && d != EndpointNode
}

func (d Endpoint) dropsClaimsNotHeld() bool {
	return d != EndpointInProcess && d != EndpointUnknown
}

func (d Endpoint) Trusted() bool {
	return d == EndpointInProcess || d == EndpointLocal
}

type endpointKey struct{}

func WithEndpoint(ctx context.Context, d Endpoint) context.Context {
	return context.WithValue(ctx, endpointKey{}, d)
}

func EndpointOf(ctx context.Context) Endpoint {
	d, _ := ctx.Value(endpointKey{}).(Endpoint)
	return d
}

func LocalOrOwner(owner jkauthz.Principal) Policy {
	return PolicyFunc(func(r Request) Decision {
		if PassesUnpoliced(r.Endpoint, owner, r.Subject) {
			return Allow()
		}
		return Deny("%s: no policy is loaded, so the %s endpoint passes only the owner", r.Method, r.Endpoint)
	})
}

func PassesUnpoliced(d Endpoint, owner jkauthz.Principal, s jkauthz.Subject) bool {
	return d.Trusted() || IsOwner(owner, s)
}

func IsOwner(owner jkauthz.Principal, s jkauthz.Subject) bool {
	return !owner.IsZero() && s.Principal == owner
}
