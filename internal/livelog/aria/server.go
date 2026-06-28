package aria

import (
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Server is the authoritative aria state: an ordered list of closed messages and
// at most one open message (whose blocks are versioned). One routine —
// produce — serves both a one-shot Read(sinceLT) and a live subscription, so the
// stream is literally server-pushed pagination of the same read.
//
// Safe for concurrent use. Push callbacks run outside the lock and must not
// re-enter the Server.
type Server struct {
	mu     sync.Mutex
	closed []Message
	open   *openMsg
	subs   map[int]*subscriber
	nextID int
}

type openMsg struct {
	lt    int
	role  string
	order []string                // block ids, in arrival order
	block map[string]livedoc.Node // id -> current node (carrying its version)
}

type subscriber struct {
	conn ConnState
	push func(AriaRead)
}

// ConnState is what a connection has been delivered: the highest committed LT,
// and — for the open message — which block versions it has seen.
type ConnState struct {
	committedLT int
	liveLT      int
	liveSeen    map[string]int
}

// NewServer returns an empty aria server.
func NewServer() *Server { return &Server{subs: map[int]*subscriber{}} }

// Open starts a new open message at lt (close any prior one first).
func (s *Server) Open(lt int, role string) {
	s.mu.Lock()
	s.open = &openMsg{lt: lt, role: role, block: map[string]livedoc.Node{}}
	s.mu.Unlock()
	s.broadcast()
}

// Set adds or updates a block in the open message, assigning the next version.
// n carries the block's full current representation (Phase 1).
func (s *Server) Set(id string, n livedoc.Node) {
	s.mu.Lock()
	if s.open != nil {
		n.ID = id
		n.Version = s.open.block[id].Version + 1
		if _, ok := s.open.block[id]; !ok {
			s.open.order = append(s.open.order, id)
		}
		s.open.block[id] = n
	}
	s.mu.Unlock()
	s.broadcast()
}

// Close finalizes the open message into the committed list (clearing open).
func (s *Server) Close() {
	s.mu.Lock()
	if s.open != nil {
		m := Message{LT: s.open.lt, Role: s.open.role}
		for _, id := range s.open.order {
			m.Nodes = append(m.Nodes, s.open.block[id])
		}
		s.closed = append(s.closed, m)
		s.open = nil
	}
	s.mu.Unlock()
	s.broadcast()
}

// Commit appends an already-closed message (e.g. the user's prompt).
func (s *Server) Commit(m Message) {
	s.mu.Lock()
	s.closed = append(s.closed, m)
	s.mu.Unlock()
	s.broadcast()
}

// Read produces a catch-up page from sinceLT — used for a one-shot read or to
// seed a (re)connecting client.
func (s *Server) Read(sinceLT int) AriaRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := ConnState{committedLT: sinceLT}
	r, _ := s.produce(&c)
	return r
}

// Subscribe registers a live pusher seeded at sinceLT: it immediately receives a
// catch-up page (if non-empty), then a page on every change. Returns unsubscribe.
func (s *Server) Subscribe(sinceLT int, push func(AriaRead)) (cancel func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	sub := &subscriber{conn: ConnState{committedLT: sinceLT}, push: push}
	s.subs[id] = sub
	r, changed := s.produce(&sub.conn)
	s.mu.Unlock()
	if changed {
		push(r)
	}
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// broadcast computes each subscriber's delta (mutating its ConnState under the
// lock) and delivers outside the lock.
func (s *Server) broadcast() {
	s.mu.Lock()
	var pending []func()
	for _, sub := range s.subs {
		if r, changed := s.produce(&sub.conn); changed {
			r, push := r, sub.push
			pending = append(pending, func() { push(r) })
		}
	}
	s.mu.Unlock()
	for _, p := range pending {
		p()
	}
}

// produce computes the AriaRead delta for conn and advances conn. Caller holds
// s.mu. This single function is the whole protocol: closed messages past the
// cursor (as full, or as a close-patch if the connection streamed them live),
// then the open message's changed blocks.
func (s *Server) produce(c *ConnState) (AriaRead, bool) {
	var r AriaRead
	for _, m := range s.closed { // closed list is LT-ordered, so patches precede newer fulls
		if m.LT <= c.committedLT {
			continue
		}
		if m.LT == c.liveLT {
			// This connection already streamed this message's content live, so
			// only signal the close transition.
			r.Committed = append(r.Committed, Committed{LT: m.LT, Closed: true})
		} else {
			r.Committed = append(r.Committed, Committed{LT: m.LT, Role: m.Role, Nodes: m.Nodes})
		}
		c.committedLT = m.LT
	}
	if s.open != nil {
		if c.liveLT != s.open.lt {
			c.liveLT = s.open.lt
			c.liveSeen = map[string]int{}
		}
		var changed []livedoc.Node
		for _, id := range s.open.order {
			b := s.open.block[id]
			if c.liveSeen[id] < b.Version {
				changed = append(changed, b)
				c.liveSeen[id] = b.Version
			}
		}
		if len(changed) > 0 {
			r.Live = &Live{LT: s.open.lt, Role: s.open.role, Nodes: changed}
		}
	}
	return r, !r.Empty()
}
