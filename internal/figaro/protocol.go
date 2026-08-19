package figaro

import (
	"github.com/jack-work/jkrpc"
)

// The agent no longer owns a socket. Its endpoint is angelus-side
// (angelus.ariaHub), which is what lets the aria be reclaimed from memory
// while attached clients stay connected, and what lets a dormant aria answer
// a dial at all.

var _ Figaro = (*Agent)(nil) // compile-time interface check
var _ AgentServer = (*Agent)(nil)
var _ Notifier = (*jkrpc.Server)(nil)
