package rpc

type Passage struct {
	Forwarded bool   `json:"forwarded,omitempty"`
	Bearer    string `json:"bearer,omitempty"`
}

type PassageAnswer struct {
	Error string `json:"error,omitempty"`
}

const MaxPassage = 16 << 10

const MethodNodeOwns = "node.owns"

type NodeOwnsRequest struct {
	IDs []string `json:"ids"`
}

type NodeOwnsResponse struct {
	Owned []string `json:"owned"`
}
