package compose

import (
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// Incremental composes the open region by reusing the nodes it already
// composed for the part of that region which can no longer change.
type Incremental struct {
	nodes []livedoc.Node // composed nodes for the memoized prefix
	keys  []memoKey      // one per memoized message, in order

	published int // nodes handed over last call that are still identical

	composed int
	reused   int
}

// memoKey identifies a memoized message closely enough to detect that the
// region it belongs to is not the one that was composed.
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
