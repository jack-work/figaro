package figaro

import (
	"github.com/jack-work/jkrpc"
)

// The endpoint is angelus-side (angelus.ariaHub), so an aria can be reclaimed
// while attached clients stay connected.

var _ Figaro = (*Agent)(nil) // compile-time interface check
var _ AgentServer = (*Agent)(nil)
var _ Notifier = (*jkrpc.Server)(nil)
