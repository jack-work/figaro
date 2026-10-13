package aria

import (
	"encoding/json"
	"sort"

	"github.com/jack-work/figaro/api/livedoc"
)

// nodeSize is a node's cost against the page budget: its serialized length.
func nodeSize(n livedoc.Node) int {
	b, err := json.Marshal(n)
	if err != nil {
		return 1
	}
	return len(b)
}

type cursor struct{ turn, node int }

// units is how many positions a turn occupies; an inquiry with no nodes has one.
func units(t Turn) int {
	if n := len(t.Nodes); n > 0 {
		return n
	}
	if t.Inquiry != "" || len(t.InquirySegments) > 0 {
		return 1
	}
	return 0
}

func unitSize(t Turn, i int) int {
	if i < len(t.Nodes) {
		return nodeSize(t.Nodes[i])
	}
	return len(t.Inquiry) + 1
}

// locate resolves an anchor to a cursor, and reports whether anything lies in
// the direction of travel. A backward read from beyond the last turn is the
// tail; a forward read from there has nothing to give.
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
	// ti is the largest index whose id is <= a.Turn.
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

// step advances a cursor one node in dir; ok is false at the ends.
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

func below(c, f cursor) bool {
	return c.turn < f.turn || (c.turn == f.turn && c.node < f.node)
}

// floorAt resolves a floor anchor to the first position at or above it; ok is
// false when nothing lies there.
func floorAt(turns []Turn, f Anchor) (cursor, bool) {
	for ti := sort.Search(len(turns), func(i int) bool { return turns[i].ID >= f.Turn }); ti < len(turns); ti++ {
		n := uint64(units(turns[ti]))
		if n == 0 {
			continue
		}
		if turns[ti].ID != f.Turn {
			return cursor{ti, 0}, true
		}
		if f.Node < n {
			return cursor{ti, int(f.Node)}, true
		}
	}
	return cursor{}, false
}

// Paginate cuts one Page out of turns, walking from at in dir until budget
// bytes are spent. Pure.
func Paginate(turns []Turn, at Anchor, dir Direction, budget int) Page {
	return paginate(turns, at, dir, budget, Anchor{})
}

// paginate is the one walk. floor applies backward, inclusive; zero is no floor.
func paginate(turns []Turn, at Anchor, dir Direction, budget int, floor Anchor) Page {
	if len(turns) == 0 || budget <= 0 {
		return Page{}
	}
	stop, floored := cursor{}, dir == Backward && !floor.Zero()
	if floored {
		var ok bool
		if stop, ok = floorAt(turns, floor); !ok {
			return Page{}
		}
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
	if floored && below(start, stop) {
		return Page{}
	}

	// Always take the first node so a page can never be empty.
	spent, end := 0, start
	for c, ok := start, true; ok; c, ok = step(turns, c, dir) {
		if floored && below(c, stop) {
			break
		}
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
	_, hasPrev := step(turns, lo, Backward)
	next, hasNext := step(turns, hi, Forward)
	p.More.Before, p.More.After = hasPrev, hasNext
	if hasPrev {
		// A backward read excludes its anchor, so Prev is the page's own first.
		a := anchorAt(turns, lo)
		p.Prev = &a
	}
	if hasNext {
		a := anchorAt(turns, next)
		p.Next = &a
	}
	return p
}

func anchorAt(turns []Turn, c cursor) Anchor {
	return Anchor{Turn: turns[c.turn].ID, Node: uint64(c.node)}
}

// PaginateBefore pages backward from an anchor, excluding the anchor node when
// it exists. floor is inclusive of its own anchor; the zero anchor is no floor.
// A floor cut sets More.Before exactly as a budget cut would.
func PaginateBefore(turns []Turn, at, floor Anchor, budget int) Page {
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
			return Page{}
		}
		at = Anchor{Turn: turns[prev.turn].ID, Node: uint64(prev.node)}
	}
	return paginate(turns, at, Backward, budget, floor)
}

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
		// Below Live.From the nodes are closed and can never receive a delta.
		if t.Live != nil && uint64(last) < t.Live.From {
			part.Live = nil
		}
		parts = append(parts, part)
	}
	return parts
}
