package rpc

type Passage struct {
	Forwarded bool   `json:"forwarded,omitempty"`
	Bearer    string `json:"bearer,omitempty"`
}

type PassageAnswer struct {
	Error string `json:"error,omitempty"`
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
