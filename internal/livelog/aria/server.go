package aria

import (
	"reflect"
	"sync"

	"github.com/jack-work/figaro/api/livedoc"
)

// Server materializes one aria's turns and broadcasts changes as Pages.
type Server struct {
	mu     sync.Mutex
	cache  *TurnCache // the sealed section; mutated only under mu
	open   *openTurn
	subs   map[int]func(Page)
	nextID int

	// baseTurn/base remember where a turn's streaming region begins: the
	// producer recomposes the whole region every frame, so a second round
	// inside one turn must reopen at the same boundary, not after the first.
	baseTurn uint64
	base     uint64
}

// openTurn is the streaming suffix of the tail turn: the nodes at ids >= from.
type openTurn struct {
	id     uint64
	from   uint64
	prefix []livedoc.Node // settled: held by reference, never edited
	suffix []livedoc.Node
	ver    int // next frame version; last emitted is ver-1
}

func (o *openTurn) count() int { return len(o.prefix) + len(o.suffix) }

func (o *openTurn) at(i int) livedoc.Node {
	if i < len(o.prefix) {
		return o.prefix[i]
	}
	return o.suffix[i-len(o.prefix)]
}

func (o *openTurn) nodes() []livedoc.Node {
	out := make([]livedoc.Node, 0, o.count())
	out = append(out, o.prefix...)
	return append(out, o.suffix...)
}

// NewServer returns a server whose sealed section is unbounded until BindCache.
func NewServer() *Server {
	return &Server{subs: map[int]func(Page){}, cache: NewTurnCache(nil)}
}

// BindCache re-seats the sealed section on the shared composed cache. Call
// before history accumulates; an unbound server keeps every turn resident.
func (s *Server) BindCache(node string, shared *ComposedCache) {
	s.mu.Lock()
	s.cache.bind(node, shared)
	s.mu.Unlock()
}

// LastTurn is the newest known turn id, 0 if none.
func (s *Server) LastTurn() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache.LastID()
}

// Turns materializes the whole log; the paging path never uses it.
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
// reports whether it did; live state that appeared meanwhile is authoritative.
func (s *Server) AdoptIfEmpty(turns []Turn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache.Len() > 0 || s.open != nil || len(turns) == 0 {
		return false
	}
	s.cache.ReplaceAll(turns)
	return true
}

// OpenTurn begins a streaming suffix on turn id, creating the turn if needed.
// It emits no frame; the first Update carries v 0.
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
// deltas against the prior frame. Node ids are positional: from+i.
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
	// The question rides only the frames that establish the suffix: first and close.
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

// Close folds the streaming suffix into its turn; the turn stays open.
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

// StampTail hands the caller the newest turn under the lock, for fields only
// a durable walk knows. Call before Seal so the sealed turn goes out complete.
func (s *Server) StampTail(fill func(tail *Turn, prev []Turn)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tl := s.cache.Tail()
	if tl == nil || fill == nil {
		return
	}
	var prev []Turn
	if n := s.cache.Len(); n >= 2 {
		prev = s.cache.Slice(n-2, n-2)
	}
	fill(tl, prev)
}

// Seal marks the newest turn finished; every node in it is immutable from here.
func (s *Server) Seal(lts []uint64) {
	s.mu.Lock()
	tl := s.cache.Tail()
	if tl == nil {
		s.mu.Unlock()
		return
	}
	if tl.Sealed {
		s.mu.Unlock()
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

// Abandon drops the streaming suffix without folding it in. It broadcasts
// nothing; clients keep the partial until the next turn opens.
func (s *Server) Abandon() {
	s.mu.Lock()
	s.open = nil
	s.mu.Unlock()
}

// Commit appends an already-complete turn and broadcasts it.
func (s *Server) Commit(t Turn) {
	s.mu.Lock()
	s.cache.Append(t)
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, Page{Parts: []TurnPart{{Turn: t}}})
}

// Subscribe registers a pusher for subsequent frames; no initial snapshot.
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

// Read pages forward from at; ReadBefore pages backward. floor stops the
// backward walk from below, inclusive; the zero anchor is no floor.
func (s *Server) Read(at Anchor, budget int) Page {
	return s.page(at, Anchor{}, Forward, budget)
}

func (s *Server) ReadBefore(at, floor Anchor, budget int) Page {
	return s.page(at, floor, Backward, budget)
}

func (s *Server) page(at, floor Anchor, dir Direction, budget int) Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.cache.Len()
	if n == 0 && s.open == nil {
		return Page{}
	}
	lo, hi := s.cache.ChunkFor(at, dir, budget)
	// Turns under the floor are neither composed nor walked, and widening
	// stops there: a page the floor ended would re-cut unchanged forever.
	bottom := 0
	if dir == Backward && !floor.Zero() {
		if fi := s.cache.FloorIndex(floor); fi <= hi {
			bottom = fi
			if lo < bottom {
				lo = bottom
			}
		}
	}
	for {
		turns := s.overlayOpen(s.cache.Slice(lo, hi), hi >= n-1)
		var p Page
		if dir == Backward {
			p = PaginateBefore(turns, at, floor, budget)
		} else {
			p = Paginate(turns, at, dir, budget)
		}
		covered := lo == 0 && hi >= n-1
		if covered {
			return p
		}
		// A page ending at the window's edge may have been cut by the window
		// rather than its budget; widen and re-cut.
		touchLo := lo > bottom && len(p.Parts) > 0 && p.Parts[0].ID == turns[0].ID
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
			if lo < bottom {
				lo = bottom
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

// overlayOpen copies a slice (it aliases the cache) and, when it includes the
// tail, attaches the streaming suffix and its Live boundary.
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
	// streamed: cleared -> unset, first appearance -> set, otherwise a splice.
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
	scalar("sender_name", old.SenderName, n.SenderName)
	scalar("status", old.Status, n.Status)
	scalar("tool_call_id", old.ToolCallID, n.ToolCallID)
	scalarInt("opened_at", old.OpenedAt, n.OpenedAt)
	scalarInt("started_at", old.StartedAt, n.StartedAt)
	scalarInt("finished_at", old.FinishedAt, n.FinishedAt)
	scalarInt("at", old.At, n.At)
	scalarInt("output_base", int64(old.OutputBase), int64(n.OutputBase))
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
	str("sender_name", n.SenderName)
	str("output", n.Output)
	str("input", n.Input)
	if n.Args != nil {
		set["args"] = n.Args
	}
	if n.OutputBase != 0 {
		set["output_base"] = n.OutputBase
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

// OpenInquiry records the question that opened a turn and broadcasts it with
// the form state the turn enters with. The inquiry is turn metadata, not a node.
func (s *Server) OpenInquiry(id uint64, inquiry string, deltas map[string]livedoc.FormDelta, segments ...InquirySegment) {
	s.mu.Lock()
	if s.cache.Len() == 0 || s.cache.LastID() != id {
		s.cache.Append(Turn{ID: id})
	}
	tl := s.cache.Tail()
	tl.Inquiry = inquiry
	tl.InquirySegments = segments
	if len(deltas) > 0 {
		tl.FormDeltas = deltas
	}
	s.cache.TailMutated()
	from := uint64(len(tl.Nodes))
	subs := s.subsLocked()
	s.mu.Unlock()
	deliver(subs, Page{Parts: []TurnPart{{
		Turn:        Turn{ID: id, Inquiry: inquiry, InquirySegments: segments, FormDeltas: deltas},
		From:        from,
		ClippedHead: from > 0,
	}}})
}

// TailNodeLT is where the next turn's form-state window opens; zero when the
// newest turn drew nothing and carries no bracket.
func (s *Server) TailNodeLT() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	tl := s.cache.Tail()
	if tl == nil {
		return 0
	}
	return PriorBoundaryOf(*tl)
}

// PriorBoundaryOf is a turn's last node record, or its LT bracket end when it
// drew nothing; without the fallback the next window is the whole history.
func PriorBoundaryOf(t Turn) uint64 {
	var from uint64
	for _, n := range t.Nodes {
		for _, src := range n.Src {
			from = max(from, src.LT)
		}
	}
	if from == 0 && len(t.LTs) > 0 {
		from = t.LTs[len(t.LTs)-1]
	}
	return from
}

// inquiryOfLocked answers only for the tail: materializing history for a
// question only the tail can carry would defeat the window. Caller holds s.mu.
func (s *Server) inquiryOfLocked(id uint64) (string, []InquirySegment) {
	if tl := s.cache.Tail(); tl != nil && tl.ID == id {
		return tl.Inquiry, tl.InquirySegments
	}
	return "", nil
}

// ReleaseCache is teardown only: the key list stays, the payloads are gone
// and nothing will fault them back.
func (s *Server) ReleaseCache() {
	s.mu.Lock()
	s.cache.Release()
	s.mu.Unlock()
}

// TailBracket is the newest turn's LT bracket end (0 when none or unbracketed),
// without materializing anything.
func (s *Server) TailBracket() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	tl := s.cache.Tail()
	if tl == nil || len(tl.LTs) < 2 {
		return 0
	}
	return tl.LTs[1]
}
