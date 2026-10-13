package rpc

// The node door: the handshake every connection makes first.

// ProtocolVersion mismatch in a Hello is refused.
const ProtocolVersion = 1

type Capability string

type PrincipalKind string

const (
	PrincipalAria PrincipalKind = "aria"
	PrincipalUser PrincipalKind = "user"
	PrincipalNode PrincipalKind = "node"
)

// Principal is set by the server from how the connection authenticated,
// never from params: CallerKey is attribution only.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	ID   string        `json:"id"`
	// Node is the node the principal belongs to; empty means this one.
	Node string `json:"node,omitempty"`
}

type HelloRequest struct {
	Version      int          `json:"version"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	// Node is the caller's node id; empty for a CLI or an aria.
	Node string `json:"node,omitempty"`
}

type HelloResponse struct {
	Version      int          `json:"version"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Node         string       `json:"node"`
	Principal    Principal    `json:"principal"`
}
