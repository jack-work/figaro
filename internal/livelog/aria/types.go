// Package aria implements figaro's single paginated read API.
//
// There is one wire shape, Page, served two ways: pulled by read and pushed by
// the live stream. A page is a contiguous window over (turn, node) space, so
// subscribing is equivalent to repeatedly reading forward from your cursor.
//
// The coordinate is (turn, node). LT — the model's logical time — appears only
// as `lts` metadata hanging off nodes, because it cannot address a UI element:
// a tool node spans two LTs, and the same LT can carry both a tool result and
// a steering interjection. Turn ids address; LTs join back to the fig IR.
//
// A turn's open suffix streams as field-level deltas. Each live frame carries a
// record version V (server-controlled, 0-indexed, incremented per frame) and
// the nodes that changed, each as a NodeDelta: `set` merges fields (creating
// the node on first set, where `type` must appear), `unset` removes them,
// `patch` splices a streamed string field on the previous version's value.
//
// Live.From is the suffix boundary. A node below it is closed, and a closed
// node never reopens — so a page that stops short of From can be cached
// forever, and scrolling back through a streaming turn costs what scrolling
// back through a finished one costs.
//
// Empty sections are omitted.
package aria

import "github.com/jack-work/figaro/internal/livedoc"

// Turn is one exchange: a user prompt plus everything the agent thought, ran
// and said in response, as a single ordered node list. The prompt is node 0 —
// it is not a separate entry.
//
// ID is the coordinate a human names: `fig send <aria>:<turn>`. LTs is the
// fig-IR span [first, last] the turn covers, carried as metadata only —
// addressing is (turn, node), never LT. Sealed says the turn stopped moving;
// it is orthogonal to whether a page showed you all of it.
//
// Live is the open suffix. Nil once the turn seals.
type Turn struct {
	ID     uint64         `json:"turn"`
	LTs    []uint64       `json:"lts,omitempty"`
	Sealed bool           `json:"sealed"`
	Nodes  []livedoc.Node `json:"nodes,omitempty"`
	Live   *Live          `json:"live,omitempty"`
}

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

// Live is a turn's open suffix: the boundary, the record version, and the
// per-node field deltas in this frame. From is the boundary — nodes below it
// are closed and can never receive a delta. A frame with no Nodes is a close
// marker: promote your materialized copy iff your highest seen version is V.
type Live struct {
	From  uint64      `json:"from"`
	V     int         `json:"v"`
	Nodes []NodeDelta `json:"nodes,omitempty"`
}

// NodeDelta is a field-level change to one node, addressed by its positional
// id within the turn. Ids are positional everywhere — in a committed part the
// i'th node has id From+i and is omitted from the wire entirely; here it is
// explicit because a delta references a node out of order.
type NodeDelta struct {
	ID    uint64                   `json:"id"`
	Set   map[string]any           `json:"set,omitempty"`   // merge fields (create on first set; type required)
	Unset []string                 `json:"unset,omitempty"` // remove fields
	Patch map[string]livedoc.Delta `json:"patch,omitempty"` // splice a streamed string field on its prev value
}

// Empty reports whether the delta changes nothing.
func (d NodeDelta) Empty() bool {
	return len(d.Set) == 0 && len(d.Unset) == 0 && len(d.Patch) == 0
}

// Message is the CLIENT-SIDE materialization of one turn, not a wire type.
// The wire speaks Page/TurnPart; this is what a folded client hands its
// renderer.
//
// LT carries the TURN ID, not a logical time. The name is a labelled seam:
// renaming it reaches into the transcript pager and both renderers, which
// belong to S24/S25, and doing it here would collide with that work. Nothing
// on the wire uses this type, so the seam is local and cheap to close.
type Message struct {
	LT    int
	From  uint64 // node offset within the turn; >0 means this is a later slice of it
	Role  string
	Nodes []livedoc.Node
}
