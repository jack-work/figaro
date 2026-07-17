// Package aria implements figaro's single paginated read API.
//
// An AriaRead is one "aria read": a page of the conversation. The live stream
// pushes AriaReads as state changes — server-pushed pagination — so subscribing
// is equivalent to repeatedly calling read(sinceLT). The cursor is a figaro LT.
//
// The open message streams as field-level deltas. Each live frame carries a
// record version V (server-controlled, 0-indexed, incremented per frame) and the
// nodes that changed, each as a NodeDelta: `set` merges fields (creating the node
// on first set, where `type` must appear), `unset` removes them, `patch` splices
// a streamed string field on the previous version's value. A frame's nodes are
// ordered; a new id appends at that position.
//
// A message in `committed` is closed. Two forms: a close marker {lt, v} — you
// streamed it live, so promote your materialized copy iff your highest seen
// version equals v — or a full snapshot {lt, role, nodes} (catch-up: adopt
// wholesale). On any version mismatch the client reconnects and re-reads from its
// last fully-committed LT.
//
// Empty sections are omitted.
package aria

import "github.com/jack-work/figaro/internal/livedoc"

// AriaRead is one page — pushed live or returned by Read; the two are equivalent.
type AriaRead struct {
	Committed []Committed `json:"committed,omitempty"`
	Live      *Live       `json:"live,omitempty"`
	Metrics   *Metrics    `json:"metrics,omitempty"`
}

// Empty reports whether the page carries nothing (so it isn't sent).
func (r AriaRead) Empty() bool { return len(r.Committed) == 0 && r.Live == nil && r.Metrics == nil }

// Metrics summarizes the current session for the status surfaces. ContextLimit
// is the effective prompt cap when the provider can determine one; zero means
// the selected model has no available cap metadata.
type Metrics struct {
	ContextTokens    int    `json:"context_tokens"`
	ContextLimit     int    `json:"context_limit,omitempty"`
	ContextExact     bool   `json:"context_exact"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	Mantra           string `json:"mantra,omitempty"`
}

// Live is one frame of the open message: its record version and the per-node
// field deltas. Role appears on the first frame (v 0) and on catch-up snapshots.
type Live struct {
	LT    int         `json:"lt"`
	V     int         `json:"v"`
	Role  string      `json:"role,omitempty"`
	Nodes []NodeDelta `json:"nodes"`
}

// NodeDelta is a field-level change to one block, addressed by stable id.
type NodeDelta struct {
	ID    string                   `json:"id"`
	Set   map[string]any           `json:"set,omitempty"`   // merge fields (create on first set; type required)
	Unset []string                 `json:"unset,omitempty"` // remove fields
	Patch map[string]livedoc.Delta `json:"patch,omitempty"` // splice a streamed string field on its prev value
}

// Empty reports whether the delta changes nothing.
func (d NodeDelta) Empty() bool {
	return len(d.Set) == 0 && len(d.Unset) == 0 && len(d.Patch) == 0
}

// Committed is a closed message: a close marker {lt, v} (promote iff seen==v) or
// a full snapshot {lt, role, nodes} (adopt wholesale). Presence implies closed.
type Committed struct {
	LT    int            `json:"lt"`
	V     int            `json:"v,omitempty"`
	Role  string         `json:"role,omitempty"`
	Nodes []livedoc.Node `json:"nodes,omitempty"`
}

// Full reports whether this is a content snapshot (vs a close marker).
func (c Committed) Full() bool { return c.Nodes != nil }

// Message is a closed (immutable) message, identified by its figaro LT.
type Message struct {
	LT    int
	Role  string
	Nodes []livedoc.Node
}
