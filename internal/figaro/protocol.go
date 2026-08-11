package figaro

import (
	"github.com/jack-work/jkrpc"
)

// The agent no longer owns a socket. Its endpoint is angelus-side
// (angelus.ariaHub), which is what lets the aria be reclaimed from memory
// while attached clients stay connected, and what lets a dormant aria answer
// a dial at all.
//
// SocketPath survives on Config and on the Figaro interface because it is
// the ADDRESS an aria is reachable at: resolve and attach hand it to
// clients, and that address is still a pure function of the id. What is
// gone is the listener: StartSocket, its accept loop, and the per-connection
// Subscribe that tied every client's lifetime to the agent's.

var _ Figaro = (*Agent)(nil) // compile-time interface check
var _ AgentServer = (*Agent)(nil)
var _ Notifier = (*jkrpc.Server)(nil)
