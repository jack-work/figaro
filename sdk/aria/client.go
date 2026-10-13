package aria

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jack-work/figaro/api/livedoc"
)

// Client folds Pages into a local range-backed view. A suffix close marker is
// accepted only when the seen record version matches; a mismatch fires
// OnDesync with the highest fully sealed turn.
type Client struct {
	mu sync.Mutex

	store       *Store
	closedLimit int
	closedRev   uint64
	rev         uint64
	// Highest fully sealed turn.
	lastCommittedLT int

	// emitted[turn] is how many of a turn's nodes have been released as closed
	// messages. It only rises; what is merely held (Store.OpenHead) falls.
	emitted map[int]int
	// inquiry[turn] is held while any part of the turn is retained.
	inquiry map[int]Inquiry
	fetch   Fetcher

	// offered counts nodes handed to appendUnits. Insert clips duplicates, so
	// this counter is the only witness to a node offered twice.
	offered int

	OnClosed  func(Message)
	OnLive    func(Message)
	OnDesync  func(sinceLT int)
	OnMetrics func(Metrics)
}

// Inquiry is a turn's opening question and the form state that arrived with it.
type Inquiry struct {
	Text       string
	Segments   []InquirySegment
	FormDeltas map[string]livedoc.FormDelta
	LT         uint64 // see Message.InquiryLT
}

func NewClient() *Client {
	return &Client{store: NewStore(), emitted: map[int]int{}, inquiry: map[int]Inquiry{}}
}

// Store hands out the guarded store; not safe to use concurrently with Apply.
func (c *Client) Store() *Store {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store
}

// Query reports what the store holds over [from, to]; it never fetches. The
// returned Segment.Msgs alias the store and are valid only until the next fold.
func (c *Client) Query(from, to Anchor) []Segment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Query(from, to)
}

// ForEachIn walks the retained messages inside [from, to] under the client's lock.
func (c *Client) ForEachIn(from, to Anchor, fn func(Message) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.ForEachIn(from, to, fn)
}

func (c *Client) SetFetcher(f Fetcher) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetch = f
}

// Ensure fills every hole in [from, to].
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

// ForEachSegment walks [from, to] as runs and holes, under the client's lock.
func (c *Client) ForEachSegment(from, to Anchor, msg func(Message) bool, gap func(Gap) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.ForEachSegment(from, to, msg, gap)
}

func (c *Client) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Count()
}

func (c *Client) TailFrom(n int) (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.TailFrom(n)
}

func (c *Client) Skip(a Anchor, n int) (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Skip(a, n)
}

func (c *Client) Before(a Anchor, n int) (Anchor, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Before(a, n)
}

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
		c.rev++
	}
	for id := range c.inquiry {
		if id < int(a.Turn) {
			delete(c.inquiry, id)
		}
	}
}

// CloneBelow is a new client holding the turns below base, for pointing at an
// aria that shares that prefix; this client is left as it was, since a parked
// copy may still own it. Below a sealed fork point both arias' turns are
// byte-identical. base 0 gives an empty client.
func (c *Client) CloneBelow(base int) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := NewClient()
	out.closedLimit = c.closedLimit
	if base <= 0 {
		return out
	}
	out.store = c.store.CloneBelow(uint64(base))
	for id, inq := range c.inquiry {
		if id < base {
			out.inquiry[id] = inq
		}
	}
	for id, n := range c.emitted {
		if id < base {
			out.emitted[id] = n
		}
	}
	out.lastCommittedLT = min(c.lastCommittedLT, base-1)
	return out
}

// Detach silences a client: no callbacks, no fetcher.
func (c *Client) Detach() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.OnClosed, c.OnLive, c.OnDesync, c.OnMetrics = nil, nil, nil, nil
	c.fetch = nil
}

func (c *Client) InquiryOf(turn int) (Inquiry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, ok := c.inquiry[turn]
	return q, ok
}

// SetMoreBefore records whether anything precedes the oldest retained message.
// Only a backward read knows; a pushed frame's More describes the delta window,
// so Apply does not fold it.
func (c *Client) SetMoreBefore(more bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.store.More()
	m.Before = more
	c.store.SetMore(m)
}

func (c *Client) MoreBefore() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.More().Before
}

// FirstMissing is the lowest turn this client does not hold in full. It is a
// fact about what is held, not the divergence point a switch was told: a clone
// drops the open turn. ok is false when the client holds nothing.
func (c *Client) FirstMissing() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	top, ok := c.store.Top()
	if !ok {
		return 0, false
	}
	turn := int(top.Turn)
	if c.store.Complete(turn) {
		return turn + 1, true
	}
	return turn, true
}

// MoreAfter reports whether conversation is believed to exist above what is held.
func (c *Client) MoreAfter() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.More().After
}

// SetClosedLimit bounds retained closed messages; zero is unbounded.
func (c *Client) SetClosedLimit(limit int) {
	c.mu.Lock()
	c.closedLimit = limit
	c.trimClosed()
	c.mu.Unlock()
}

func (c *Client) Offered() int { return c.offered }

// Cursor is the highest fully sealed turn: the resume point for a re-read.
func (c *Client) Cursor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCommittedLT
}

// OpenAnimating reports whether the open message has a running tool, so a
// renderer can skip its timer-driven redraw when false.
func (c *Client) OpenAnimating() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.OpenAnimating()
}

// Fold says whether a folded page notifies the client's observers.
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
	c.rev++
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

// fold reports the messages it closed and the turn to re-read from on a
// version mismatch (-1 for none). Caller holds c.mu.
func (c *Client) fold(p Page) (finalized []Message, desync int) {
	desync = -1

	for _, part := range p.Parts {
		id := int(part.ID)
		staged := c.claimsOpen(part)

		if part.Inquiry != "" || len(part.FormDeltas) > 0 {
			// Text, deltas and LT arrive on different parts; each is sticky.
			q := c.inquiry[id]
			if part.Inquiry != "" {
				q.Text, q.Segments = part.Inquiry, part.InquirySegments
			}
			if len(part.FormDeltas) > 0 {
				q.FormDeltas = part.FormDeltas
			}
			if len(part.LTs) > 0 {
				q.LT = part.LTs[0]
			}
			c.inquiry[id] = q
			// A part clipped at the head may not claim the open slots.
			if staged && !part.ClippedHead {
				c.store.ClaimOpen(id)
			}
		}

		// reclaimed is the old head when this part lowered it by a backward read.
		reclaimed := -1
		if len(part.Nodes) > 0 && staged {
			c.store.ClaimOpen(id)
			head := c.store.OpenHead()
			c.store.Absorb(part.From, part.Nodes)
			if c.store.OpenHead() < head {
				reclaimed = head
			}
		}

		if part.Live != nil && staged {
			c.store.ClaimOpen(id)
			c.store.SetLiveFrom(part.Live.From)
			for _, nd := range part.Live.Nodes {
				c.store.FoldAt(nd)
			}
			// Everything below Live.From is closed for good; release it now.
			if lo, n := c.openStart(id), int(c.store.LiveFrom()); n > lo && n <= c.store.OpenLen() {
				finalized = c.appendUnits(finalized, id, lo, c.store.OpenSlice(lo, n))
				c.emitted[id] = n
			}
			if len(part.Live.Nodes) > 0 {
				c.store.SetOpenV(part.Live.V)
			} else if c.store.OpenV() != part.Live.V && len(part.Nodes) == 0 {
				// A close marker for a version never reached: frames were missed.
				desync = c.lastCommittedLT
			}
		}

		// A backward read can hand back nodes below the release cursor that
		// the server has already closed; release them downward too, or they
		// sit under Live.From held and invisible.
		if reclaimed >= 0 && staged {
			lo := c.store.OpenHead()
			if hi := min(reclaimed, int(c.store.LiveFrom())); hi > lo {
				finalized = c.appendUnits(finalized, id, lo, c.store.OpenSlice(lo, hi))
				// The cursor only ever rises, or the next live frame re-offers
				// everything between.
				if hi > c.emitted[id] {
					c.emitted[id] = hi
				}
			}
		}

		if !part.Sealed {
			continue
		}
		if staged {
			// A seal often arrives as a bare marker; the buffer is the fuller record.
			nodes := part.Nodes
			if c.store.OpenLen() >= len(nodes) {
				nodes = c.store.OpenNodes()
			}
			if s := c.openStart(id); s < len(nodes) {
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
		finalized = c.store.Insert(finalized...)
		c.closedRev++
		c.rev++
	}
	c.adoptMoreAfter(p)
	c.trimClosed()
	return finalized, desync
}

// adoptMoreAfter takes the page's More.After only when the page's last node is
// at or above the highest anchor the store covers; a page from the middle says
// nothing about the tail.
func (c *Client) adoptMoreAfter(p Page) {
	if len(p.Parts) == 0 {
		return
	}
	_, top := p.Span()
	covered, ok := c.store.Top()
	// The open tail is coverage too, and Top() cannot see it.
	if id := c.store.OpenTurn(); id != 0 && c.store.OpenLen() > c.store.OpenHead() {
		liveTop := Anchor{Turn: uint64(id), Node: uint64(c.store.OpenLen() - 1)}
		if !ok || covered.Less(liveTop) {
			covered, ok = liveTop, true
		}
	}
	if !ok || !top.Less(covered) {
		m := c.store.More()
		m.After = p.More.After
		c.store.SetMore(m)
	}
}

// unitChars bounds one materialized message's payload. A node is never split.
const unitChars = 40000

// appendUnits appends nodes as one message, or as several when they exceed
// unitChars. Caller holds c.mu.
func (c *Client) appendUnits(dst []Message, turn, from int, nodes []livedoc.Node) []Message {
	c.offered += len(nodes)
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

// turnRole: a message with nodes speaks in the agent's voice; one without (an
// inquiry whose turn produced nothing) in the user's.
func turnRole(nodes []livedoc.Node) string {
	if len(nodes) == 0 {
		return livedoc.RoleInput
	}
	return livedoc.RoleOutput
}

// message builds one slice of a turn. Only the slice at node 0 carries the question.
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

// openStart is the release cursor, floored by what the store actually holds.
func (c *Client) openStart(turn int) int {
	e := c.emitted[turn]
	if c.store.OpenTurn() != turn {
		return e
	}
	if h := c.store.OpenHead(); h > e {
		return h
	}
	return e
}

// openMessage is the open turn's suffix as a message. Caller holds c.mu.
func (c *Client) openMessage() Message {
	turn := c.store.OpenTurn()
	e := c.openStart(turn)
	return c.message(turn, c.store.OpenBase(e), c.store.OpenSuffix(e))
}

// trimClosed enforces the retention limit; a turn no longer retained keeps no inquiry.
func (c *Client) trimClosed() {
	if c.closedLimit <= 0 || c.store.Count() <= c.closedLimit {
		return
	}
	c.closedRev++
	c.rev++
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

type View struct {
	Closed []Message
	Open   *Message
}

// ClosedRevision changes whenever the retained closed set does. Never zero, so
// zero is usable as "unset".
func (c *Client) ClosedRevision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closedRev + 1
}

// Revision changes on every change to the local view, the open message included.
func (c *Client) Revision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rev + 1
}

// OpenKey identifies the open message without materializing it.
func (c *Client) OpenKey() (turn int, from uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	turn = c.store.OpenTurn()
	if turn == 0 {
		return 0, 0, false
	}
	return turn, uint64(c.store.OpenBase(c.openStart(turn))), true
}

// Open returns the in-flight message, nil when none.
func (c *Client) Open() *Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store.OpenTurn() == 0 {
		return nil
	}
	m := c.openMessage()
	return &m
}

// View returns a snapshot of the current local state, Closed in (Turn, From) order.
func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// FoldDelta applies a NodeDelta: set merges fields, unset clears them, patch
// splices a streamed string field onto its previous value.
func FoldDelta(n livedoc.Node, d NodeDelta) livedoc.Node {
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
		n.Sender = asStr(v)
	case "status":
		n.Status = asStr(v)
	case "markdown":
		n.Markdown = asStr(v)
	case "role":
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
	case "output_base":
		n.OutputBase = int(asInt64(v))
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

// asUint64s, asSrcs and asFormDeltas accept both the in-process value and its
// JSON echo: a delta reaches the fold constructed locally or decoded off the wire.
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

// Contiguous reports whether the store holds an unbroken run from a to its newest.
func (c *Client) Contiguous(a Anchor) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store.Count() == 0 {
		return false
	}
	top, ok := c.store.TailFrom(1)
	if !ok {
		return false
	}
	_, gap := c.store.firstGap(a, top)
	return !gap
}
