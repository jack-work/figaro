package aria

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Server is the authoritative aria state: closed messages plus at most one open
// message. Live subscribers get incremental frames (the field deltas of each
// update, versioned); Read returns a catch-up snapshot (closed messages in full
// + the open message as a full-create frame). Push callbacks run outside the lock
// and must not re-enter the Server.
type Server struct {
	mu     sync.Mutex
	closed []Message
	open   *openMsg
	subs   map[int]func(AriaRead)
	nextID int
}

type openMsg struct {
	lt    int
	role  string
	order []string
	block map[string]livedoc.Node
	ver   int // next frame version (0-indexed); last emitted is ver-1
}

// NewServer returns an empty aria server.
func NewServer() *Server { return &Server{subs: map[int]func(AriaRead){}} }

// Open starts a new open message at lt (close any prior one first). It emits no
// frame; the first Update carries the role at v 0.
func (s *Server) Open(lt int, role string) {
	s.mu.Lock()
	s.open = &openMsg{lt: lt, role: role, block: map[string]livedoc.Node{}}
	s.mu.Unlock()
}

// Update applies the new full node list to the open message and broadcasts a
// frame of the field deltas vs the prior state (v++ if anything changed).
func (s *Server) Update(nodes []livedoc.Node) {
	s.mu.Lock()
	if s.open == nil {
		s.mu.Unlock()
		return
	}
	var deltas []NodeDelta
	for i, n := range nodes {
		id := blockID(i, n)
		old, existed := s.open.block[id]
		if d := delta(id, old, existed, n); !d.Empty() {
			deltas = append(deltas, d)
		}
		if !existed {
			s.open.order = append(s.open.order, id)
		}
		s.open.block[id] = n
	}
	if len(deltas) == 0 {
		s.mu.Unlock()
		return
	}
	v := s.open.ver
	role := ""
	if v == 0 {
		role = s.open.role
	}
	s.open.ver++
	frame := AriaRead{Live: &Live{LT: s.open.lt, V: v, Role: role, Nodes: deltas}}
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, frame)
}

// Close finalizes the open message and broadcasts a close marker {lt, v} where v
// is the last emitted frame version.
func (s *Server) Close() {
	s.mu.Lock()
	if s.open == nil {
		s.mu.Unlock()
		return
	}
	m := Message{LT: s.open.lt, Role: s.open.role}
	for _, id := range s.open.order {
		m.Nodes = append(m.Nodes, s.open.block[id])
	}
	lastV := s.open.ver - 1
	if lastV < 0 {
		lastV = 0
	}
	s.closed = append(s.closed, m)
	s.open = nil
	frame := AriaRead{Committed: []Committed{{LT: m.LT, V: lastV}}}
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, frame)
}

// Commit appends an already-closed message (history rebuild, or a non-streamed
// message) and broadcasts it as a full snapshot.
func (s *Server) Commit(m Message) {
	s.mu.Lock()
	s.closed = append(s.closed, m)
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, AriaRead{Committed: []Committed{{LT: m.LT, Role: m.Role, Nodes: m.Nodes}}})
}

// Subscribe registers a live pusher for subsequent frames (no initial snapshot;
// use Read to catch up). Returns unsubscribe.
func (s *Server) Subscribe(push func(AriaRead)) (cancel func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.subs[id] = push
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// Read returns a catch-up snapshot from sinceLT: closed messages after it in
// full, plus the open message (if any) as a full-create frame at its version.
func (s *Server) Read(sinceLT int) AriaRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r AriaRead
	for _, m := range s.closed {
		if m.LT <= sinceLT {
			continue
		}
		r.Committed = append(r.Committed, Committed{LT: m.LT, Role: m.Role, Nodes: m.Nodes})
	}
	if s.open != nil && len(s.open.order) > 0 {
		deltas := make([]NodeDelta, 0, len(s.open.order))
		for _, id := range s.open.order {
			deltas = append(deltas, fullSet(id, s.open.block[id]))
		}
		v := s.open.ver - 1
		if v < 0 {
			v = 0
		}
		r.Live = &Live{LT: s.open.lt, V: v, Role: s.open.role, Nodes: deltas}
	}
	return r
}

func (s *Server) subsLocked() []func(AriaRead) {
	out := make([]func(AriaRead), 0, len(s.subs))
	for _, f := range s.subs {
		out = append(out, f)
	}
	return out
}

func deliver(subs []func(AriaRead), r AriaRead) {
	for _, f := range subs {
		f(r)
	}
}

// delta computes the field-level change from old (when existed) to n for block id.
func delta(id string, old livedoc.Node, existed bool, n livedoc.Node) NodeDelta {
	if !existed {
		return fullSet(id, n)
	}
	d := NodeDelta{ID: id}
	set := map[string]any{}
	var unset []string
	scalar := func(field, ov, nv string) {
		if nv == ov {
			return
		}
		if nv == "" {
			unset = append(unset, field)
		} else {
			set[field] = nv
		}
	}
	if n.Type != old.Type {
		set["type"] = string(n.Type)
	}
	scalar("name", old.Name, n.Name)
	scalar("status", old.Status, n.Status)
	// Streamed string fields. Phase 1: whole value via set/unset. (Phase 2
	// replaces this with `patch` splices.)
	scalar("markdown", old.Markdown, n.Markdown)
	scalar("output", old.Output, n.Output)
	if !reflect.DeepEqual(old.Args, n.Args) {
		if n.Args == nil {
			unset = append(unset, "args")
		} else {
			set["args"] = n.Args
		}
	}
	if len(set) > 0 {
		d.Set = set
	}
	d.Unset = unset
	return d
}

// fullSet is the creation/snapshot delta: every non-zero field in set.
func fullSet(id string, n livedoc.Node) NodeDelta {
	set := map[string]any{"type": string(n.Type)}
	if n.Name != "" {
		set["name"] = n.Name
	}
	if n.Args != nil {
		set["args"] = n.Args
	}
	if n.Status != "" {
		set["status"] = n.Status
	}
	if n.Markdown != "" {
		set["markdown"] = n.Markdown
	}
	if n.Output != "" {
		set["output"] = n.Output
	}
	return NodeDelta{ID: id, Set: set}
}

// blockID is a node's stable handle: its tool_call_id, or its positional id.
func blockID(i int, n livedoc.Node) string {
	if n.ID != "" {
		return n.ID
	}
	return fmt.Sprintf("n%d", i)
}
