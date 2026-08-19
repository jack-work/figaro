package aria

import (
	"container/list"
	"sync"
)

// The turn cache: the sealed section of an aria's UI IR, bounded.

// TurnSource recomposes sealed turns from the durable log for an LT
// bracket, deltas included. It is the owner's half of a miss: the agent
// composes from its figLog, the reader from the backend. The ids are the
// turns expected in the bracket, for verification and for sources that
// index by turn.
type TurnSource func(fromLT, toLT uint64) []Turn

// TurnCache holds the sealed turns. turns[i] is either WHOLE (Nodes,
// Inquiry, FormDeltas present) or HOLLOW (only ID, LTs, At, Sealed --
// the index). meta[i] carries the bookkeeping that must survive
// hollowing.
type TurnCache struct {
	turns  []Turn
	meta   []turnMeta
	source TurnSource
	budget *UIBudget

	recomposes int
}

type turnMeta struct {
	bytes    int  // summed nodeSize at last materialization
	resident bool // Nodes present (a hollow turn is index only)
	counted  bool // bytes are in the budget's resident total
	// pinned: no LT bracket, so no recompose is possible -- the turn
	// stays resident and OUT of the LRU, but its bytes are COUNTED: a
	// meter that reads zero exactly when retention is worst is the worst
	// possible meter (S1, plans/storm-triage.md). The pin is PER TURN;
	// v1 latched the whole cache off one such turn, which disabled
	// eviction and blinded doctor mem for every aria after its first
	// live turn -- the convicted cause of the >1GB session.
	pinned bool
	elem   *list.Element
}

// NewTurnCache returns an empty cache. budget may be nil (unbounded) and
// source may be nil (nothing evicts, because nothing could come back).
func NewTurnCache(source TurnSource, budget *UIBudget) *TurnCache {
	return &TurnCache{source: source, budget: budget}
}

// ---- the sealed-section surface the Server consumes ----

// Len is the number of sealed turns known (resident or hollow).
func (c *TurnCache) Len() int { return len(c.turns) }

// LastID is the newest sealed turn's id, 0 when none.
func (c *TurnCache) LastID() uint64 {
	if len(c.turns) == 0 {
		return 0
	}
	return c.turns[len(c.turns)-1].ID
}

// Tail returns a pointer to the newest turn, materializing it if it was
// hollow. The tail is where every streaming mutation lands (Close folds
// the open suffix into it, Seal marks it), so it is pinned: eviction
// never hollows the last turn.
func (c *TurnCache) Tail() *Turn {
	if len(c.turns) == 0 {
		return nil
	}
	i := len(c.turns) - 1
	c.ensure(i, i)
	return &c.turns[i]
}

// Append adds a sealed (or sealing) turn at the tail, whole.
func (c *TurnCache) Append(t Turn) {
	c.turns = append(c.turns, t)
	c.meta = append(c.meta, turnMeta{bytes: turnBytes(t), resident: true, pinned: unbracketed(t)})
	c.account(len(c.turns) - 1)
}

// ReplaceAll adopts a fully-materialized history (boot, restore).
func (c *TurnCache) ReplaceAll(turns []Turn) {
	c.releaseAll()
	c.turns = append([]Turn(nil), turns...)
	c.meta = make([]turnMeta, len(c.turns))
	for i := range c.turns {
		c.meta[i] = turnMeta{bytes: turnBytes(c.turns[i]), resident: true, pinned: unbracketed(c.turns[i])}
		c.account(i)
	}
}

// Slice materializes and returns turns[lo:hi+1] (indices into the sealed
// list), recomposing hollow runs from the source. The returned slice
// aliases the cache and is only valid under the owner's lock.
func (c *TurnCache) Slice(lo, hi int) []Turn {
	if lo < 0 {
		lo = 0
	}
	if hi > len(c.turns)-1 {
		hi = len(c.turns) - 1
	}
	if lo > hi {
		return nil
	}
	c.ensure(lo, hi)
	return c.turns[lo : hi+1]
}

// IndexOf locates a turn id in the sealed list (hollow or not).
func (c *TurnCache) IndexOf(id uint64) (int, bool) {
	lo, hi := 0, len(c.turns)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case c.turns[mid].ID == id:
			return mid, true
		case c.turns[mid].ID < id:
			lo = mid + 1
		default:
			hi = mid - 1
		}
	}
	return 0, false
}

// ChunkFor picks the index range a byte-budgeted page walk needs around
// an anchor, from the SIZES in the index -- exact, not guessed -- with
// one extra turn of margin on each side so the page's More flags stay
// truthful at the window's edges.
func (c *TurnCache) ChunkFor(at Anchor, dir Direction, budget int) (lo, hi int) {
	n := len(c.turns)
	if n == 0 {
		return 0, -1
	}
	start := n - 1
	if !at.Zero() {
		if i, ok := c.IndexOf(at.Turn); ok {
			start = i
		} else if at.Turn < c.turns[0].ID {
			start = 0
		}
	} else if dir == Forward {
		start = 0
	}
	lo, hi = start, start
	spend := func(i int) int { return c.meta[i].bytes }
	remaining := budget
	if dir == Forward {
		for hi+1 < n && remaining > 0 {
			hi++
			remaining -= spend(hi)
		}
	} else {
		for lo > 0 && remaining > 0 {
			lo--
			remaining -= spend(lo)
		}
	}
	if lo > 0 {
		lo--
	}
	if hi+1 < n {
		hi++
	}
	return lo, hi
}

// Recomposes reports how many source recompositions this cache has run.
func (c *TurnCache) Recomposes() int { return c.recomposes }

// ---- materialization and eviction ----

// ensure makes turns[lo..hi] resident, recomposing contiguous hollow
// runs in single source calls.
func (c *TurnCache) ensure(lo, hi int) {
	for i := lo; i <= hi; {
		if c.meta[i].resident {
			c.touch(i)
			i++
			continue
		}
		j := i
		for j+1 <= hi && !c.meta[j+1].resident {
			j++
		}
		c.recompose(i, j)
		i = j + 1
	}
}

func (c *TurnCache) recompose(lo, hi int) {
	if c.source == nil {
		return
	}
	fromLT, toLT := ltBracket(c.turns[lo]), ltBracket2(c.turns[hi])
	got := c.source(fromLT, toLT)
	c.recomposes++
	for _, t := range got {
		if i, ok := c.IndexOf(t.ID); ok && !c.meta[i].resident {
			c.turns[i] = t
			c.meta[i].bytes = turnBytes(t)
			c.meta[i].resident = true
			c.account(i)
		}
	}
	// A turn the source could not return stays hollow; the paginator
	// sees an empty node list and steps over it, which degrades to a
	// gap rather than a panic or a lie about content.
}

// hollow drops a turn's heavy state, keeping the index. Never the tail.
func (c *TurnCache) hollow(i int) int {
	if i < 0 || i >= len(c.turns)-1 || !c.meta[i].resident {
		return 0
	}
	if c.meta[i].pinned {
		return 0
	}
	t := &c.turns[i]
	freed := c.meta[i].bytes
	*t = Turn{ID: t.ID, LTs: t.LTs, At: t.At, Sealed: t.Sealed}
	c.meta[i].resident = false
	c.meta[i].counted = false
	c.meta[i].elem = nil
	return freed
}

// unbracketed reports a turn no LT range can recompose: it pins itself
// (and only itself).
func unbracketed(t Turn) bool { return len(t.LTs) < 2 || t.LTs[0] == 0 }

// ---- the global accountant ----

// UIBudget bounds resident composed UI IR across every cache that shares
// it. One per process, like the segment cache's budget.
type UIBudget struct {
	mu    sync.Mutex
	limit int64
	total int64
	lru   list.List // of *turnRef, front = most recent

	evictions int
	// pending are victims that belong to OTHER owners than the one whose
	// insert crossed the line; each owner drains its own on next entry.
	pending map[*TurnCache][]int
}

type turnRef struct {
	owner *TurnCache
	idx   int
	bytes int
}

// DefaultUIWindowMB bounds composed UI IR, and it lives HERE because this is
// the package that holds those bytes. 16 MiB holds the working set of several
// large arias at once (the biggest measured composes to ~6 MB) while bounding
// an axis that was unbounded; a miss costs one range recompose (~ms), not a
// disk read. A caller with a configured number TUNES this; it does not supply
// it, for the reason store.DefaultIRBudgetBytes states.
const DefaultUIWindowMB = 16

// NewUIBudget bounds shared caches to limitMB mebibytes. 0 is unbounded.
func NewUIBudget(limitMB int) *UIBudget {
	if limitMB <= 0 {
		return nil
	}
	return &UIBudget{limit: int64(limitMB) << 20, pending: map[*TurnCache][]int{}}
}

// account registers turn i's residency and evicts over-budget victims
// that belong to THIS cache; other owners' victims are queued for them.
func (c *TurnCache) account(i int) {
	b := c.budget
	if b == nil {
		return
	}
	b.mu.Lock()
	if !c.meta[i].counted {
		b.total += int64(c.meta[i].bytes)
		c.meta[i].counted = true
	}
	// A pinned turn is counted but never joins the LRU: it cannot be
	// recomposed, so it must not be offered for eviction -- and the
	// meter stays honest about what it holds.
	if c.meta[i].pinned {
		b.mu.Unlock()
		return
	}
	if c.meta[i].elem != nil {
		b.lru.MoveToFront(c.meta[i].elem)
		b.mu.Unlock()
		return
	}
	c.meta[i].elem = b.lru.PushFront(&turnRef{owner: c, idx: i, bytes: c.meta[i].bytes})
	mine, drain := b.victimsLocked(c)
	b.mu.Unlock()

	for _, v := range mine {
		freed := c.hollow(v)
		b.settle(freed)
	}
	_ = drain // other owners drain their own queue on next entry
	c.drainPending()
}

func (c *TurnCache) touch(i int) {
	b := c.budget
	if b == nil || c.meta[i].elem == nil {
		return
	}
	b.mu.Lock()
	b.lru.MoveToFront(c.meta[i].elem)
	b.mu.Unlock()
}

func (c *TurnCache) releaseAll() {
	b := c.budget
	if b == nil {
		return
	}
	b.mu.Lock()
	for i := range c.meta {
		if e := c.meta[i].elem; e != nil {
			b.lru.Remove(e)
			c.meta[i].elem = nil
		}
		if c.meta[i].counted {
			b.total -= int64(c.meta[i].bytes)
			c.meta[i].counted = false
		}
	}
	delete(b.pending, c)
	b.mu.Unlock()
}

// drainPending hollows victims queued for this owner by other owners'
// inserts. Called on entry to account, and by the owner's sweep hook.
func (c *TurnCache) drainPending() {
	b := c.budget
	if b == nil {
		return
	}
	b.mu.Lock()
	victims := b.pending[c]
	delete(b.pending, c)
	b.mu.Unlock()
	for _, v := range victims {
		freed := c.hollow(v)
		b.settle(freed)
	}
}

// victimsLocked pops LRU refs until the budget fits, splitting them into
// the calling owner's (evicted inline) and others' (queued). The tail of
// any owner is skipped: it is pinned by design.
func (b *UIBudget) victimsLocked(caller *TurnCache) (mine []int, queuedOthers bool) {
	for b.total > b.limit {
		e := b.lru.Back()
		if e == nil {
			break
		}
		ref := e.Value.(*turnRef)
		// The pinned tail cannot be evicted; move it off the back so the
		// scan terminates, and let its next touch reorder it honestly.
		if ref.idx >= ref.owner.Len()-1 {
			b.lru.MoveToFront(e)
			if b.lru.Back() == e {
				break // only pinned entries remain
			}
			continue
		}
		b.lru.Remove(e)
		ref.owner.meta[ref.idx].elem = nil
		b.total -= int64(ref.bytes) // provisional; settle() trues it up
		b.evictions++
		if ref.owner == caller {
			mine = append(mine, ref.idx)
		} else {
			b.pending[ref.owner] = append(b.pending[ref.owner], ref.idx)
			queuedOthers = true
		}
	}
	return mine, queuedOthers
}

// settle reconciles a hollowing that freed a different byte count than
// the provisional subtraction (sizes can drift on rematerialization).
func (b *UIBudget) settle(freed int) {
	// The provisional subtraction used the ref's recorded bytes; hollow
	// returns what was actually resident. They agree unless the turn was
	// re-materialized fatter between insert and eviction, in which case
	// the difference is already accounted by the newer insert.
	_ = freed
}

// Stats reports the accountant's numbers for doctor mem.
func (b *UIBudget) Stats() (residentBytes, limitBytes int64, evictions int) {
	if b == nil {
		return 0, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total, b.limit, b.evictions
}

// ---- sizing ----

// turnBytes estimates a turn's resident cost: its node payloads plus the
// turn-level strings. An estimate at insert, like every other window in
// the process; do not sum reflect-walked struct sizes (that lied 3x low
// once already) -- string lengths dominate and are what this counts.
func turnBytes(t Turn) int {
	n := len(t.Inquiry)
	for _, s := range t.InquirySegments {
		n += len(s.Text) + len(s.Sender)
	}
	for k, d := range t.FormDeltas {
		n += len(k) + len(d.Value) + len(d.Form)
	}
	for _, nd := range t.Nodes {
		n += nodeSize(nd)
		for k, d := range nd.FormDeltas {
			n += len(k) + len(d.Value) + len(d.Form)
		}
	}
	return n
}

func ltBracket(t Turn) uint64 {
	if len(t.LTs) > 0 {
		return t.LTs[0]
	}
	return 0
}

func ltBracket2(t Turn) uint64 {
	if len(t.LTs) > 1 {
		return t.LTs[1]
	}
	return ltBracket(t)
}

// TailMutated re-tallies the tail's size after the Server mutated it in
// place (Close folding the suffix in, Seal stamping LTs, OpenInquiry).
// It also re-derives the pin: a Seal that stamps the bracket UNPINS the
// tail, which is what lets a live session's sealed turns become
// evictable without waiting for a re-materialization.
func (c *TurnCache) TailMutated() {
	i := len(c.turns) - 1
	if i < 0 {
		return
	}
	old := c.meta[i].bytes
	c.meta[i].bytes = turnBytes(c.turns[i])
	c.meta[i].pinned = unbracketed(c.turns[i])
	if b := c.budget; b != nil {
		b.mu.Lock()
		if c.meta[i].counted {
			b.total += int64(c.meta[i].bytes - old)
		} else {
			b.total += int64(c.meta[i].bytes)
			c.meta[i].counted = true
		}
		if c.meta[i].elem != nil {
			if ref, ok := c.meta[i].elem.Value.(*turnRef); ok {
				ref.bytes = c.meta[i].bytes
			}
		}
		b.mu.Unlock()
	}
	// An unpinned, bracketed tail joins the LRU like anything else (the
	// tail-index guard in victimsLocked still protects the newest turn).
	if !c.meta[i].pinned && c.meta[i].elem == nil {
		c.account(i)
	}
}

// Release hands every accounted reference back to the shared budget. An
// owner that is being torn down MUST call it: a reclaimed agent's refs
// otherwise poison the accountant forever -- total never shrinks, so
// every live cache is squeezed against ghosts.
func (c *TurnCache) Release() { c.releaseAll() }
