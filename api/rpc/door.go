package rpc

// THE NODE DOOR: the handshake every connection makes first, whether it
// arrived over a unix socket, a peer link, or the gateway.

// ProtocolVersion is the wire protocol this build speaks. A peer whose
// Hello carries a different version is refused.
const ProtocolVersion = 1

// Capability names an optional feature a node serves.
type Capability string

// PrincipalKind is who is on the other end of a connection.
type PrincipalKind string

const (
	PrincipalAria PrincipalKind = "aria"
	PrincipalUser PrincipalKind = "user"
	PrincipalNode PrincipalKind = "node"
)

// Principal is the server's answer to "who is calling". It is set by the
// server from how the connection authenticated (peercred, doorkey, oidc, or
// the broker socket's own identity) and never from params: CallerKey is
// attribution only.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	ID   string        `json:"id"`
	// Node is the node the principal belongs to; empty means this one.
	Node string `json:"node,omitempty"`
}

// HelloRequest opens a connection. Params for MethodHello.
type HelloRequest struct {
	Version      int          `json:"version"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	// Node is the caller's node id; empty for a CLI or an aria.
	Node string `json:"node,omitempty"`
}

// HelloResponse is the server's side of the handshake.
type HelloResponse struct {
	Version      int          `json:"version"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Node         string       `json:"node"`
	Principal    Principal    `json:"principal"`
}
