package rpc

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
