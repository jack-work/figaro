package rpc

import "encoding/json"

type Passage struct {
	Forwarded bool   `json:"forwarded,omitempty"`
	Bearer    string `json:"bearer,omitempty"`
	Version   uint64 `json:"version,omitempty"`
	Policy    uint64 `json:"policy,omitempty"`
}

type PassageAnswer struct {
	Error   string `json:"error,omitempty"`
	Version uint64 `json:"version,omitempty"`
	Policy  uint64 `json:"policy,omitempty"`
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
	Entries     []json.RawMessage `json:"entries"`
	Version     uint64            `json:"version,omitempty"`
	Policy      uint64            `json:"policy,omitempty"`
	Reached     []string          `json:"reached,omitempty"`
	Unreachable []NodeUnreachable `json:"unreachable,omitempty"`
}

const MethodTroupeLeave = "troupe.leave"

type TroupeLeaveRequest struct {
	Troupe string `json:"troupe"`
}

type TroupeLeaveResponse struct {
	Node        string            `json:"node"`
	Reached     []string          `json:"reached"`
	Unreachable []NodeUnreachable `json:"unreachable,omitempty"`
}

type TroupeMembersResponse struct {
	Node       string         `json:"node"`
	Provider   string         `json:"provider,omitempty"`
	AdmittedBy string         `json:"admitted_by,omitempty"`
	Owner      string         `json:"owner,omitempty"`
	Version    uint64         `json:"version"`
	Peers      []TroupeMember `json:"peers"`
	Gone       []TroupeGone   `json:"gone,omitempty"`
	Keys       *TroupeKeys    `json:"keys,omitempty"`
}

type TroupeGone struct {
	Node string `json:"node"`
	By   string `json:"by"`
}

type TroupeKeys struct {
	Troupe              string `json:"troupe"`
	NodeExpires         int64  `json:"node_expires,omitempty"`
	IntermediateExpires int64  `json:"intermediate_expires,omitempty"`
	Error               string `json:"error,omitempty"`
}

type TroupeMember struct {
	Node       string `json:"node"`
	Endpoint   string `json:"endpoint"`
	AdmittedBy string `json:"admitted_by,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Signs      bool   `json:"signs,omitempty"`
	Met        bool   `json:"met"`
	Version    uint64 `json:"version,omitempty"`
	SeenAt     int64  `json:"seen_at,omitempty"`
	Drift      bool   `json:"drift,omitempty"`
}

const (
	MethodTroupePolicy    = "troupe.policy"
	MethodTroupePolicySet = "troupe.policy.set"
)

type TroupePolicyRequest struct {
	Troupe string `json:"troupe"`
}

type TroupePolicySetRequest struct {
	Troupe string `json:"troupe"`
	Body   string `json:"body"`
}

type TroupePolicyVersion struct {
	Version uint64 `json:"version"`
	Signer  string `json:"signer"`
	By      string `json:"by"`
	At      int64  `json:"at"`
}

type TroupePolicySighting struct {
	Node   string `json:"node"`
	Self   bool   `json:"self,omitempty"`
	Policy uint64 `json:"policy"`
	SeenAt int64  `json:"seen_at,omitempty"`
}

type TroupePolicyResponse struct {
	Troupe    string                 `json:"troupe"`
	Provider  string                 `json:"provider"`
	Path      string                 `json:"path,omitempty"`
	InForce   *TroupePolicyVersion   `json:"in_force,omitempty"`
	Body      string                 `json:"body,omitempty"`
	LoadError string                 `json:"load_error,omitempty"`
	Rivals    []TroupePolicyVersion  `json:"rivals,omitempty"`
	Voided    []TroupePolicyVersion  `json:"voided,omitempty"`
	Nodes     []TroupePolicySighting `json:"nodes,omitempty"`
	LocalOnly []string               `json:"local_only"`
}

type TroupePolicySetResponse struct {
	Version     uint64            `json:"version"`
	Signer      string            `json:"signer"`
	Reached     []string          `json:"reached"`
	Unreachable []NodeUnreachable `json:"unreachable,omitempty"`
}
