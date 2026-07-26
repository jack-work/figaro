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
// OnClosed fires when a message finalizes; OnLive fires with the open message's
// current ordered nodes; OnDesync requests a catch-up from the given LT.
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
	openTurn         int
	openFrom       uint64
	openV          int
	openNodesSlice []livedoc.Node

	// emitted[turn] is how many of a turn's nodes have already gone out as
	// closed messages. The prompt is committed (below Live.From) long before
	// its turn seals, so it must freeze to scrollback immediately rather than
	// sit in the redrawable live region wearing the agent's header.
	emitted map[int]int

	OnClosed  func(Message)
	OnLive    func(turn int, from uint64, role string, nodes []livedoc.Node)
	OnDesync  func(sinceLT int)
	OnMetrics func(Metrics)
}

// NewClient returns a fresh client.
func NewClient() *Client {
	return &Client{closedSeen: map[int]bool{}, emitted: map[int]int{}}
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
			// the prompt reaches scrollback under its own voice instead of
			// riding the live region until the turn seals.
			if n := int(c.openFrom); n > c.emitted[id] && n <= len(c.openNodesSlice) {
				for s := c.emitted[id]; s < n; {
					e, role := VoiceRunEnd(c.openNodesSlice[:n], s)
					finalized = append(finalized, Message{
						Turn: id, From: uint64(s), Role: role,
						Nodes: append([]livedoc.Node(nil), c.openNodesSlice[s:e]...),
					})
					s = e
				}
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
		// One Message per voice run: the prompt closes under "you", the agent's
		// reply under "figaro". Emitting one Message per TURN printed the user's
		// own words beneath the agent's name.
		for s := c.emitted[id]; s < len(nodes); {
			e, role := VoiceRunEnd(nodes, s)
			finalized = append(finalized, Message{
				Turn: id, From: uint64(s), Role: role, Nodes: nodes[s:e],
			})
			s = e
		}
		delete(c.emitted, id)
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
	// print them twice and wrap the prompt in the agent's header.
	liveLT, liveFrom, liveNodes := c.openTurn, c.openFrom, c.openSuffix()
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
		c.OnLive(liveLT, liveFrom, turnRole(liveNodes), liveNodes)
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

// turnRole reports the voice a turn is rendered under. A turn holds both, so
// this is only the coarse hint the older surfaces still ask for; per-node Role
// is the real answer.
//
// A turn is an exchange, so it closes under the assistant's bookend unless it
// holds nothing but the prompt. Nodes carrying no explicit role are agent
// output — a streamed delta need not repeat it on every frame.
func turnRole(nodes []livedoc.Node) string {
	if len(nodes) == 0 {
		return ""
	}
	for _, n := range nodes {
		if n.Role != livedoc.RoleInput {
			return livedoc.RoleOutput
		}
	}
	return livedoc.RoleInput
}

// nodeVoice is the voice a single node speaks in. The prompt and any steering
// interjection are the user's; prose, thinking and tools are the agent's. A
// node carrying no explicit role is agent output, because a streamed delta need
// not repeat the role on every frame.
func nodeVoice(n livedoc.Node) string {
	if n.Role == livedoc.RoleInput {
		return livedoc.RoleInput
	}
	return livedoc.RoleOutput
}

// VoiceRunEnd returns the end of the contiguous same-voice run beginning at
// start, plus the voice that run speaks in.
//
// A turn holds BOTH voices, so a header must be printed per RUN, not once per
// turn — otherwise the user's own question renders under the agent's name.
// Every surface that labels a unit derives its runs here, so the client (inline)
// and the pager cannot drift apart on where a voice changes.
//
// Index-based rather than callback- or slice-based so the hot render paths stay
// allocation-free; the common shape is one or two runs.
func VoiceRunEnd(nodes []livedoc.Node, start int) (int, string) {
	if start >= len(nodes) {
		return start, ""
	}
	cur := nodeVoice(nodes[start])
	i := start + 1
	for i < len(nodes) && nodeVoice(nodes[i]) == cur {
		i++
	}
	return i, cur
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
	suffix := c.openSuffix()
	return &Message{Turn: c.openTurn, From: c.openFrom, Role: turnRole(suffix), Nodes: suffix}
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
		suffix := c.openSuffix()
		v.Open = &Message{Turn: c.openTurn, From: c.openFrom, Role: turnRole(suffix), Nodes: suffix}
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
		// silently made every STREAMED node look like agent output — so the
		// voice-run split could only ever work on committed snapshots, and a
		// live prompt rendered under the agent's header.
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
