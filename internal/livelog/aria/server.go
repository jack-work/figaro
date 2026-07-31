package aria

import (
	"reflect"
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Server materializes one aria's turns and broadcasts changes as Pages.
//
// State is a turn list whose last entry may still be growing. Within a growing
// turn, `open` holds the suffix currently being streamed: its nodes are
// mutable and versioned, everything below `open.from` is closed and can never
// receive another delta. That boundary is what a client needs to know which
// half of a page it may cache forever.
type Server struct {
	mu     sync.Mutex
	turns  []Turn
	open   *openTurn
	subs   map[int]func(Page)
	nextID int

	// baseTurn/base remember where a turn's streaming region begins. The
	// producer recomposes the WHOLE region on every frame (it reads the turn's
	// messages back from the log), so a second round inside one turn must
	// reopen at the same boundary rather than after the first round — else the
	// recomposed nodes would be appended again instead of replacing.
	baseTurn uint64
	base     uint64
}

// openTurn is the streaming suffix of turns[len-1]: the nodes at ids
// >= from, materialized so Update can diff against the previous frame.
type openTurn struct {
	id    uint64
	from  uint64
	nodes []livedoc.Node
	ver   int // next frame version (0-indexed); last emitted is ver-1
}

// NewServer returns an empty aria server.
func NewServer() *Server { return &Server{subs: map[int]func(Page){}} }

// LastTurn returns the id of the newest known turn (0 if none) — the cursor a
// client streams from.
func (s *Server) LastTurn() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		return 0
	}
	return s.turns[len(s.turns)-1].ID
}

// Turns returns a snapshot of materialized turns, the last carrying its open
// suffix and Live boundary when one is streaming.
func (s *Server) Turns() []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Server) HasOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open != nil
}

// Restore replaces materialized state without broadcasting.
func (s *Server) Restore(turns []Turn) {
	s.mu.Lock()
	s.turns = append([]Turn(nil), turns...)
	s.open = nil
	s.mu.Unlock()
}

// AdoptIfEmpty seeds a server that has never materialized anything, and
// reports whether it did. s.turns fills as turns seal, so an aria this process
// has not run a turn for — a dormant one, just attached — holds nothing and
// would serve an empty page. The caller composes from the durable log outside
// this lock (it is not cheap) and offers the result here; if a turn opened in
// the meantime the live state is already authoritative and the offer is
// declined.
func (s *Server) AdoptIfEmpty(turns []Turn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) > 0 || s.open != nil || len(turns) == 0 {
		return false
	}
	s.turns = append([]Turn(nil), turns...)
	return true
}

// OpenTurn begins a streaming suffix on turn id, creating the turn if this is
// its first message. The boundary is wherever the turn's committed nodes
// currently end, so a multi-round turn reopens further along each time rather
// than restarting. It emits no frame; the first Update carries v 0.
func (s *Server) OpenTurn(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.turns); n == 0 || s.turns[n-1].ID != id {
		s.turns = append(s.turns, Turn{ID: id})
	}
	if s.baseTurn != id {
		s.baseTurn = id
		s.base = uint64(len(s.turns[len(s.turns)-1].Nodes))
	}
	s.open = &openTurn{id: id, from: s.base}
}

// Update applies the suffix's new full node list and broadcasts the field
// deltas against the prior frame (v++ if anything changed). Node ids are
// positional — the i'th suffix node is id from+i — so identity needs no
// separate key.
func (s *Server) Update(nodes []livedoc.Node) {
	s.mu.Lock()
	if s.open == nil {
		s.mu.Unlock()
		return
	}
	var deltas []NodeDelta
	for i, n := range nodes {
		id := s.open.from + uint64(i)
		if i < len(s.open.nodes) {
			if d := delta(id, s.open.nodes[i], n); !d.Empty() {
				deltas = append(deltas, d)
			}
			continue
		}
		deltas = append(deltas, fullSet(id, n))
	}
	s.open.nodes = append([]livedoc.Node(nil), nodes...)
	if len(deltas) == 0 {
		s.mu.Unlock()
		return
	}
	v := s.open.ver
	s.open.ver++
	inquiry, segments := s.inquiryOfLocked(s.open.id)
	frame := Page{Parts: []TurnPart{{
		Turn: Turn{ID: s.open.id, Inquiry: inquiry, InquirySegments: segments,
			Live: &Live{From: s.open.from, V: v, Nodes: deltas}},
		From: s.open.from,
	}}}
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, frame)
}

// Close folds the streaming suffix into its turn. The turn itself stays open —
// a turn spans many messages — so nothing is sealed here; Seal does that.
func (s *Server) Close() {
	s.mu.Lock()
	if s.open == nil {
		s.mu.Unlock()
		return
	}
	i := len(s.turns) - 1
	t := s.turns[i]
	t.Nodes = append(t.Nodes[:s.open.from:s.open.from], s.open.nodes...)
	s.turns[i] = t
	lastV := s.open.ver - 1
	if lastV < 0 {
		lastV = 0
	}
	id, from := s.open.id, s.open.from
	s.open = nil
	inquiry, segments := s.inquiryOfLocked(id)
	frame := Page{Parts: []TurnPart{{
		Turn: Turn{ID: id, Inquiry: inquiry, InquirySegments: segments,
			Live: &Live{From: from, V: lastV}},
		From: from,
	}}}
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, frame)
}

// Seal marks the newest turn finished: it stops moving, and every node in it
// is immutable from here. This is the one moment the word means — and, later,
// the moment the turn is written to its xwal channel.
func (s *Server) Seal(lts []uint64) {
	s.mu.Lock()
	if len(s.turns) == 0 {
		s.mu.Unlock()
		return
	}
	i := len(s.turns) - 1
	if s.turns[i].Sealed {
		s.mu.Unlock() // already sealed: sealing is idempotent and silent
		return
	}
	s.turns[i].Sealed = true
	if len(lts) > 0 {
		s.turns[i].LTs = lts
	}
	sealed := s.turns[i]
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, Page{Parts: []TurnPart{{Turn: sealed}}})
}

// Abandon drops the streaming suffix without folding it in (a round that
// failed before its message reached the IR). It broadcasts nothing: connected
// clients keep showing the partial until the next turn opens and resets it.
func (s *Server) Abandon() {
	s.mu.Lock()
	s.open = nil
	s.mu.Unlock()
}

// Commit appends an already-complete turn and broadcasts it as a snapshot.
func (s *Server) Commit(t Turn) {
	s.mu.Lock()
	s.turns = append(s.turns, t)
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, Page{Parts: []TurnPart{{Turn: t}}})
}

// Subscribe registers a live pusher for subsequent frames (no initial
// snapshot; use Read to catch up). Returns unsubscribe.
func (s *Server) Subscribe(push func(Page)) (cancel func()) {
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

// Read pages forward from at; ReadBefore pages backward. Both are the same
// cut, differing only in which side of the anchor the budget is spent on —
// that is what lets a scrolling client pull an earlier or a later page from
// wherever it happens to be.
func (s *Server) Read(at Anchor, budget int) Page {
	return Paginate(s.Turns(), at, Forward, budget)
}

func (s *Server) ReadBefore(at Anchor, budget int) Page {
	return PaginateBefore(s.Turns(), at, budget)
}

// snapshotLocked copies the turn list, attaching the streaming suffix and its
// Live boundary to the turn that owns it.
func (s *Server) snapshotLocked() []Turn {
	out := append([]Turn(nil), s.turns...)
	if s.open == nil {
		return out
	}
	i := len(out) - 1
	if i < 0 || out[i].ID != s.open.id {
		out = append(out, Turn{ID: s.open.id})
		i = len(out) - 1
	}
	t := out[i]
	t.Nodes = append(append([]livedoc.Node(nil), t.Nodes[:s.open.from]...), s.open.nodes...)
	v := s.open.ver - 1
	if v < 0 {
		v = 0
	}
	t.Sealed = false
	t.Live = &Live{From: s.open.from, V: v}
	out[i] = t
	return out
}

func (s *Server) subsLocked() []func(Page) {
	out := make([]func(Page), 0, len(s.subs))
	for _, f := range s.subs {
		out = append(out, f)
	}
	return out
}

func deliver(subs []func(Page), p Page) {
	for _, push := range subs {
		push(p)
	}
}

// delta computes the field-level change from old to n for node id.
func delta(id uint64, old, n livedoc.Node) NodeDelta {
	d := NodeDelta{ID: id}
	set := map[string]any{}
	var unset []string
	patch := map[string]livedoc.Delta{}
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
	scalarInt := func(field string, ov, nv int64) {
		if ov == nv {
			return
		}
		if nv == 0 {
			unset = append(unset, field)
			return
		}
		set[field] = nv
	}
	// streamed handles the growable string fields: cleared -> unset, first
	// appearance -> set (whole), otherwise a splice on the previous value.
	streamed := func(field, ov, nv string) {
		switch {
		case nv == ov:
		case nv == "":
			unset = append(unset, field)
		case ov == "":
			set[field] = nv
		default:
			if dl, ok := livedoc.Diff(ov, nv); ok {
				patch[field] = dl
			} else {
				set[field] = nv
			}
		}
	}
	if n.Type != old.Type {
		set["type"] = string(n.Type)
	}
	scalar("role", old.Role, n.Role)
	scalar("name", old.Name, n.Name)
	scalar("summary", old.Summary, n.Summary)
	scalar("sender", old.Sender, n.Sender)
	scalar("status", old.Status, n.Status)
	scalar("tool_call_id", old.ToolCallID, n.ToolCallID)
	scalarInt("started_at", old.StartedAt, n.StartedAt)
	scalarInt("finished_at", old.FinishedAt, n.FinishedAt)
	scalarInt("at", old.At, n.At)
	streamed("markdown", old.Markdown, n.Markdown)
	streamed("output", old.Output, n.Output)
	if !reflect.DeepEqual(old.Args, n.Args) {
		if n.Args == nil {
			unset = append(unset, "args")
		} else {
			set["args"] = n.Args
		}
	}
	if !reflect.DeepEqual(old.LTs, n.LTs) && n.LTs != nil {
		set["lts"] = n.LTs
	}
	if !reflect.DeepEqual(old.Src, n.Src) && n.Src != nil {
		set["src"] = n.Src
	}
	if len(set) > 0 {
		d.Set = set
	}
	d.Unset = unset
	if len(patch) > 0 {
		d.Patch = patch
	}
	return d
}

// fullSet is the creation delta: every non-zero field in set.
func fullSet(id uint64, n livedoc.Node) NodeDelta {
	set := map[string]any{"type": string(n.Type)}
	str := func(k, v string) {
		if v != "" {
			set[k] = v
		}
	}
	str("role", n.Role)
	str("name", n.Name)
	str("summary", n.Summary)
	str("status", n.Status)
	str("tool_call_id", n.ToolCallID)
	str("markdown", n.Markdown)
	str("sender", n.Sender)
	str("output", n.Output)
	if n.Args != nil {
		set["args"] = n.Args
	}
	if n.StartedAt != 0 {
		set["started_at"] = n.StartedAt
	}
	if n.FinishedAt != 0 {
		set["finished_at"] = n.FinishedAt
	}
	if n.LTs != nil {
		set["lts"] = n.LTs
	}
	if n.Src != nil {
		set["src"] = n.Src
	}
	if n.At != 0 {
		set["at"] = n.At
	}
	return NodeDelta{ID: id, Set: set}
}

// OpenInquiry records the question that opened a turn and broadcasts it. The
// inquiry is turn metadata, not a node: an exchange begins with exactly one of
// them, so it is a property of the turn rather than an element of its list.
//
// It is the whole of the prompt's UI IR, which is why it broadcasts: a watching
// client must show the question the instant it commits, not when the agent's
// first token arrives.
func (s *Server) OpenInquiry(id uint64, inquiry string, segments ...InquirySegment) {
	s.mu.Lock()
	if n := len(s.turns); n == 0 || s.turns[n-1].ID != id {
		s.turns = append(s.turns, Turn{ID: id})
	}
	i := len(s.turns) - 1
	s.turns[i].Inquiry = inquiry
	s.turns[i].InquirySegments = segments
	from := uint64(len(s.turns[i].Nodes))
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, Page{Parts: []TurnPart{{
		Turn:        Turn{ID: id, Inquiry: inquiry, InquirySegments: segments},
		From:        from,
		ClippedHead: from > 0,
	}}})
}

// inquiryOfLocked is the recorded question for a turn — its text AND the
// segments naming who asked it — or the zero values if none. Caller holds s.mu.
//
// EVERY PART CARRIES ITS TURN'S QUESTION. It used to ride exactly one frame —
// the OpenInquiry broadcast — and every streaming frame afterwards described
// the same turn without it. A client that had not folded that single frame
// before nodes arrived had a turn with content and no question, and nothing
// later re-supplied it: only the seal carries the whole Turn, which is why the
// question appeared when the turn ENDED and not before.
//
// It returns the SEGMENTS for the same reason, and that half was missing:
// re-supplying the text alone made every streaming frame a part that named the
// question but not its askers, and the client holds what a part last said
// (heldInquiry) — so the attributed inquiry OpenInquiry had just broadcast was
// overwritten with an unattributed copy by the very next frame. `figaro show`
// re-derives segments from the IR and was right; the live surfaces were told
// the question came from nobody. A steer was unaffected: it is a node, and a
// node carries its own sender.
//
// A part is a description of a turn, so it states what the turn IS. The
// question is not a delta and cannot be reconstructed from one; leaving it off
// made a part that says "here are nodes for turn 7" without saying what turn 7
// asked, which is a hole the client cannot fill. Repeating it costs the
// prompt's bytes per frame over a unix socket, and buys the invariant that a
// part is never partial about identity.
func (s *Server) inquiryOfLocked(id uint64) (string, []InquirySegment) {
	for i := range s.turns {
		if s.turns[i].ID == id {
			return s.turns[i].Inquiry, s.turns[i].InquirySegments
		}
	}
	return "", nil
}
