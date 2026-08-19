// Package aria implements figaro's single paginated read API.
package aria

import "github.com/jack-work/figaro/internal/livedoc"

// Turn is one exchange: the question that opened it plus everything the agent
// thought, ran and said in response. The question is Inquiry: TEXT on the
// turn, never a node: so every entry in Nodes is agent output or a steering
// interjection that rode along.
type Turn struct {
	ID uint64 `json:"turn"`

	// Inquiry is the question that opened the turn: plain text, not a node.
	// A turn is one exchange and an exchange begins with exactly one inquiry,
	// so it is a property of the turn rather than an element of it. Steering
	// interjections stay nodes: they ride inside a turn, they do not start one.
	Inquiry string `json:"inquiry,omitempty"`

	// InquirySegments is the same question, split by WHO ASKED IT.
	InquirySegments []InquirySegment `json:"inquiry_segments,omitempty"`

	// At is when the inquiry arrived (unix millis). It lives on the turn
	// because Inquiry is bare text and cannot carry a timestamp of its own.
	At int64 `json:"at,omitempty"`

	// FormDeltas is the form state a reader would have needed to understand
	// this turn's INQUIRY: the transitions whose window closed on the record
	// that opened the turn. The inquiry is not a node, so deltas that belong
	// to it live here, exactly as At does. Keyed "<formid>.<path>"; see
	// livedoc.FormDelta.
	FormDeltas map[string]livedoc.FormDelta `json:"formDeltas,omitempty"`

	LTs    []uint64       `json:"lts,omitempty"`
	Sealed bool           `json:"sealed"`
	Nodes  []livedoc.Node `json:"nodes,omitempty"`
	Live   *Live          `json:"live,omitempty"`
}

// InquirySegment is one submission inside a turn's opening question: the text
// and who sent it, already rendered (rpc.Attribution) so no consumer re-derives
// the spelling. An empty Sender means unknown and draws nothing.
type InquirySegment struct {
	Sender string `json:"sender,omitempty"`
	Text   string `json:"text"`
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
// per-node field deltas in this frame. From is the boundary: nodes below it
// are closed and can never receive a delta. A frame with no Nodes is a close
// marker: promote your materialized copy iff your highest seen version is V.
type Live struct {
	From  uint64      `json:"from"`
	V     int         `json:"v"`
	Nodes []NodeDelta `json:"nodes,omitempty"`
}

// NodeDelta is a field-level change to one node, addressed by its positional
// ordinal within the turn. In a snapshot part the i'th node is addressed as
// From+i. livedoc.Node still carries a legacy string ID as provenance/provider
// metadata, but that is not a UI address. The ordinal is explicit here because
// a delta may reference nodes sparsely.
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

// Message is the CLIENT-SIDE materialization of one SLICE of a turn, not a
// wire type. The wire speaks Page/TurnPart; this is what a folded client hands
// its renderer.
type Message struct {
	Turn int    // turn id: NOT a logical time, and NOT unique per message
	From uint64 // node offset within the turn; >0 means this is a later slice of it
	// Inquiry is the turn's opening question, carried ONLY by the slice that
	// STARTS the turn (From == 0). A later slice must leave it empty, or a long
	// turn re-asks its own question at every page boundary and halfway down its
	// own live suffix.
	Inquiry string
	// InquirySegments splits Inquiry by sender; see Turn.InquirySegments.
	InquirySegments []InquirySegment
	// FormDeltas is the TURN-level form state (see Turn.FormDeltas), carried
	// like Inquiry: only by the slice that starts the turn.
	FormDeltas map[string]livedoc.FormDelta
	Role       string
	Nodes      []livedoc.Node
}
