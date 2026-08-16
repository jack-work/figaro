package compose

import (
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// Incremental composes the open region by reusing the nodes it already
// composed for the part of that region which can no longer change.
//
// Nodes(msgs, ...) returns exactly what the wholesale Nodes(msgs, ...)
// returns, on every call. incremental_test.go drives a whole turn frame by
// frame and asserts that node for node; a composer that agreed only at seal
// would render correct transcripts over wrong live frames.
//
// Why the reuse is sound, and what the guard is for, is in
// ~/notes/figaro/s6-incremental-prediction.md.
type Incremental struct {
	nodes []livedoc.Node // composed nodes for the memoized prefix
	keys  []memoKey      // one per memoized message, in order

	published int // nodes handed over last call that are still identical

	composed int
	reused   int
}

// memoKey identifies a memoized message closely enough to detect that the
// region it belongs to is not the one that was composed.
//
// The texts are compared with Go string equality, which returns on pointer
// identity before it looks at any byte. The log hands back the same string
// headers every frame, so the check costs a pointer compare per block; it
// degrades to a byte compare, still correct, if that sharing ever stops.
type memoKey struct {
	lt     uint64
	role   message.Role
	blocks []blockKey
}

type blockKey struct {
	kind    message.ContentType
	toolID  string
	text    string
	sender  string
	args    int
	isError bool
}

func NewIncremental() *Incremental { return &Incremental{} }

// Reset drops the memo. The agent calls it between turns, where the region
// itself is replaced.
func (c *Incremental) Reset() {
	c.nodes = nil
	c.keys = nil
	c.published = 0
	c.composed = 0
	c.reused = 0
}

// Stats reports messages composed and messages served from the memo since the
// last Reset. It is the meter for the work this type exists to skip: a claim
// that something is being reused needs a number that can read zero.
func (c *Incremental) Stats() (composed, reused int) { return c.composed, c.reused }

// Nodes composes the region and returns it in two pieces: a prefix of nodes
// that can no longer change, and the suffix that can. stable is the count of
// leading prefix nodes identical to those returned by the previous call.
//
// A node enters the prefix ONLY when it can no longer change. Neither returned
// slice is mutated after it is returned -- the prefix grows by append, which a
// holder of the shorter slice cannot observe -- so a consumer may retain both
// rather than copy them.
func (c *Incremental) Nodes(msgs []message.Message, partials, argPartials map[string]string, timings map[string]ToolTiming) (prefix, suffix []livedoc.Node, stable int) {
	bound := stableBoundary(msgs)
	published := c.published
	if !c.valid(msgs, bound) {
		c.nodes = nil
		c.keys = nil
		published = 0
	}
	if n := len(c.keys); bound > n {
		c.nodes = append(c.nodes, Nodes(msgs[n:bound], partials, argPartials, timings)...)
		for _, m := range msgs[n:bound] {
			c.keys = append(c.keys, keyOf(m))
		}
		c.composed += bound - n
	}
	c.reused += len(c.keys)
	if published > len(c.nodes) {
		published = len(c.nodes)
	}
	c.published = len(c.nodes)

	tail := msgs[len(c.keys):]
	c.composed += len(tail)
	return c.nodes, Nodes(tail, partials, argPartials, timings), published
}

// valid reports whether the memo describes the same messages this region
// starts with. A memo that cannot be trusted is dropped, not repaired: a miss
// costs one recomposition, a lie is permanent, because a composed node is
// cached per LT and goes out on the wire.
func (c *Incremental) valid(msgs []message.Message, stable int) bool {
	if len(c.keys) > stable || len(c.keys) > len(msgs) {
		return false
	}
	for i, k := range c.keys {
		if !k.matches(msgs[i]) {
			return false
		}
	}
	return true
}

// keyOf builds a memo key from a read-back entry: LT is final only once the
// log has handed the message back. Never key on an LT supplied by an appender.
func keyOf(m message.Message) memoKey {
	k := memoKey{lt: m.LogicalTime, role: m.Role}
	if len(m.Content) > 0 {
		k.blocks = make([]blockKey, len(m.Content))
		for i, c := range m.Content {
			k.blocks[i] = blockKey{
				kind: c.Type, toolID: c.ToolCallID, text: c.Text,
				sender: c.Sender, args: len(c.Arguments), isError: c.IsError,
			}
		}
	}
	return k
}

func (k memoKey) matches(m message.Message) bool {
	if k.lt != m.LogicalTime || k.role != m.Role || len(k.blocks) != len(m.Content) {
		return false
	}
	for i, b := range k.blocks {
		c := m.Content[i]
		if b.kind != c.Type || b.toolID != c.ToolCallID || b.args != len(c.Arguments) ||
			b.isError != c.IsError || b.sender != c.Sender || b.text != c.Text {
			return false
		}
	}
	return true
}

// stableBoundary returns the count of leading messages whose composition can
// no longer change.
//
// Two things move a node after it was first composed: its own message is still
// being written, and a tool_invoke composed before its result arrives (status
// running -> ok, streamed partial -> the durable clamped text). So a prefix is
// stable when every tool_invoke inside it has its result inside it too, and it
// stops one short of the last message, which may still be growing.
//
// Splitting the region there is what makes the reuse an identity rather than
// an approximation: with every invoke's result inside the prefix, composing
// the prefix and the remainder separately and concatenating them is the same
// walk Nodes would make over the whole region.
//
// Invokes are matched to results BY ID, not counted. Counting says "as many
// results as invokes have gone by", which a duplicate result for one call, or
// a result belonging to a region that started earlier, can satisfy while the
// call that is actually streaming has none -- and that call's node renders
// from `partials`, which the memo key cannot see. Matching costs a linear scan
// over the handful of calls a region has open at once, from a stack-backed
// slice, and allocates nothing: a map per question is the pattern this
// campaign spent four days deleting one layer down.
func stableBoundary(msgs []message.Message) int {
	if len(msgs) < 2 {
		return 0
	}
	var buf [8]string // the open calls; a region holds a handful, never many
	open := buf[:0]
	stable := 0
	for i, m := range msgs[:len(msgs)-1] {
		for _, c := range m.Content {
			switch c.Type {
			case message.ContentToolInvoke:
				open = append(open, c.ToolCallID)
			case message.ContentToolResult:
				// A result for a call this region never opened closes nothing.
				for j := range open {
					if open[j] == c.ToolCallID {
						open = append(open[:j], open[j+1:]...)
						break
					}
				}
			}
		}
		if len(open) == 0 {
			stable = i + 1
		}
	}
	return stable
}
