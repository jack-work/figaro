package aria

import (
	"encoding/json"
	"sort"

	"github.com/jack-work/figaro/internal/livedoc"
)

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
//
// The zero Anchor means "the natural end for this direction": the very first
// node when paging Forward, the very last when paging Backward. So a backward
// read with no anchor is the tail: what `fig show -n N` asks for.
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
//
// Only a page's boundary turns can be clipped; inner turns are whole. That is
// a consequence of the window being contiguous, not a rule the paginator has
// to enforce. The flags are stated explicitly anyway so a client never has to
// infer clipping from a part's position, which would break the day we serve a
// sparse fetch (search results, bookmarks).
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

// nodeSize is a node's cost against the page budget: its serialized length.
// The budget exists to bound wire bytes and client memory, so measuring the
// bytes themselves is the honest metric: node count is not a proxy for it
// (a prose node may be 40 bytes, a tool node 40KB).
func nodeSize(n livedoc.Node) int {
	b, err := json.Marshal(n)
	if err != nil {
		// Unmarshalable nodes cannot reach the wire either; charge a nominal
		// cost so pagination still terminates.
		return 1
	}
	return len(b)
}

// cursor is a position in the flattened (turn, node) space.
type cursor struct{ turn, node int }

// locate resolves an anchor to a cursor, and reports whether anything lies in
// the direction of travel. An anchor outside the materialized range is not an
// error: it is how a caller says "the end": a backward read from beyond the
// last turn is the tail, and a forward read from there has nothing left to
// give. Clamping both directions to the same cursor (as this once did) made a
// forward read from beyond the end return the whole last turn, which a pager
// joining at the tail then rendered a second time.
func locate(turns []Turn, a Anchor, dir Direction) (cursor, bool) {
	if len(turns) == 0 {
		return cursor{}, false
	}
	last := len(turns) - 1
	tail := cursor{last, len(turns[last].Nodes) - 1}
	if tail.node < 0 {
		tail.node = 0
	}
	if a.Zero() {
		if dir == Backward {
			return tail, true
		}
		return cursor{0, 0}, true
	}
	if a.Turn > turns[last].ID {
		if dir == Forward {
			return cursor{}, false
		}
		return tail, true
	}
	if a.Turn < turns[0].ID {
		if dir == Backward {
			return cursor{}, false
		}
		return cursor{0, 0}, true
	}
	// Turn ids ascend, so find the anchor by bisection rather than by walking.
	// ti is the largest index whose id is <= a.Turn (an exact hit when the turn
	// is present, its predecessor otherwise): the same answer the linear scan
	// gave. It matters because Paginate is called once PER PAGE, so an O(#turns)
	// probe made walking a whole aria quadratic in the turn count.
	ti := sort.Search(len(turns), func(i int) bool { return turns[i].ID > a.Turn }) - 1
	if ti < 0 {
		ti = 0
	}
	ni := int(a.Node)
	if n := len(turns[ti].Nodes); ni >= n {
		ni = n - 1
	}
	if ni < 0 {
		ni = 0
	}
	return cursor{ti, ni}, true
}

// step advances a cursor one node in dir. ok is false at the ends.
func step(turns []Turn, c cursor, dir Direction) (cursor, bool) {
	if dir == Backward {
		if c.node > 0 {
			return cursor{c.turn, c.node - 1}, true
		}
		for t := c.turn - 1; t >= 0; t-- {
			if n := len(turns[t].Nodes); n > 0 {
				return cursor{t, n - 1}, true
			}
		}
		return c, false
	}
	if c.node+1 < len(turns[c.turn].Nodes) {
		return cursor{c.turn, c.node + 1}, true
	}
	for t := c.turn + 1; t < len(turns); t++ {
		if len(turns[t].Nodes) > 0 {
			return cursor{t, 0}, true
		}
	}
	return c, false
}

// Paginate cuts one Page out of turns, walking from at in dir until budget
// bytes are spent. It is pure: same inputs, same page, no clock, no state.
//
// Budget is in bytes and granularity is in nodes, a node is never split, and
// at least one node is always emitted even if it alone busts the budget. That
// is safe only because tool output is already clamped upstream (compose's
// 200-source-line tailBound), so a single node is bounded.
//
// Live rides on a part only when the window actually overlaps the open suffix.
// A page that stops short of Live.From is therefore as immutable as a sealed
// page, which is what makes scrolling back through a streaming turn cost the
// same as scrolling back through a finished one.
func Paginate(turns []Turn, at Anchor, dir Direction, budget int) Page {
	if len(turns) == 0 || budget <= 0 {
		return Page{}
	}
	start, ok := locate(turns, at, dir)
	if !ok {
		return Page{}
	}
	if len(turns[start.turn].Nodes) == 0 {
		if next, ok := step(turns, start, dir); ok {
			start = next
		} else {
			return Page{}
		}
	}

	// Walk, collecting positions until the budget is spent. Always take the
	// first node so a page can never be empty.
	spent, end := 0, start
	for c, ok := start, true; ok; c, ok = step(turns, c, dir) {
		sz := nodeSize(turns[c.turn].Nodes[c.node])
		if c != start && spent+sz > budget {
			break
		}
		spent += sz
		end = c
	}

	lo, hi := start, end
	if dir == Backward {
		lo, hi = end, start
	}

	p := Page{Parts: assemble(turns, lo, hi)}
	// More is about the whole conversation, not this window: is there any
	// node before lo, or after hi.
	_, p.More.Before = step(turns, lo, Backward)
	_, p.More.After = step(turns, hi, Forward)
	return p
}

// PaginateBefore pages backward from an anchor, EXCLUDING the anchor node when
// that node actually exists. "Before" means before: the caller already holds
// the anchor: it is the oldest thing in its window and it asked for what
// precedes it. Returning it again duplicates a message at every page boundary.
//
// An anchor past the last turn is a TAIL request (recentCursor). It names no
// existing node, so there is nothing to exclude and it yields the tail
// inclusively. That asymmetry is why this is a named function and not a flag:
// the two callers mean genuinely different things by the same anchor.
func PaginateBefore(turns []Turn, at Anchor, budget int) Page {
	if len(turns) == 0 {
		return Page{}
	}
	if !at.Zero() && at.Turn <= turns[len(turns)-1].ID {
		start, ok := locate(turns, at, Backward)
		if !ok {
			return Page{}
		}
		prev, ok := step(turns, start, Backward)
		if !ok {
			return Page{} // the anchor is the oldest node; nothing precedes it
		}
		at = Anchor{Turn: turns[prev.turn].ID, Node: uint64(prev.node)}
	}
	return Paginate(turns, at, Backward, budget)
}

// assemble builds the parts spanning lo..hi inclusive, in reading order.
func assemble(turns []Turn, lo, hi cursor) []TurnPart {
	parts := make([]TurnPart, 0, hi.turn-lo.turn+1)
	for ti := lo.turn; ti <= hi.turn; ti++ {
		t := turns[ti]
		first, last := 0, len(t.Nodes)-1
		if ti == lo.turn {
			first = lo.node
		}
		if ti == hi.turn {
			last = hi.node
		}
		if first > last {
			continue
		}
		part := TurnPart{
			Turn:        t,
			From:        uint64(first),
			ClippedHead: first > 0,
			ClippedTail: last < len(t.Nodes)-1,
		}
		part.Nodes = t.Nodes[first : last+1]
		// The open suffix is only live for a window that reaches it. Below
		// Live.From the nodes are closed and can never receive a delta.
		if t.Live != nil && uint64(last) < t.Live.From {
			part.Live = nil
		}
		parts = append(parts, part)
	}
	return parts
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
