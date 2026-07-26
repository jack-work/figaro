package aria

import (
	"sort"
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Client folds AriaReads into a local view. Live frames are folded into
// materialized livedoc.Node instances by id; a close marker promotes them iff the
// seen record version matches; any mismatch fires OnDesync with the last
// fully-committed LT so the caller can reconnect and re-read.
//
// OnClosed fires when a message finalizes; OnLive fires with the open message
// (its suffix nodes, and the turn's inquiry while the suffix starts the turn);
// OnDesync requests a catch-up from the given LT.
type Client struct {
	mu sync.Mutex

	closed          []Message
	closedSeen      map[int]bool
	closedFloor     int
	closedLimit     int
	closedRev       uint64
	lastCommittedLT int

	// The open turn, materialized. openTurn holds the TURN ID (see Message);
	// openFrom is the suffix boundary reported by Live.From — nodes below it
	// are closed and will never be touched again.
	openTurn       int
	openFrom       uint64
	openV          int
	openNodesSlice []livedoc.Node

	// emitted[turn] is how many of a turn's nodes have already gone out as
	// closed messages, and inquiry[turn] is that turn's opening question until
	// its first slice carries it away. The inquiry commits before the agent has
	// said anything, so it must be held here to be attached to whichever slice
	// turns out to start the turn.
	emitted map[int]int
	inquiry map[int]string

	OnClosed  func(Message)
	OnLive    func(Message)
	OnDesync  func(sinceLT int)
	OnMetrics func(Metrics)
}

// NewClient returns a fresh client.
func NewClient() *Client {
	return &Client{closedSeen: map[int]bool{}, emitted: map[int]int{}, inquiry: map[int]string{}}
}

// SetClosedLimit bounds retained closed messages. Zero keeps the default,
// unbounded behavior.
func (c *Client) SetClosedLimit(limit int) {
	c.mu.Lock()
	c.closedLimit = limit
	c.trimClosed()
	c.mu.Unlock()
}

// Cursor is the highest fully-committed LT — the resume point for a re-read.
func (c *Client) Cursor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCommittedLT
}

// OpenAnimating reports whether the open message has a running tool — i.e. a
// spinner that needs the periodic tick repaint. When false, a renderer can skip
// its timer-driven redraw entirely (content updates still arrive via Apply).
func (c *Client) OpenAnimating() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openTurn == 0 {
		return false
	}
	for _, n := range c.openNodesSlice {
		if n.Type == livedoc.NodeTool && n.Status == livedoc.StatusRunning {
			return true
		}
	}
	return false
}

// Apply folds one page.
// Apply folds a Page into the local view.
//
// A part is one of three things. Sealed: the turn stopped moving — finalize it
// from the part's snapshot, or from what we streamed if the part is just the
// marker. Live with deltas: fold them at their positional ids. Live without
// deltas: the streaming suffix closed but the turn continues, so keep holding
// it open. A part whose window sits entirely below Live.From carries no Live
// at all, which is exactly why such a page never needs re-fetching.
func (c *Client) Apply(p Page) {
	c.mu.Lock()
	var finalized []Message
	desync := -1
	metrics := p.Metrics

	for _, part := range p.Parts {
		id := int(part.ID)

		// The inquiry is TEXT ON THE TURN, not a node, and it commits before the
		// agent has said anything — so it arrives on a part of its own, with no
		// nodes and no Live. Hold it until a slice starts the turn, and open the
		// turn now so the question paints the instant it is asked.
		if part.Inquiry != "" && !part.ClippedHead {
			c.inquiry[id] = part.Inquiry
			if c.openTurn != id && !c.seenClosed(id) {
				c.resetOpen()
				c.openTurn = id
			}
		}

		// Adopt any nodes the part carries. From is the positional id of
		// Nodes[0], so a clipped part slots into place rather than replacing.
		if len(part.Nodes) > 0 {
			if c.openTurn != id {
				c.resetOpen()
				c.openTurn = id
			}
			c.absorb(part.From, part.Nodes)
		}

		if part.Live != nil {
			if c.openTurn != id {
				c.resetOpen()
				c.openTurn = id
			}
			c.openFrom = part.Live.From
			for _, nd := range part.Live.Nodes {
				c.foldAt(nd)
			}
			// Everything below Live.From is closed for good. Release it now so
			// the head of a long turn reaches scrollback instead of riding the
			// live region until the turn seals.
			if n := int(c.openFrom); n > c.emitted[id] && n <= len(c.openNodesSlice) {
				finalized = append(finalized, c.message(id, c.emitted[id],
					append([]livedoc.Node(nil), c.openNodesSlice[c.emitted[id]:n]...)))
				c.emitted[id] = n
			}
			if len(part.Live.Nodes) > 0 {
				c.openV = part.Live.V
			} else if c.openV != part.Live.V && len(part.Nodes) == 0 {
				// A close marker whose version we never reached: we missed
				// frames, so ask for a catch-up rather than show a gap.
				desync = c.lastCommittedLT
			}
		}

		if !part.Sealed {
			continue
		}
		if c.seenClosed(id) {
			c.advanceCommitted(id)
			continue
		}
		nodes := part.Nodes
		if c.openTurn == id && len(c.openNodesSlice) >= len(nodes) {
			nodes = c.openNodes()
		}
		c.closedSeen[id] = true
		// Everything not already released closes as one message. A turn that
		// produced nothing at all (interrupted before its first block) still
		// closes one, carrying the inquiry — otherwise the question the user
		// asked would never reach scrollback.
		if s := c.emitted[id]; s < len(nodes) {
			finalized = append(finalized, c.message(id, s, nodes[s:]))
		} else if s == 0 && c.inquiry[id] != "" {
			finalized = append(finalized, c.message(id, 0, nil))
		}
		delete(c.emitted, id)
		delete(c.inquiry, id)
		c.advanceCommitted(id)
		if c.openTurn == id {
			c.resetOpen()
		}
	}

	if len(finalized) > 0 {
		c.closed = append(c.closed, finalized...)
		c.closedRev++
	}
	c.trimClosed()

	haveLive := c.openTurn != 0
	// The live region is the OPEN SUFFIX only. Nodes below openFrom were
	// already released as closed messages above; redrawing them here would
	// print them twice.
	live := c.openMessage()
	c.mu.Unlock()

	if metrics != nil && c.OnMetrics != nil {
		c.OnMetrics(*metrics)
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
}

// absorb slots a contiguous run of nodes in at their positional ids.
func (c *Client) absorb(from uint64, nodes []livedoc.Node) {
	need := int(from) + len(nodes)
	for len(c.openNodesSlice) < need {
		c.openNodesSlice = append(c.openNodesSlice, livedoc.Node{})
	}
	copy(c.openNodesSlice[from:], nodes)
}

// foldAt applies a delta at its positional id, growing the slice as the open
// suffix appends.
func (c *Client) foldAt(nd NodeDelta) {
	for uint64(len(c.openNodesSlice)) <= nd.ID {
		c.openNodesSlice = append(c.openNodesSlice, livedoc.Node{})
	}
	c.openNodesSlice[nd.ID] = foldDelta(c.openNodesSlice[nd.ID], nd)
}

// turnRole is the voice a message renders under. Every node is agent output:
// the inquiry is text on the turn, and a steer is an inline annotation inside
// the agent's run, not a voice of its own. So a message with nodes speaks in
// the agent's voice, and one without — an inquiry whose turn produced nothing —
// in the user's.
func turnRole(nodes []livedoc.Node) string {
	if len(nodes) == 0 {
		return livedoc.RoleInput
	}
	return livedoc.RoleOutput
}

// message builds one closed slice of a turn. Only the slice that STARTS the
// turn carries the inquiry; a later one leaving it set would print the question
// again at every page boundary.
func (c *Client) message(turn, from int, nodes []livedoc.Node) Message {
	m := Message{Turn: turn, From: uint64(from), Role: turnRole(nodes), Nodes: nodes}
	if from == 0 {
		m.Inquiry = c.inquiry[turn]
	}
	return m
}

// openMessage is the open turn's suffix as a message. Caller holds the lock.
func (c *Client) openMessage() Message {
	return c.message(c.openTurn, int(c.openFrom), c.openSuffix())
}

func (c *Client) trimClosed() {
	if c.closedLimit <= 0 || len(c.closed) <= c.closedLimit {
		return
	}
	c.closedRev++
	sort.SliceStable(c.closed, func(i, j int) bool {
		if c.closed[i].Turn != c.closed[j].Turn {
			return c.closed[i].Turn < c.closed[j].Turn
		}
		return c.closed[i].From < c.closed[j].From
	})
	c.closed = append([]Message(nil), c.closed[len(c.closed)-c.closedLimit:]...)
	c.closedSeen = make(map[int]bool, len(c.closed))
	for _, m := range c.closed {
		c.closedSeen[m.Turn] = true
	}
	if len(c.closed) > 0 && c.closed[0].Turn > c.closedFloor {
		c.closedFloor = c.closed[0].Turn
	}
}

func (c *Client) seenClosed(lt int) bool {
	return lt < c.closedFloor || c.closedSeen[lt]
}

// View is the client's local reconstruction.
type View struct {
	Closed []Message
	Open   *Message
}

// ClosedRevision is a counter bumped whenever the retained closed set changes
// (a message finalized, or the retention limit trimmed one away). A viewer that
// derives state from the closed tail — the transcript's page window does — can
// hold it and skip the rebuild while it is unchanged, instead of re-deriving
// per frame. Never zero after the first change, so zero is usable as "unset".
func (c *Client) ClosedRevision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closedRev + 1
}

// Open returns just the open, in-flight message (nil when none). View copies
// and sorts the whole retained closed set; callers that only want the live
// message — every transcript frame asks for it — should not pay for that.
//
// It is the open SUFFIX, for the same reason the push path is (see Apply):
// nodes below openFrom were already released as closed messages, so carrying
// them here prints them twice and wraps the prompt in the agent's header. From
// is the suffix's offset within the turn, so a caller can place it.
func (c *Client) Open() *Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openTurn == 0 {
		return nil
	}
	m := c.openMessage()
	return &m
}

// View returns a snapshot of the current local state.
func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	closed := append([]Message(nil), c.closed...)
	// c.closed is in arrival order, which interleaves when a live-sealed message
	// (this session) precedes a catch-up Read of older history. Order by the FULL
	// identity (Turn, From): a turn arrives as several slices, so sorting on Turn
	// alone leaves their relative order to however they happened to arrive.
	sort.SliceStable(closed, func(i, j int) bool {
		if closed[i].Turn != closed[j].Turn {
			return closed[i].Turn < closed[j].Turn
		}
		return closed[i].From < closed[j].From
	})
	v := View{Closed: closed}
	if c.openTurn != 0 {
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

func (c *Client) openNodes() []livedoc.Node {
	return append([]livedoc.Node(nil), c.openNodesSlice...)
}

// openSuffix is the still-mutable tail of the open turn: everything at or above
// Live.From. The committed head has already been released to scrollback.
func (c *Client) openSuffix() []livedoc.Node {
	n := int(c.openFrom)
	if n < 0 || n > len(c.openNodesSlice) {
		n = 0
	}
	return append([]livedoc.Node(nil), c.openNodesSlice[n:]...)
}

func (c *Client) resetOpen() {
	c.openTurn, c.openV, c.openFrom = 0, 0, 0
	c.openNodesSlice = nil
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
	case "status":
		n.Status = asStr(v)
	case "markdown":
		n.Markdown = asStr(v)
	case "role":
		// The server sends this (fullSet/diff both emit "role"), and dropping it
		// silently made every STREAMED node look like agent output — which is
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
	case "id":
		n.ID = asStr(v)
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
// way — constructed locally in a test, or decoded off the wire.
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
