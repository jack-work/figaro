package rpc

const CapNote Capability = "note"

const (
	NoteMaxBytes = 1024
	NoteKeep     = 8
)

type NoteRequest struct {
	Text string `json:"text"`
}

type NoteResponse struct {
	OK  bool   `json:"ok"`
	Key string `json:"key"`
}
