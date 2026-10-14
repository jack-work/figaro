// Package aria holds the wire types of figaro's paginated read API.
package aria

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/livedoc"
)

const maxNode = ^uint64(0)

// Turn is one exchange. The inquiry is text on the turn, never a node, so
// every node is agent output or a steering interjection.
type Turn struct {
	ID uint64 `json:"turn"`

	Inquiry string `json:"inquiry,omitempty"`

	// InquirySegments is Inquiry split by sender.
	InquirySegments []InquirySegment `json:"inquiry_segments,omitempty"`

	// At is when the inquiry arrived, unix millis.
	At int64 `json:"at,omitempty"`

	// FormDeltas are the transitions whose window closed on the record that
	// opened the turn, keyed "<formid>.<path>".
	FormDeltas map[string]livedoc.FormDelta `json:"formDeltas,omitempty"`

	LTs    []uint64       `json:"lts,omitempty"`
	Sealed bool           `json:"sealed"`
	Nodes  []livedoc.Node `json:"nodes,omitempty"`
	Live   *Live          `json:"live,omitempty"`
}

// InquirySegment.Sender is already rendered (rpc.Attribution); empty means
// unknown and draws nothing.
type InquirySegment struct {
	Sender string `json:"sender,omitempty"`
	Text   string `json:"text"`
}

type Metrics struct {
	ContextTokens    int    `json:"context_tokens"`
	ContextExact     bool   `json:"context_exact"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	Mantra           string `json:"mantra,omitempty"`
}

// Live is a turn's open suffix. Nodes below From are closed and never receive
// a delta. A frame with no Nodes is a close marker: promote the materialized
// copy iff the highest seen version is V.
type Live struct {
	From  uint64      `json:"from"`
	V     int         `json:"v"`
	Nodes []NodeDelta `json:"nodes,omitempty"`
}

// NodeDelta addresses a node by its positional ordinal within the turn;
// livedoc.Node's string ID is provenance, not an address.
type NodeDelta struct {
	ID    uint64                   `json:"id"`
	Set   map[string]any           `json:"set,omitempty"`   // merge fields (create on first set; type required)
	Unset []string                 `json:"unset,omitempty"` // remove fields
	Patch map[string]livedoc.Delta `json:"patch,omitempty"` // splice a streamed string field on its prev value
}

func (d NodeDelta) Empty() bool {
	return len(d.Set) == 0 && len(d.Unset) == 0 && len(d.Patch) == 0
}

// Message is the client-side materialization of one slice of a turn, not a
// wire type.
type Message struct {
	Turn int    // turn id, not unique per message
	From uint64 // node offset within the turn
	// Inquiry, InquirySegments and FormDeltas are carried only by the slice
	// that starts the turn (From == 0).
	Inquiry         string
	InquirySegments []InquirySegment
	// InquiryLT is Turn.LTs[0], known once the turn is sealed; zero until then.
	InquiryLT  uint64
	FormDeltas map[string]livedoc.FormDelta
	Role       string
	Nodes      []livedoc.Node
}

type Direction string

const (
	Forward Direction = "forward"
	// Backward pages are still assembled in reading order; only which nodes
	// fall inside the budget differs.
	Backward Direction = "backward"
)

// Anchor is the UI coordinate: turn id plus positional node ordinal. LT is
// the model's coordinate and never appears here.
type Anchor struct {
	Turn uint64 `json:"turn"`
	Node uint64 `json:"node"`
}

func (a Anchor) Zero() bool { return a.Turn == 0 && a.Node == 0 }

// More is the only extent signal: a total node count would be wrong for an
// unsealed turn.
type More struct {
	Before bool `json:"before,omitempty"`
	After  bool `json:"after,omitempty"`
}

// TurnPart carries a contiguous run of nodes: Nodes[i] has id From+i.
type TurnPart struct {
	Turn
	From        uint64 `json:"from"`
	ClippedHead bool   `json:"clipped_head,omitempty"`
	ClippedTail bool   `json:"clipped_tail,omitempty"`
}

// Page is the one wire shape, pulled by read and pushed by the live stream. A
// pure delta push is a Page whose single part carries Live and no Nodes. Read
// forward at Next for the following page, backward before Prev for the
// preceding one; each is set only when More says there is something that way.
type Page struct {
	// Parts are ascending by (turn, from) and non-overlapping: a wire invariant.
	Parts   []TurnPart `json:"parts"`
	More    More       `json:"more"`
	Next    *Anchor    `json:"next,omitempty"`
	Prev    *Anchor    `json:"prev,omitempty"`
	Metrics *Metrics   `json:"metrics,omitempty"`
}

// Span is the coordinate extrema a page covers, not its first and last
// elements; zero anchors when it carries nothing.
func (p Page) Span() (from, to Anchor) {
	if len(p.Parts) == 0 {
		return Anchor{}, Anchor{}
	}
	first := true
	for _, part := range p.Parts {
		lo := Anchor{Turn: part.ID, Node: part.From}
		hi := lo
		if n := len(part.Nodes); n > 0 {
			hi.Node += uint64(n) - 1
		}
		if first {
			from, to, first = lo, hi, false
			continue
		}
		if lo.Less(from) {
			from = lo
		}
		if to.Less(hi) {
			to = hi
		}
	}
	return from, to
}

// MarshalJSON keeps `parts` present as an array even when empty.
func (p Page) MarshalJSON() ([]byte, error) {
	type wire Page
	if p.Parts == nil {
		p.Parts = []TurnPart{}
	}
	return json.Marshal(wire(p))
}

func (p Page) Empty() bool { return len(p.Parts) == 0 && p.Metrics == nil }

// Less is the one anchor ordering; nothing else may open-code it.
func (a Anchor) Less(b Anchor) bool {
	if a.Turn != b.Turn {
		return a.Turn < b.Turn
	}
	return a.Node < b.Node
}

// Next crosses a turn boundary only at the node ceiling: an anchor does not
// know its turn's length. The ceiling anchor is its own successor; wrapping
// to the zero anchor would read as "the tail".
func (a Anchor) Next() Anchor {
	if a.Node == maxNode {
		if a.Turn == ^uint64(0) {
			return a
		}
		return Anchor{Turn: a.Turn + 1}
	}
	return Anchor{Turn: a.Turn, Node: a.Node + 1}
}

// Prev of (t, 0) is (t-1, maxNode); the zero anchor is its own predecessor.
func (a Anchor) Prev() Anchor {
	if a.Node > 0 {
		return Anchor{Turn: a.Turn, Node: a.Node - 1}
	}
	if a.Turn == 0 {
		return a
	}
	return Anchor{Turn: a.Turn - 1, Node: maxNode}
}

// LiveTail is nil when every node on the page is closed; only the last part
// can carry Live.
func (p Page) LiveTail() *Live {

	if n := len(p.Parts); n > 0 {
		return p.Parts[n-1].Live
	}
	return nil
}
