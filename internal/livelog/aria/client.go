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
	lastCommittedLT int

	openLT    int
	openRole  string
	openV     int
	openOrder []string
	openBlock map[string]livedoc.Node

	OnClosed  func(Message)
	OnLive    func(lt int, role string, nodes []livedoc.Node)
	OnDesync  func(sinceLT int)
	OnMetrics func(Metrics)
}

// NewClient returns a fresh client.
func NewClient() *Client {
	return &Client{closedSeen: map[int]bool{}, openBlock: map[string]livedoc.Node{}}
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
	if c.openLT == 0 {
		return false
	}
	for _, n := range c.openBlock {
		if n.Type == livedoc.NodeTool && n.Status == livedoc.StatusRunning {
			return true
		}
	}
	return false
}

// Apply folds one page.
func (c *Client) Apply(r AriaRead) {
	c.mu.Lock()
	var finalized []Message
	desync := -1
	metrics := r.Metrics
	for _, cm := range r.Committed {
		switch {
		case cm.Full():
			if !c.seenClosed(cm.LT) {
				c.closedSeen[cm.LT] = true
				finalized = append(finalized, Message{LT: cm.LT, Role: cm.Role, Nodes: cm.Nodes})
			}
			if cm.LT == c.openLT {
				c.resetOpen()
			}
			c.advanceCommitted(cm.LT)
		case c.openLT == cm.LT && c.openV == cm.V:
			// close marker for what we streamed, versions agree: promote.
			if !c.closedSeen[cm.LT] {
				c.closedSeen[cm.LT] = true
				finalized = append(finalized, Message{LT: cm.LT, Role: c.openRole, Nodes: c.openNodes()})
			}
			c.advanceCommitted(cm.LT)
			c.resetOpen()
		default:
			// version mismatch / message we don't hold open: catch up.
			desync = c.lastCommittedLT
		}
	}

	c.closed = append(c.closed, finalized...)
	c.trimClosed()

	var (
		haveLive  bool
		liveLT    int
		liveRole  string
		liveNodes []livedoc.Node
	)
	if r.Live != nil {
		f := r.Live
		if c.openLT != f.LT {
			c.openLT = f.LT
			c.openRole = f.Role
			c.openOrder = nil
			c.openBlock = map[string]livedoc.Node{}
		}

		if f.Role != "" {
			c.openRole = f.Role
		}
		for _, nd := range f.Nodes {
			cur, ok := c.openBlock[nd.ID]
			if !ok {
				c.openOrder = append(c.openOrder, nd.ID)
			}
			c.openBlock[nd.ID] = foldDelta(cur, nd)
		}
		c.openV = f.V
		haveLive, liveLT, liveRole, liveNodes = true, c.openLT, c.openRole, c.openNodes()
	}
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
		c.OnLive(liveLT, liveRole, liveNodes)
	}
	if desync >= 0 && c.OnDesync != nil {
		c.OnDesync(desync)
	}
}

func (c *Client) trimClosed() {
	if c.closedLimit <= 0 || len(c.closed) <= c.closedLimit {
		return
	}
	sort.SliceStable(c.closed, func(i, j int) bool { return c.closed[i].LT < c.closed[j].LT })
	c.closed = append([]Message(nil), c.closed[len(c.closed)-c.closedLimit:]...)
	c.closedSeen = make(map[int]bool, len(c.closed))
	for _, m := range c.closed {
		c.closedSeen[m.LT] = true
	}
	if len(c.closed) > 0 && c.closed[0].LT > c.closedFloor {
		c.closedFloor = c.closed[0].LT
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

// View returns a snapshot of the current local state.
func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	closed := append([]Message(nil), c.closed...)
	// c.closed is in arrival order, which interleaves when a live-sealed message
	// (this session) precedes a catch-up Read of older history. Sort by LT so the
	// transcript renders the conversation in order.
	sort.SliceStable(closed, func(i, j int) bool { return closed[i].LT < closed[j].LT })
	v := View{Closed: closed}
	if c.openLT != 0 {
		v.Open = &Message{LT: c.openLT, Role: c.openRole, Nodes: c.openNodes()}
	}
	return v
}

func (c *Client) advanceCommitted(lt int) {
	if lt > c.lastCommittedLT {
		c.lastCommittedLT = lt
	}
}

func (c *Client) openNodes() []livedoc.Node {
	out := make([]livedoc.Node, 0, len(c.openOrder))
	for _, id := range c.openOrder {
		out = append(out, c.openBlock[id])
	}
	return out
}

func (c *Client) resetOpen() {
	c.openLT, c.openRole, c.openV = 0, "", 0
	c.openOrder = nil
	c.openBlock = map[string]livedoc.Node{}
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
