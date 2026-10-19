package rpc

import "encoding/json"

type Passage struct {
	Forwarded bool   `json:"forwarded,omitempty"`
	Bearer    string `json:"bearer,omitempty"`
	Version   uint64 `json:"version,omitempty"`
}

type PassageAnswer struct {
	Error   string `json:"error,omitempty"`
	Version uint64 `json:"version,omitempty"`
}

const MaxPassage = 16 << 10

const MethodNodeResolve = "node.resolve"

type NodeResolveRequest struct {
	Refs []string `json:"refs"`
}

type NodeResolveResponse struct {
	IDs   map[string]string `json:"ids"`
	Moved map[string]Moved  `json:"moved,omitempty"`
}

type NodeUnreachable struct {
	Node  string `json:"node"`
	Error string `json:"error"`
}

const MethodTroupeMembers = "troupe.members"

const MethodTroupeEntries = "troupe.entries"

const (
	MethodTroupeSign     = "troupe.sign"
	MethodTroupeIdentity = "troupe.identity"
)

type TroupeSignRequest struct {
	Troupe    string        `json:"troupe"`
	Node      string        `json:"node"`
	PublicKey []byte        `json:"public_key"`
	Door      string        `json:"door"`
	Rejoin    *TroupeRejoin `json:"rejoin,omitempty"`
}

type TroupeRejoin struct {
	Chain [][]byte `json:"chain"`
	Sig   []byte   `json:"sig"`
}

type TroupeSignResponse struct {
	Chain   [][]byte          `json:"chain"`
	Root    []byte            `json:"root"`
	Entries []json.RawMessage `json:"entries"`
}

type TroupeIdentityRequest struct {
	Troupe string `json:"troupe"`
}

type TroupeIdentityResponse struct {
	Troupe     string    `json:"troupe"`
	Node       string    `json:"node"`
	Principal  Principal `json:"principal"`
	Root       []byte    `json:"root"`
	RootSHA256 string    `json:"root_sha256"`
	Signers    []string  `json:"signers"`
}

type TroupeEntriesRequest struct {
	Troupe  string            `json:"troupe"`
	Entries []json.RawMessage `json:"entries,omitempty"`
}

type TroupeEntriesResponse struct {
	Entries []json.RawMessage `json:"entries"`
}

type TroupeMembersResponse struct {
	Node    string         `json:"node"`
	Version uint64         `json:"version"`
	Peers   []TroupeMember `json:"peers"`
	Keys    *TroupeKeys    `json:"keys,omitempty"`
}

type TroupeKeys struct {
	Troupe              string `json:"troupe"`
	NodeExpires         int64  `json:"node_expires,omitempty"`
	IntermediateExpires int64  `json:"intermediate_expires,omitempty"`
	Error               string `json:"error,omitempty"`
}

type TroupeMember struct {
	Node     string `json:"node"`
	Endpoint string `json:"endpoint"`
	Met      bool   `json:"met"`
	Version  uint64 `json:"version,omitempty"`
	SeenAt   int64  `json:"seen_at,omitempty"`
	Drift    bool   `json:"drift,omitempty"`
}
