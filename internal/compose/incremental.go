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
	c.composed = 0
	c.reused = 0
}

// Stats reports messages composed and messages served from the memo since the
// last Reset. It is the meter for the work this type exists to skip: a claim
// that something is being reused needs a number that can read zero.
func (c *Incremental) Stats() (composed, reused int) { return c.composed, c.reused }

// Nodes composes the region, reusing the memoized prefix where it still
// applies and composing the rest.
func (c *Incremental) Nodes(msgs []message.Message, partials, argPartials map[string]string, timings map[string]ToolTiming) []livedoc.Node {
	stable := stableBoundary(msgs)
	if !c.valid(msgs, stable) {
		c.nodes = nil
		c.keys = nil
	}
	if n := len(c.keys); stable > n {
		c.nodes = append(c.nodes, Nodes(msgs[n:stable], partials, argPartials, timings)...)
		for _, m := range msgs[n:stable] {
			c.keys = append(c.keys, keyOf(m))
		}
		c.composed += stable - n
	}
	c.reused += len(c.keys)

	tail := msgs[len(c.keys):]
	c.composed += len(tail)
	tailNodes := Nodes(tail, partials, argPartials, timings)

	out := make([]livedoc.Node, 0, len(c.nodes)+len(tailNodes))
	out = append(out, c.nodes...)
	out = append(out, tailNodes...)
	if len(out) == 0 {
		// Wholesale composition returns a nil slice for a region with nothing
		// renderable in it. Returning an empty non-nil one instead is a
		// difference every caller happens to survive today, which is not the
		// same as no difference: the claim here is identity.
		return nil
	}
	return out
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
func stableBoundary(msgs []message.Message) int {
	if len(msgs) < 2 {
		return 0
	}
	stable, open := 0, 0
	for i, m := range msgs[:len(msgs)-1] {
		for _, c := range m.Content {
			switch c.Type {
			case message.ContentToolInvoke:
				open++
			case message.ContentToolResult:
				if open > 0 {
					open--
				}
			}
		}
		if open == 0 {
			stable = i + 1
		}
	}
	return stable
}
