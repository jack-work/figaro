package aria

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jack-work/figaro/api/livedoc"
)

// Client folds Pages into a local range-backed view. Live frames are folded
// into materialized livedoc.Node instances by positional ordinal; a suffix
// close marker is accepted only when the seen record version matches. Any
// mismatch fires OnDesync with the highest fully sealed turn so the caller can
// re-read.
type Client struct {
	mu sync.Mutex

	store       *Store
	closedLimit int
	closedRev   uint64
	// Highest fully sealed turn; the field name predates turn addressing.
	lastCommittedLT int

	// The open turn, materialized, lives in the store (Store.openTail): it
	// holds the TURN ID (see Message), the suffix boundary reported by
	// Live.From: nodes below it are closed and will never be touched again -
	// the record version, and the node buffer.

	// emitted[turn] is how many of a turn's nodes have already gone out as
	// closed messages.
	emitted map[int]int
	// inquiry[turn] is that turn's opening question, held for as long as any
	// part of the turn is retained.
	inquiry map[int]Inquiry
	fetch   Fetcher

	OnClosed  func(Message)
	OnLive    func(Message)
	OnDesync  func(sinceLT int)
	OnMetrics func(Metrics)
}

// Inquiry is a turn's opening question: the text, its per-sender split, and the
// turn-level form state that arrived with it.
type Inquiry struct {
	Text       string
	Segments   []InquirySegment
	FormDeltas map[string]livedoc.FormDelta
	LT         uint64 // see Message.InquiryLT
}

// NewClient returns a fresh client.
func NewClient() *Client {
	return &Client{store: NewStore(), emitted: map[int]int{}, inquiry: map[int]Inquiry{}}
}

// Store exposes the range store beneath the client. It is not safe to use
// concurrently with Apply: the client's mutex guards the store, and this hands
// out the guarded object.
func (c *Client) Store() *Store {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store
}

// Query reports what the store HOLDS over [from, to]; it never fetches. The
// returned Segment.Msgs ALIAS the store and are valid only until the next
// Apply/Merge/Evict: the caller is a renderer, which runs under the same lock
// discipline as the fold and never keeps a segment across one.
func (c *Client) Query(from, to Anchor) []Segment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Query(from, to)
}

// ForEachIn walks the retained messages inside [from, to] under the client's
// lock: the renderer's read path, which must not allocate a segment list per
// frame (see Store.ForEachIn).
func (c *Client) ForEachIn(from, to Anchor, fn func(Message) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.ForEachIn(from, to, fn)
}

// SetFetcher installs the reader Ensure fills holes with.
func (c *Client) SetFetcher(f Fetcher) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetch = f
}

// Ensure fills every hole in [from, to] so that Query over the same interval
// then returns exactly one Segment with a nil Gap.
func (c *Client) Ensure(ctx context.Context, from, to Anchor) error {
	for range ensureRounds {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		gap, ok := c.store.firstGap(from, to)
		fetch, before := c.fetch, c.store.Count()
		c.mu.Unlock()
		if !ok {
			return nil
		}
		if fetch == nil {
			return ErrNoFetcher
		}
		got, err := fetch(ctx, fillAt(gap), fillLimit)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.fold(got)
		m := c.store.More()
		m.Before = got.More.Before
		c.store.SetMore(m)
		grew := c.store.Count() != before
		c.mu.Unlock()
		if !grew {
			return ErrStalled
		}
	}
	return ErrStalled
}

// ForEachSegment walks [from, to] as runs and holes, under the client's lock
// (see Store.ForEachSegment). This is the renderer's GAP-AWARE read path: the
// pager draws one sentinel row per hole, so it is the one consumer that must
// see them.
func (c *Client) ForEachSegment(from, to Anchor, msg func(Message) bool, gap func(Gap) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.ForEachSegment(from, to, msg, gap)
}

// Count is how many closed messages the store retains.
func (c *Client) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Count()
}

// TailFrom is the anchor of the n-th message from the end (see Store.TailFrom).
func (c *Client) TailFrom(n int) (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.TailFrom(n)
}

// Skip is the anchor of the n-th message at or after a (see Store.Skip).
func (c *Client) Skip(a Anchor, n int) (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Skip(a, n)
}

// Before is the anchor n messages before a, and how far it got (see
// Store.Before), a windowed reader lowering its floor over history the store
// already holds.
func (c *Client) Before(a Anchor, n int) (Anchor, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Before(a, n)
}

// EvictBefore forgets everything below a.
func (c *Client) EvictBefore(a Anchor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a == (Anchor{}) || c.store.Count() == 0 {
		return
	}
	before := c.store.Count()
	c.store.Evict(Anchor{}, a.Prev())
	if c.store.Count() != before {
		c.closedRev++
	}
	for id := range c.inquiry {
		if id < int(a.Turn) {
			delete(c.inquiry, id)
		}
	}
}

// InquiryOf reports a turn's opening question, which is held while any part of
// the turn is retained.
func (c *Client) InquiryOf(turn int) (Inquiry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, ok := c.inquiry[turn]
	return q, ok
}

// SetMoreBefore records whether anything precedes the oldest retained message.
// Only the wire knows: a backward read reports it (Page.More.Before), and an
// empty backward read proves the floor. A PUSHED frame does not: its More
// describes the delta window, not the conversation: which is why this is set
// by whoever performed the read rather than folded in Apply.
func (c *Client) SetMoreBefore(more bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.store.More()
	m.Before = more
	c.store.SetMore(m)
}

// MoreBefore reports whether older history is believed to exist.
func (c *Client) MoreBefore() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.More().Before
}

// SetClosedLimit bounds retained closed messages. Zero keeps the default,
// unbounded behavior.
func (c *Client) SetClosedLimit(limit int) {
	c.mu.Lock()
	c.closedLimit = limit
	c.trimClosed()
	c.mu.Unlock()
}

// Cursor is the highest fully sealed turn: the resume point for a re-read.
func (c *Client) Cursor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCommittedLT
}

// OpenAnimating reports whether the open message has a running tool: i.e. a
// spinner that needs the periodic tick repaint. When false, a renderer can skip
// its timer-driven redraw entirely (content updates still arrive via Apply).
func (c *Client) OpenAnimating() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.OpenAnimating()
}

// Fold says whether a folded page notifies the client's observers. History a
// caller fetched itself folds Quiet: nothing about it is news.
type Fold bool

const (
	Notify Fold = true
	Quiet  Fold = false
)

// Apply folds a Page into the local view and reports the messages it closed,
// oldest first.
func (c *Client) Apply(p Page, f Fold) []Message {
	c.mu.Lock()
	finalized, desync := c.fold(p)
	haveLive := c.store.OpenTurn() != 0
	live := c.openMessage()
	c.mu.Unlock()

	if f == Quiet {
		return finalized
	}
	if p.Metrics != nil && c.OnMetrics != nil {
		c.OnMetrics(*p.Metrics)
	}
	for _, m := range finalized {
		if c.OnClosed != nil {
			c.OnClosed(m)
		}
	}
	if haveLive && c.OnLive != nil {
		c.OnLive(live)
	}
	if desync >= 0 && c.OnDesync != nil {
		c.OnDesync(desync)
	}
	return finalized
}

// fold folds a page into the store and reports the messages it closed and the
// turn to re-read from on a version mismatch (-1 for none). Caller holds c.mu.
func (c *Client) fold(p Page) (finalized []Message, desync int) {
	desync = -1

	for _, part := range p.Parts {
		id := int(part.ID)
		// One turn at a time may hold the open slots; see claimsOpen.
		staged := c.claimsOpen(part)

		// The question commits before the agent has said anything, so it
		// arrives on a part of its own and is held against the turn.
		if part.Inquiry != "" || len(part.FormDeltas) > 0 {
			q := Inquiry{Text: part.Inquiry, Segments: part.InquirySegments, FormDeltas: part.FormDeltas}
			// The LT arrives with the seal, on a later part than the text; a
			// part without one must not forget one already held.
			if len(part.LTs) > 0 {
				q.LT = part.LTs[0]
			} else {
				q.LT = c.inquiry[id].LT
			}
			c.inquiry[id] = q
			// A part clipped at the head describes a turn we hold only the tail
			// of, and may not claim the open slots.
			if staged && !part.ClippedHead {
				c.store.ClaimOpen(id)
			}
		}

		// From is the positional id of Nodes[0], so a clipped part slots into
		// place rather than replacing.
		if len(part.Nodes) > 0 && staged {
			c.store.ClaimOpen(id)
			// Nodes below From were never delivered and are not ours to release.
			if n := int(part.From); n > c.emitted[id] && n > c.store.OpenLen() {
				c.emitted[id] = n
			}
			c.store.Absorb(part.From, part.Nodes)
		}

		if part.Live != nil && staged {
			c.store.ClaimOpen(id)
			c.store.SetLiveFrom(part.Live.From)
			for _, nd := range part.Live.Nodes {
				c.store.FoldAt(nd)
			}
			// Everything below Live.From is closed for good; release it now so
			// the head of a long turn need not wait for the seal.
			if n := int(c.store.LiveFrom()); n > c.emitted[id] && n <= c.store.OpenLen() {
				finalized = c.appendUnits(finalized, id, c.emitted[id], c.store.OpenSlice(c.emitted[id], n))
				c.emitted[id] = n
			}
			if len(part.Live.Nodes) > 0 {
				c.store.SetOpenV(part.Live.V)
			} else if c.store.OpenV() != part.Live.V && len(part.Nodes) == 0 {
				// A close marker for a version we never reached: frames were
				// missed, so ask for a catch-up rather than show a gap.
				desync = c.lastCommittedLT
			}
		}

		if !part.Sealed {
			continue
		}
		if staged {
			// Release what was streamed, not what the part restates: a seal
			// often arrives as a bare marker, and the buffer is the fuller
			// record.
			nodes := part.Nodes
			if c.store.OpenLen() >= len(nodes) {
				nodes = c.store.OpenNodes()
			}
			if s := c.emitted[id]; s < len(nodes) {
				finalized = c.appendUnits(finalized, id, s, nodes[s:])
				c.store.SetTurnLen(uint64(id), uint64(len(nodes)))
			} else if s == 0 && c.inquiry[id].Text != "" {
				finalized = append(finalized, c.message(id, 0, nil))
				c.store.SetTurnLen(uint64(id), 1)
			} else if len(nodes) > 0 {
				c.store.SetTurnLen(uint64(id), uint64(len(nodes)))
			}
		} else if len(part.Nodes) > 0 {
			finalized = c.appendUnits(finalized, id, int(part.From),
				append([]livedoc.Node(nil), part.Nodes...))
			// A part not clipped at the tail states the turn's extent, which is
			// what lets the store call (t, last) and (t+1, 0) neighbours.
			if !part.ClippedTail {
				c.store.SetTurnLen(uint64(id), part.From+uint64(len(part.Nodes)))
			}
		} else if c.inquiry[id].Text != "" {
			finalized = append(finalized, c.message(id, 0, nil))
			if !part.ClippedTail {
				c.store.SetTurnLen(uint64(id), 1)
			}
		}
		delete(c.emitted, id)
		c.advanceCommitted(id)
		if c.store.OpenTurn() == id {
			c.store.ResetOpen()
		}
	}

	if len(finalized) > 0 {
		// The store is the one authority on what is already held: what it takes
		// is what is news.
		finalized = c.store.Insert(finalized...)
		c.closedRev++
	}
	c.trimClosed()
	return finalized, desync
}

// unitChars bounds one materialized message's payload, so a renderer caching
// rows per message caches them at a useful granularity. A node is never split.
const unitChars = 40000

// appendUnits appends nodes as one message, or as several when they exceed
// unitChars. Caller holds c.mu.
func (c *Client) appendUnits(dst []Message, turn, from int, nodes []livedoc.Node) []Message {
	size := func(n livedoc.Node) int { return len(n.Markdown) + len(n.Output) + len(n.Summary) }
	total := 0
	for _, n := range nodes {
		total += size(n)
	}
	if total < unitChars || len(nodes) < 2 {
		return append(dst, c.message(turn, from, nodes))
	}
	start, budget := 0, 0
	for i, n := range nodes {
		budget += size(n)
		if budget < unitChars && i < len(nodes)-1 {
			continue
		}
		dst = append(dst, c.message(turn, from+start, nodes[start:i+1]))
		start, budget = i+1, 0
	}
	return dst
}

// claimsOpen reports whether a part is entitled to the open-turn slots.
func (c *Client) claimsOpen(part TurnPart) bool {
	id := int(part.ID)
	switch {
	case c.store.OpenTurn() == id:
		return true
	case c.store.OpenTurn() != 0 && id < c.store.OpenTurn():
		return false
	case part.Live != nil:
		return true
	case part.Sealed:
		return false
	case c.store.Complete(id):
		return false
	}
	return true
}

// turnRole is the voice a message renders under. Every node is agent output:
// the inquiry is text on the turn, and a steer is an inline annotation inside
// the agent's run, not a voice of its own. So a message with nodes speaks in
// the agent's voice, and one without, an inquiry whose turn produced nothing -
// in the user's.
func turnRole(nodes []livedoc.Node) string {
	if len(nodes) == 0 {
		return livedoc.RoleInput
	}
	return livedoc.RoleOutput
}

// message builds one slice of a turn. THE SLICE THAT STARTS THE TURN CARRIES
// THE QUESTION, and no other does: a turn is an inquiry followed by the answer
// to it, and a question drawn above node 64 of 130 says an exchange began where
// the reader is merely standing.
func (c *Client) message(turn, from int, nodes []livedoc.Node) Message {
	m := Message{Turn: turn, From: uint64(from), Role: turnRole(nodes), Nodes: nodes}
	if from == 0 {
		q := c.inquiry[turn]
		m.Inquiry, m.InquirySegments = q.Text, q.Segments
		m.InquiryLT = q.LT
		m.FormDeltas = q.FormDeltas
	}
	return m
}

// openMessage is the open turn's suffix as a message. Caller holds the lock.
func (c *Client) openMessage() Message {
	turn := c.store.OpenTurn()
	e := c.emitted[turn]
	return c.message(turn, c.store.OpenBase(e), c.store.OpenSuffix(e))
}

// trimClosed enforces the retention limit, and with it the lifetime of a held
// question: a turn no longer retained keeps none.
func (c *Client) trimClosed() {
	if c.closedLimit <= 0 || c.store.Count() <= c.closedLimit {
		return
	}
	c.closedRev++
	c.store.TrimOldestTo(c.closedLimit)
	first := c.store.First()
	if first == nil {
		return
	}
	for id := range c.inquiry {
		if id < first.Turn {
			delete(c.inquiry, id)
		}
	}
}

// View is the client's local reconstruction.
type View struct {
	Closed []Message
	Open   *Message
}

// ClosedRevision is a counter bumped whenever the retained closed set changes
// (a message finalized, or the retention limit trimmed one away). A viewer that
// derives state from the closed tail: the transcript's page window does: can
// hold it and skip the rebuild while it is unchanged, instead of re-deriving
// per frame. Never zero after the first change, so zero is usable as "unset".
func (c *Client) ClosedRevision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closedRev + 1
}

// Open returns just the open, in-flight message (nil when none). View copies
// and sorts the whole retained closed set; callers that only want the live
// message: every transcript frame asks for it: should not pay for that.
func (c *Client) Open() *Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store.OpenTurn() == 0 {
		return nil
	}
	m := c.openMessage()
	return &m
}

// View returns a snapshot of the current local state.
func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Ordered by the FULL identity (Turn, From), which the store maintains: a
	// turn arrives as several slices, and arrival order interleaves when a
	// live-sealed message precedes a catch-up Read of older history.
	v := View{Closed: c.store.All()}
	if c.store.OpenTurn() != 0 {
		m := c.openMessage()
		v.Open = &m
	}
	return v
}

func (c *Client) advanceCommitted(lt int) {
	if lt > c.lastCommittedLT {
		c.lastCommittedLT = lt
	}
}

// foldDelta applies a NodeDelta to a node: set merges fields, unset clears them,
// patch splices a streamed string field on its previous value.
func foldDelta(n livedoc.Node, d NodeDelta) livedoc.Node {
	for k, v := range d.Set {
		setField(&n, k, v)
	}
	for _, f := range d.Unset {
		setField(&n, f, nil)
	}
	for f, dl := range d.Patch {
		switch f {
		case "markdown":
			n.Markdown = livedoc.Apply(n.Markdown, dl)
		case "output":
			n.Output = livedoc.Apply(n.Output, dl)
		case "input":
			n.Input = livedoc.Apply(n.Input, dl)
		}
	}
	return n
}

func setField(n *livedoc.Node, field string, v any) {
	switch field {
	case "type":
		n.Type = livedoc.NodeType(asStr(v))
	case "name":
		n.Name = asStr(v)
	case "summary":
		n.Summary = asStr(v)
	case "sender":
		// Dropping this silently would make every STREAMED input block look
		// unattributed while a re-read showed its sender: the same exchange
		// telling two stories, which is exactly how the role field broke once.
		n.Sender = asStr(v)
	case "status":
		n.Status = asStr(v)
	case "markdown":
		n.Markdown = asStr(v)
	case "role":
		// The server sends this (fullSet/diff both emit "role"), and dropping it
		// silently made every STREAMED node look like agent output: which is
		// how a steer arrived unmarked when watched and marked when re-read.
		n.Role = asStr(v)
	case "tool_call_id":
		n.ToolCallID = asStr(v)
	case "at":
		n.At = asInt64(v)
	case "lts":
		n.LTs = asUint64s(v)
	case "src":
		n.Src = asSrcs(v)
	case "output":
		n.Output = asStr(v)
	case "input":
		n.Input = asStr(v)
	case "id":
		n.ID = asStr(v)
	case "opened_at":
		n.OpenedAt = asInt64(v)
	case "started_at":
		n.StartedAt = asInt64(v)
	case "finished_at":
		n.FinishedAt = asInt64(v)
	case "args":
		if v == nil {
			n.Args = nil
		} else if m, ok := v.(map[string]any); ok {
			n.Args = m
		}
	case "formDeltas":
		n.FormDeltas = asFormDeltas(v)
	}
}

func asStr(v any) string {
	s, _ := v.(string)
	return s
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

// asUint64s and asSrcs accept both the in-process value and its JSON echo
// ([]any of float64 / map[string]any), because a delta reaches the fold either
// way: constructed locally in a test, or decoded off the wire.
func asUint64s(v any) []uint64 {
	switch t := v.(type) {
	case []uint64:
		return t
	case []any:
		out := make([]uint64, 0, len(t))
		for _, e := range t {
			out = append(out, uint64(asInt64(e)))
		}
		return out
	}
	return nil
}

func asSrcs(v any) []livedoc.Src {
	switch t := v.(type) {
	case []livedoc.Src:
		return t
	case []any:
		out := make([]livedoc.Src, 0, len(t))
		for _, e := range t {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, livedoc.Src{LT: uint64(asInt64(m["lt"])), Block: int(asInt64(m["block"]))})
		}
		return out
	}
	return nil
}

// asFormDeltas accepts both the in-process value and its JSON echo, like
// asSrcs above and for the same reason: a delta reaches the fold either
// constructed locally or decoded off the wire.
func asFormDeltas(v any) map[string]livedoc.FormDelta {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]livedoc.FormDelta:
		return t
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out map[string]livedoc.FormDelta
		if json.Unmarshal(raw, &out) != nil || len(out) == 0 {
			return nil
		}
		return out
	}
}
