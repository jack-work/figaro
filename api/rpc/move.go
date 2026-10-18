package rpc

import "encoding/json"

const (
	MethodMove         = "figaro.move"
	MethodImportBegin  = "node.import.begin"
	MethodImportChunk  = "node.import.chunk"
	MethodImportCommit = "node.import.commit"
	MethodImportAbort  = "node.import.abort"
	MethodImportStatus = "node.import.status"
	MethodNodeLearn    = "node.learn"
)

const MaxImportChunk = 1 << 20

type MoveRequest struct {
	FigaroID string `json:"figaro_id"`
	To       string `json:"to"`
}

type MoveResponse struct {
	Node   string   `json:"node"`
	Moved  []string `json:"moved"`
	Bytes  int64    `json:"bytes"`
	Millis int64    `json:"millis"`
}

type ImportBeginRequest struct {
	Token     string            `json:"token"`
	Component json.RawMessage   `json:"component"`
	Sandboxes map[string]string `json:"sandboxes,omitempty"`
}

type ImportChunkRequest struct {
	Token   string `json:"token"`
	Node    string `json:"node"`
	Channel string `json:"channel"`
	Name    string `json:"name"`
	Offset  int64  `json:"offset"`
	Data    []byte `json:"data"`
}

type ImportCommitRequest struct {
	Token  string                     `json:"token"`
	Digest string                     `json:"digest"`
	Metas  map[string]json.RawMessage `json:"metas,omitempty"`
}

type ImportTokenRequest struct {
	Token string `json:"token"`
}

type ImportStatusRequest struct {
	Token   string `json:"token"`
	Abandon bool   `json:"abandon,omitempty"`
}

type ImportState string

const (
	ImportUnknown   ImportState = "unknown"
	ImportStaged    ImportState = "staged"
	ImportCommitted ImportState = "committed"
)

type ImportStatusResponse struct {
	State ImportState `json:"state"`
}

type NodeLearnRequest struct {
	IDs  []string `json:"ids"`
	Node string   `json:"node"`
}

const ErrCodeMoved = -32030

type Moved struct {
	ID   string `json:"id"`
	Node string `json:"node"`
}
