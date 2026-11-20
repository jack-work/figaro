package authn

import (
	"context"
	"fmt"
)

type Door uint8

const (
	DoorUnknown Door = iota
	DoorInProcess
	DoorLocal
	DoorNode
	DoorWeb
	DoorBox
	DoorHost
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
	case DoorHost:
		return "host"
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

func (d Door) DropsClaimsNotHeld() bool {
	return d != DoorInProcess && d != DoorUnknown
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
