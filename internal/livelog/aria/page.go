package aria

import (
	"encoding/json"
	"sort"

	"github.com/jack-work/figaro/api/livedoc"
)

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

// units is how many positions a turn occupies. A turn with NO NODES but an
// INQUIRY has one: the question is content -- a prompt that was never
// answered, or one whose answer has not arrived -- and a walk that counts
// only nodes cannot reach it, cannot anchor on it, and returns an empty
// page for an aria whose whole history is one unanswered question.
func units(t Turn) int {
	if n := len(t.Nodes); n > 0 {
		return n
	}
	if t.Inquiry != "" || len(t.InquirySegments) > 0 {
		return 1
	}
	return 0
}

// unitSize is a position's cost against the page budget.
func unitSize(t Turn, i int) int {
	if i < len(t.Nodes) {
		return nodeSize(t.Nodes[i])
	}
	return len(t.Inquiry) + 1
}

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
	tail := cursor{last, units(turns[last]) - 1}
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
	if n := units(turns[ti]); ni >= n {
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
			if n := units(turns[t]); n > 0 {
				return cursor{t, n - 1}, true
			}
		}
		return c, false
	}
	if c.node+1 < units(turns[c.turn]) {
		return cursor{c.turn, c.node + 1}, true
	}
	for t := c.turn + 1; t < len(turns); t++ {
		if units(turns[t]) > 0 {
			return cursor{t, 0}, true
		}
	}
	return c, false
}

// Paginate cuts one Page out of turns, walking from at in dir until budget
// bytes are spent. It is pure: same inputs, same page, no clock, no state.
func Paginate(turns []Turn, at Anchor, dir Direction, budget int) Page {
	if len(turns) == 0 || budget <= 0 {
		return Page{}
	}
	start, ok := locate(turns, at, dir)
	if !ok {
		return Page{}
	}
	if units(turns[start.turn]) == 0 {
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
		sz := unitSize(turns[c.turn], c.node)
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
		if len(t.Nodes) == 0 {
			// An inquiry with no answer: one position, no nodes.
			if units(t) == 0 {
				continue
			}
			parts = append(parts, TurnPart{Turn: t, From: 0})
			continue
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
