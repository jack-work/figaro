package aria

import (
	"reflect"
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Server materializes one aria's turns and broadcasts changes as Pages.
type Server struct {
	mu sync.Mutex
	// cache is the sealed section: bounded, index-preserving, recompose-
	// on-miss (turncache.go). The Server owns it and mutates it only
	// under s.mu; the streaming half below is the part no cache can be.
	cache  *TurnCache
	open   *openTurn
	subs   map[int]func(Page)
	nextID int

	// baseTurn/base remember where a turn's streaming region begins. The
	// producer recomposes the WHOLE region on every frame (it reads the turn's
	// messages back from the log), so a second round inside one turn must
	// reopen at the same boundary rather than after the first round: else the
	// recomposed nodes would be appended again instead of replacing.
	baseTurn uint64
	base     uint64
}

// openTurn is the streaming suffix of turns[len-1]: the nodes at ids
// >= from, materialized so Update can diff against the previous frame.
type openTurn struct {
	id     uint64
	from   uint64
	prefix []livedoc.Node // settled: held by reference, never edited
	suffix []livedoc.Node // still moving
	ver    int            // next frame version (0-indexed); last emitted is ver-1
}

func (o *openTurn) count() int { return len(o.prefix) + len(o.suffix) }

func (o *openTurn) at(i int) livedoc.Node {
	if i < len(o.prefix) {
		return o.prefix[i]
	}
	return o.suffix[i-len(o.prefix)]
}

// nodes materializes the frame. Callers that need one slice pay for it at
// close or at a read, not once per frame.
func (o *openTurn) nodes() []livedoc.Node {
	out := make([]livedoc.Node, 0, o.count())
	out = append(out, o.prefix...)
	return append(out, o.suffix...)
}

// NewServer returns an empty aria server, its sealed section unbounded
// until BindCache hands it a source and a budget.
func NewServer() *Server {
	return &Server{subs: map[int]func(Page){}, cache: NewTurnCache(nil, nil)}
}

// BindCache arms the sealed section with a recompose source and a shared
// byte budget. Call before history accumulates; a server never bound
// keeps every sealed turn resident, which is the old behaviour.
func (s *Server) BindCache(source TurnSource, budget *UIBudget) {
	s.mu.Lock()
	s.cache.source = source
	s.cache.budget = budget
	s.mu.Unlock()
}

// LastTurn returns the id of the newest known turn (0 if none): the cursor a
// client streams from.
func (s *Server) LastTurn() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache.LastID()
}

// Turns returns a snapshot of every materialized turn, the last carrying
// its open suffix and Live boundary when one is streaming. It
// MATERIALIZES THE WHOLE LOG and exists for tests and whole-history
// callers; the paging path (Read/ReadBefore) never uses it.
func (s *Server) Turns() []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overlayOpen(s.cache.Slice(0, s.cache.Len()-1), true)
}

func (s *Server) HasOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open != nil
}

// Restore replaces materialized state without broadcasting.
func (s *Server) Restore(turns []Turn) {
	s.mu.Lock()
	s.cache.ReplaceAll(turns)
	s.open = nil
	s.mu.Unlock()
}

// AdoptIfEmpty seeds a server that has never materialized anything, and
// reports whether it did. s.turns fills as turns seal, so an aria this process
// has not run a turn for, a dormant one, just attached: holds nothing and
// would serve an empty page. The caller composes from the durable log outside
// this lock (it is not cheap) and offers the result here; if a turn opened in
// the meantime the live state is already authoritative and the offer is
// declined.
func (s *Server) AdoptIfEmpty(turns []Turn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache.Len() > 0 || s.open != nil || len(turns) == 0 {
		return false
	}
	s.cache.ReplaceAll(turns)
	return true
}

// OpenTurn begins a streaming suffix on turn id, creating the turn if this is
// its first message. The boundary is wherever the turn's committed nodes
// currently end, so a multi-round turn reopens further along each time rather
// than restarting. It emits no frame; the first Update carries v 0.
func (s *Server) OpenTurn(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache.Len() == 0 || s.cache.LastID() != id {
		s.cache.Append(Turn{ID: id})
	}
	if s.baseTurn != id {
		s.baseTurn = id
		s.base = uint64(len(s.cache.Tail().Nodes))
	}
	s.open = &openTurn{id: id, from: s.base}
}

// Update applies the suffix's new full node list and broadcasts the field
// deltas against the prior frame (v++ if anything changed). Node ids are
// positional: the i'th suffix node is id from+i: so identity needs no
// separate key.
func (s *Server) Update(prefix, suffix []livedoc.Node, stable int) {
	s.mu.Lock()
	if s.open == nil {
		s.mu.Unlock()
		return
	}
	total := len(prefix) + len(suffix)
	if stable > s.open.count() {
		stable = s.open.count()
	}
	if stable > total {
		stable = total
	}
	if stable < 0 {
		stable = 0
	}
	var deltas []NodeDelta
	prior := s.open.count()
	for i := stable; i < total; i++ {
		var n livedoc.Node
		if i < len(prefix) {
			n = prefix[i]
		} else {
			n = suffix[i-len(prefix)]
		}
		id := s.open.from + uint64(i)
		if i < prior {
			if d := delta(id, s.open.at(i), n); !d.Empty() {
				deltas = append(deltas, d)
			}
			continue
		}
		deltas = append(deltas, fullSet(id, n))
	}
	s.open.prefix, s.open.suffix = prefix, suffix
	if len(deltas) == 0 {
		s.mu.Unlock()
		return
	}
	v := s.open.ver
	s.open.ver++
	// The question rides the frames that ESTABLISH this streaming suffix: its
	// first, and its close: and no others. See inquiryOfLocked.
	inquiry, segments := "", []InquirySegment(nil)
	if v == 0 {
		inquiry, segments = s.inquiryOfLocked(s.open.id)
	}
	frame := Page{Parts: []TurnPart{{
		Turn: Turn{ID: s.open.id, Inquiry: inquiry, InquirySegments: segments,
			Live: &Live{From: s.open.from, V: v, Nodes: deltas}},
		From: s.open.from,
	}}}
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, frame)
}

// Close folds the streaming suffix into its turn. The turn itself stays open -
// a turn spans many messages: so nothing is sealed here; Seal does that.
func (s *Server) Close() {
	s.mu.Lock()
	if s.open == nil {
		s.mu.Unlock()
		return
	}
	tl := s.cache.Tail()
	tl.Nodes = append(tl.Nodes[:s.open.from:s.open.from], s.open.nodes()...)
	s.cache.TailMutated()
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
// is immutable from here. This is the one moment the word means, and, later,
// the moment the turn is written to its xwal channel.
func (s *Server) Seal(lts []uint64) {
	s.mu.Lock()
	tl := s.cache.Tail()
	if tl == nil {
		s.mu.Unlock()
		return
	}
	if tl.Sealed {
		s.mu.Unlock() // already sealed: sealing is idempotent and silent
		return
	}
	tl.Sealed = true
	if len(lts) > 0 {
		tl.LTs = lts
	}
	s.cache.TailMutated()
	sealed := *tl
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
	s.cache.Append(t)
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
// cut, differing only in which side of the anchor the budget is spent on -
// that is what lets a scrolling client pull an earlier or a later page from
// wherever it happens to be.
func (s *Server) Read(at Anchor, budget int) Page {
	return s.page(at, Forward, budget)
}

func (s *Server) ReadBefore(at Anchor, budget int) Page {
	return s.page(at, Backward, budget)
}

func (s *Server) page(at Anchor, dir Direction, budget int) Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.cache.Len()
	if n == 0 && s.open == nil {
		return Page{}
	}
	lo, hi := s.cache.ChunkFor(at, dir, budget)
	for {
		turns := s.overlayOpen(s.cache.Slice(lo, hi), hi >= n-1)
		var p Page
		if dir == Backward {
			p = PaginateBefore(turns, at, budget)
		} else {
			p = Paginate(turns, at, dir, budget)
		}
		covered := lo == 0 && hi >= n-1
		if covered {
			return p
		}
		// A page ending AT the window's edge may have been cut by the
		// window rather than by its budget; widen and re-cut. A page
		// strictly inside the window was cut by budget alone and is
		// exactly what the full walk would have produced.
		touchLo := lo > 0 && len(p.Parts) > 0 && p.Parts[0].ID == turns[0].ID
		touchHi := hi < n-1 && len(p.Parts) > 0 && p.Parts[len(p.Parts)-1].ID == turns[len(turns)-1].ID
		if !touchLo && !touchHi {
			if lo > 0 {
				p.More.Before = true
			}
			if hi < n-1 {
				p.More.After = true
			}
			return p
		}
		span := hi - lo + 1
		if touchLo {
			lo -= span
			if lo < 0 {
				lo = 0
			}
		}
		if touchHi {
			hi += span
			if hi > n-1 {
				hi = n - 1
			}
		}
	}
}

// overlayOpen copies a materialized slice and, when it includes the tail,
// attaches the streaming suffix and its Live boundary to the turn that
// owns it. The copy matters: the slice aliases the cache, and the overlay
// must not write through it.
func (s *Server) overlayOpen(window []Turn, includesTail bool) []Turn {
	out := append([]Turn(nil), window...)
	if s.open == nil || !includesTail {
		return out
	}
	i := len(out) - 1
	if i < 0 || out[i].ID != s.open.id {
		out = append(out, Turn{ID: s.open.id})
		i = len(out) - 1
	}
	t := out[i]
	t.Nodes = append(append([]livedoc.Node(nil), t.Nodes[:s.open.from]...), s.open.nodes()...)
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
	scalarInt("opened_at", old.OpenedAt, n.OpenedAt)
	scalarInt("started_at", old.StartedAt, n.StartedAt)
	scalarInt("finished_at", old.FinishedAt, n.FinishedAt)
	scalarInt("at", old.At, n.At)
	streamed("markdown", old.Markdown, n.Markdown)
	streamed("output", old.Output, n.Output)
	streamed("input", old.Input, n.Input)
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
	str("input", n.Input)
	if n.Args != nil {
		set["args"] = n.Args
	}
	if n.OpenedAt != 0 {
		set["opened_at"] = n.OpenedAt
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
	if n.FormDeltas != nil {
		set["formDeltas"] = n.FormDeltas
	}
	return NodeDelta{ID: id, Set: set}
}

// OpenInquiry records the question that opened a turn and broadcasts it. The
// inquiry is turn metadata, not a node: an exchange begins with exactly one of
// them, so it is a property of the turn rather than an element of its list.
func (s *Server) OpenInquiry(id uint64, inquiry string, segments ...InquirySegment) {
	s.mu.Lock()
	if s.cache.Len() == 0 || s.cache.LastID() != id {
		s.cache.Append(Turn{ID: id})
	}
	tl := s.cache.Tail()
	tl.Inquiry = inquiry
	tl.InquirySegments = segments
	s.cache.TailMutated()
	from := uint64(len(tl.Nodes))
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, Page{Parts: []TurnPart{{
		Turn:        Turn{ID: id, Inquiry: inquiry, InquirySegments: segments},
		From:        from,
		ClippedHead: from > 0,
	}}})
}

// inquiryOfLocked is the recorded question for a turn: its text AND the
// segments naming who asked it: or the zero values if none. Caller holds s.mu.
func (s *Server) inquiryOfLocked(id uint64) (string, []InquirySegment) {
	// The id is the OPEN turn's, which is the tail or nothing: a frame
	// narrates the turn in flight. Materializing history to answer it
	// would defeat the window for a question only the tail can carry.
	if tl := s.cache.Tail(); tl != nil && tl.ID == id {
		return tl.Inquiry, tl.InquirySegments
	}
	return "", nil
}

// ReleaseCache returns the sealed section's budget references. Call on
// teardown; the server remains usable but unaccounted afterwards.
func (s *Server) ReleaseCache() {
	s.mu.Lock()
	s.cache.Release()
	s.mu.Unlock()
}

// TailBracket is the newest sealed turn's LT bracket end (0 when none or
// unbracketed). The tail is pinned resident, so this costs no
// materialization -- it exists so a staleness probe does not have to
// call Turns(), which would.
func (s *Server) TailBracket() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	tl := s.cache.Tail()
	if tl == nil || len(tl.LTs) < 2 {
		return 0
	}
	return tl.LTs[1]
}
