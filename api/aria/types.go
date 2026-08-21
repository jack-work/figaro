// Package aria holds the wire types of figaro's paginated read API: the
// shapes a client decodes, and nothing that reads a store. The pager, the
// composer and the cache that produce them live in internal/livelog/aria and
// re-export these names, so there is one definition and the SDK can have it
// without the daemon.
package aria

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/livedoc"
)

// Turn is one exchange: the question that opened it plus everything the agent
// thought, ran and said in response. The question is Inquiry: TEXT on the
// turn, never a node: so every entry in Nodes is agent output or a steering
// interjection that rode along.
// maxNode is the largest representable node ordinal. It appears only as the
// predecessor sentinel for node 0 (see Anchor.Prev).
const maxNode = ^uint64(0)

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

// Direction says which way a page walks from its anchor.
type Direction string

const (
	// Forward pages from the anchor toward the newest node.
	Forward Direction = "forward"
	// Backward pages from the anchor toward the oldest node. The page is
	// still assembled in reading order; only the choice of which nodes fall
	// inside the budget differs.
	Backward Direction = "backward"
)

// Anchor addresses one node in the conversation. It is the UI coordinate -
// turn id plus the node's positional ordinal within that turn. LT never
// appears here: that is the model's coordinate, carried on nodes as metadata.
type Anchor struct {
	Turn uint64 `json:"turn"`
	Node uint64 `json:"node"`
}

// Zero reports whether the anchor is unset.
func (a Anchor) Zero() bool { return a.Turn == 0 && a.Node == 0 }

// More reports whether nodes exist outside this page in each direction. It is
// the only extent signal a pager needs: a total node count would be a lie for
// an unsealed turn and useless for sizing a scrollbar, which measures rows.
type More struct {
	Before bool `json:"before,omitempty"`
	After  bool `json:"after,omitempty"`
}

// TurnPart is one turn as it appears on a page: the turn itself plus where
// this window sits inside it. A part carries a contiguous run of nodes, so
// ids are positional: Nodes[i] has id From+i, and are omitted from the wire.
type TurnPart struct {
	Turn
	From        uint64 `json:"from"`
	ClippedHead bool   `json:"clipped_head,omitempty"`
	ClippedTail bool   `json:"clipped_tail,omitempty"`
}

// Page is the one wire shape: pulled by read, pushed by the live stream. A
// pure delta push is a Page whose single part carries Live and no Nodes.
type Page struct {
	Parts   []TurnPart `json:"parts"`
	More    More       `json:"more"`
	Metrics *Metrics   `json:"metrics,omitempty"`
}

// MarshalJSON keeps `parts` present and an ARRAY, always. An empty branch
// used to serialize as {"more":{}}: no key at all, so a consumer that
// indexed parts got a nil and a client that iterated it crashed. An empty
// page is a page with nothing in it, not a page without the field.
func (p Page) MarshalJSON() ([]byte, error) {
	type wire Page
	if p.Parts == nil {
		p.Parts = []TurnPart{}
	}
	return json.Marshal(wire(p))
}

// Empty reports whether the page carries nothing, so it isn't sent.
func (p Page) Empty() bool { return len(p.Parts) == 0 && p.Metrics == nil }

// Less orders anchors lexicographically on (Turn, Node). This is THE ordering;
// nothing else in the tree may open-code the comparison.
func (a Anchor) Less(b Anchor) bool {
	if a.Turn != b.Turn {
		return a.Turn < b.Turn
	}
	return a.Node < b.Node
}

// Next is the immediately following anchor. Within a turn it is the next node.
// It does NOT otherwise cross a turn boundary, because an anchor does not
// encode its turn's length: whether (t, n) is the last node of turn t is
// knowledge the STORE holds (see Store.SetTurnLen and Store.adjacent), not
// something the coordinate can answer alone. The one exception is the node
// ceiling, where the lexicographic successor is unambiguous and wrapping to
// (t, 0) would silently invert the ordering.
func (a Anchor) Next() Anchor {
	if a.Node == maxNode {
		return Anchor{Turn: a.Turn + 1}
	}
	return Anchor{Turn: a.Turn, Node: a.Node + 1}
}

// Prev is the immediately preceding anchor: the mirror of Next. In (turn,
// node) space ordered lexicographically the predecessor of (t, 0) is the last
// node of turn t-1, which with uint64 ordinals is (t-1, maxNode). The zero
// anchor is its own predecessor: it is the floor.
func (a Anchor) Prev() Anchor {
	if a.Node > 0 {
		return Anchor{Turn: a.Turn, Node: a.Node - 1}
	}
	if a.Turn == 0 {
		return a
	}
	return Anchor{Turn: a.Turn - 1, Node: maxNode}
}

// LiveTail returns the page's open suffix, or nil if every node on it is
// closed. At most one part can carry Live and it is always the last, so this
// is the whole answer to "is any of this still moving".
func (p Page) LiveTail() *Live {
	if n := len(p.Parts); n > 0 {
		return p.Parts[n-1].Live
	}
	return nil
}
