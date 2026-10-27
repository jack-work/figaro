package authz

import (
	"context"
	"fmt"

	"github.com/jack-work/jkauthz"
)

type Door uint8

const (
	DoorUnknown Door = iota
	DoorInProcess
	DoorLocal
	DoorNode
	DoorWeb
	DoorBox
)

func (d Door) String() string {
	switch d {
	case DoorLocal:
		return "local"
	case DoorNode:
		return "node"
	case DoorWeb:
		return "web"
	case DoorBox:
		return "box"
	case DoorInProcess:
		return "in-process"
	case DoorUnknown:
		return "unknown"
	}
	return fmt.Sprintf("door %d", d)
}

func (d Door) SeatsOnlyPolicySubjects() bool {
	return d != DoorInProcess && d != DoorLocal && d != DoorNode
}

func (d Door) Trusted() bool {
	return d == DoorInProcess || d == DoorLocal
}

type doorKey struct{}

func WithDoor(ctx context.Context, d Door) context.Context {
	return context.WithValue(ctx, doorKey{}, d)
}

func DoorOf(ctx context.Context) Door {
	d, _ := ctx.Value(doorKey{}).(Door)
	return d
}

func LocalOrOwner(owner jkauthz.Principal) Policy {
	return PolicyFunc(func(r Request) Decision {
		if PassesUnpoliced(r.Door, owner, r.Subject) {
			return Allow()
		}
		return Deny("%s: no policy is loaded, so the %s door passes only the owner", r.Method, r.Door)
	})
}

func PassesUnpoliced(d Door, owner jkauthz.Principal, s jkauthz.Subject) bool {
	return d.Trusted() || IsOwner(owner, s)
}

func IsOwner(owner jkauthz.Principal, s jkauthz.Subject) bool {
	return !owner.IsZero() && s.Principal == owner
}
