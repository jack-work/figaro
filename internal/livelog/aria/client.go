package aria

import (
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Client folds AriaReads into a local view. Application is idempotent — committed
// by LT, live blocks by version — so a catch-up/live overlap or a redundant page
// can't double-apply. The cursor (highest committed LT) is what you re-read from
// after a disconnect.
//
// OnClosed fires when a message finalizes (the renderer seals it to scrollback,
// once). OnLive fires with the open message's current ordered blocks (the
// renderer repaints just this — the only mutable region).
type Client struct {
	mu sync.Mutex

	lastLT     int          // highest committed LT applied (the catch-up cursor)
	closedSeen map[int]bool // LTs already finalized (invariant guard)
	closed     []Message    // finalized messages, in delivery order

	openLT    int
	openRole  string
	openOrder []string
	openBlock map[string]livedoc.Node

	OnClosed func(Message)
	OnLive   func(lt int, role string, nodes []livedoc.Node)
}

// NewClient returns a fresh client at cursor 0.
func NewClient() *Client {
	return &Client{closedSeen: map[int]bool{}, openBlock: map[string]livedoc.Node{}}
}

// Cursor is the highest committed LT applied — pass it to Read/Subscribe to
// resume after a disconnect.
func (c *Client) Cursor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastLT
}

// Apply folds one page, firing OnClosed for finalized messages and OnLive for
// the current open view.
func (c *Client) Apply(r AriaRead) {
	c.mu.Lock()
	var finalized []Message
	for _, cm := range r.Committed {
		if c.closedSeen[cm.LT] {
			continue // invariant: a given LT finalizes once per connection
		}
		c.closedSeen[cm.LT] = true
		if cm.LT > c.lastLT {
			c.lastLT = cm.LT
		}
		switch {
		case cm.Closed && c.openLT == cm.LT:
			// close-patch for the message we were streaming: promote our blocks.
			finalized = append(finalized, Message{LT: cm.LT, Role: c.openRole, Nodes: c.openNodes()})
			c.resetOpen()
		case cm.Closed:
			// a close-patch for a message we never streamed (shouldn't happen on
			// a well-formed connection); record the boundary, no content.
			finalized = append(finalized, Message{LT: cm.LT})
		default:
			// A full closed message. If we were streaming it live (it closed
			// while we were disconnected and we reconnected with an earlier
			// cursor), adopt the canonical content and drop our stale open state.
			if cm.LT == c.openLT {
				c.resetOpen()
			}
			finalized = append(finalized, Message{LT: cm.LT, Role: cm.Role, Nodes: cm.Nodes})
		}
	}

	c.closed = append(c.closed, finalized...)

	var (
		haveLive  bool
		liveLT    int
		liveRole  string
		liveNodes []livedoc.Node
	)
	if r.Live != nil {
		if c.openLT != r.Live.LT {
			c.openLT = r.Live.LT
			c.openRole = r.Live.Role
			c.openOrder = nil
			c.openBlock = map[string]livedoc.Node{}
		}
		for _, n := range r.Live.Nodes {
			if cur, ok := c.openBlock[n.ID]; ok && n.Version <= cur.Version {
				continue // already have this version or newer
			} else if !ok {
				c.openOrder = append(c.openOrder, n.ID)
			}
			c.openBlock[n.ID] = n
		}
		haveLive, liveLT, liveRole, liveNodes = true, c.openLT, c.openRole, c.openNodes()
	}
	c.mu.Unlock()

	for _, m := range finalized {
		if c.OnClosed != nil {
			c.OnClosed(m)
		}
	}
	if haveLive && c.OnLive != nil {
		c.OnLive(liveLT, liveRole, liveNodes)
	}
}

// View is the client's local reconstruction of the aria.
type View struct {
	Closed []Message
	Open   *Message // the open message (nil if none)
}

// View returns a snapshot of the current local state.
func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := View{Closed: append([]Message(nil), c.closed...)}
	if c.openLT != 0 {
		v.Open = &Message{LT: c.openLT, Role: c.openRole, Nodes: c.openNodes()}
	}
	return v
}

// openNodes returns the open message's blocks in arrival order. Caller holds mu.
func (c *Client) openNodes() []livedoc.Node {
	out := make([]livedoc.Node, 0, len(c.openOrder))
	for _, id := range c.openOrder {
		out = append(out, c.openBlock[id])
	}
	return out
}

// resetOpen clears the open-message state. Caller holds mu.
func (c *Client) resetOpen() {
	c.openLT = 0
	c.openRole = ""
	c.openOrder = nil
	c.openBlock = map[string]livedoc.Node{}
}
