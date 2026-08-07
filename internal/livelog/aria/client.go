package aria

import (
	"context"
	"sync"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Client folds Pages into a local range-backed view. Live frames are folded
// into materialized livedoc.Node instances by positional ordinal; a suffix
// close marker is accepted only when the seen record version matches. Any
// mismatch fires OnDesync with the highest fully sealed turn so the caller can
// re-read.
//
// OnClosed fires when a message finalizes; OnLive fires with the open message
// (its suffix nodes, and the turn's inquiry while the suffix starts the turn);
// OnDesync requests a catch-up from the given sealed-turn cursor.
// Since the range store landed (docs/range-store.md, phase 1) the retained
// closed set is NOT a list: it is a set of contiguous intervals over (turn,
// node) space, held by Store. Client is the shim that preserves the old API —
// View/Open/OnClosed/OnLive — over the new substrate. Nothing outside this
// package changed, which is what makes the swap reviewable and revertable.
type Client struct {
	mu sync.Mutex

	store      *Store
	closedSeen map[int]bool
	// closedFrom is the LOWEST offset finalized for a closed turn — the floor a
	// later, earlier page can still fill in beneath. Without it a turn first met
	// by its tail could never be completed: it was marked seen and every later
	// part for it was skipped whole, dropping the head nodes AND the question.
	closedFrom  map[int]int
	closedFloor int
	closedLimit int
	closedRev   uint64
	// Highest fully sealed turn; the field name predates turn addressing.
	lastCommittedLT int

	// The open turn, materialized, lives in the store (Store.openTail): it
	// holds the TURN ID (see Message), the suffix boundary reported by
	// Live.From — nodes below it are closed and will never be touched again —
	// the record version, and the node buffer.

	// emitted[turn] is how many of a turn's nodes have already gone out as
	// closed messages, and inquiry[turn] is that turn's opening question until
	// its first slice carries it away. The inquiry commits before the agent has
	// said anything, so it must be held here to be attached to whichever slice
	// turns out to start the turn.
	emitted map[int]int
	// inquiry holds a turn's opening question until a slice carries it away.
	// Text and segments travel in ONE map: a second map keyed the same way
	// cost a whole allocation and its growth per client, and measured as a
	// ~19% B/op regression on the fold path for a field most turns leave nil.
	inquiry map[int]heldInquiry

	OnClosed  func(Message)
	OnLive    func(Message)
	OnDesync  func(sinceLT int)
	OnMetrics func(Metrics)
}

// heldInquiry is a turn's question and its per-sender split, parked together
// until a slice starts the turn. One value in one map rather than two maps
// keyed alike: the second map's allocation and growth showed up as a B/op
// regression on the fold path, for a field most turns leave nil.
type heldInquiry struct {
	text     string
	segments []InquirySegment
}

// NewClient returns a fresh client.
func NewClient() *Client {
	return &Client{store: NewStore(), closedSeen: map[int]bool{}, closedFrom: map[int]int{}, emitted: map[int]int{}, inquiry: map[int]heldInquiry{}}
}

// Store exposes the range store beneath the client. Phase 1 has no consumer:
// it is here so tests can assert the invariants the shim is built on. It is
// NOT safe to use concurrently with Apply — the client's mutex guards the
// store, and this hands out the guarded object.
func (c *Client) Store() *Store {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store
}

// Merge folds messages a caller fetched ITSELF — the pager's backward read,
// incipit's catch-up page — into the store, WITHOUT firing OnClosed or OnLive.
//
// That silence is the point. A page applied through Apply comes back out
// through OnClosed, whose inline branch freezes to native scrollback, so
// history fetched merely to orient a reader would be dumped into their
// terminal. The old answer was to keep such a page out of the client entirely
// and have the pager hold a SECOND copy of it (transcript.seed, merged into
// the tail window on every rebuild); with one owner there is nowhere else to
// put it, and no reason to want one.
//
// extents is turn -> how many anchors that turn occupies, for the parts the
// server did not clip at the tail. It is what lets the store decide that
// (t, last) and (t+1, 0) are neighbours rather than a hole, and it is applied
// BEFORE the insert so the run coalesces on the way in.
func (c *Client) Merge(msgs []Message, extents map[int]uint64) {
	if len(msgs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for turn, n := range extents {
		c.store.SetTurnLen(uint64(turn), n)
	}
	c.store.Insert(msgs...)
	c.closedRev++
}

// Query reports what the store HOLDS over [from, to]; it never fetches. The
// returned Segment.Msgs ALIAS the store and are valid only until the next
// Apply/Merge/Evict — the caller is a renderer, which runs under the same lock
// discipline as the fold and never keeps a segment across one.
func (c *Client) Query(from, to Anchor) []Segment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Query(from, to)
}

// ForEachIn walks the retained messages inside [from, to] under the client's
// lock — the renderer's read path, which must not allocate a segment list per
// frame (see Store.ForEachIn).
func (c *Client) ForEachIn(from, to Anchor, fn func(Message) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.ForEachIn(from, to, fn)
}

// SetFetcher installs the reader Ensure fills holes with (see Store.Ensure).
// Wired by whoever owns the RPC client, once, before the pager can ask.
func (c *Client) SetFetcher(f Fetcher) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.SetFetcher(f)
}

// Ensure fills every hole in [from, to] so that Query over the same interval
// then returns exactly one Segment with a nil Gap.
//
// IT IS THE SAME LOOP AS Store.Ensure WITH THE FETCH OUTSIDE THE LOCK. That is
// the whole reason it exists here: a fill is a five-second-timeout read, and
// holding the client's mutex across it would freeze every frame the renderer
// wanted to draw while it ran. The lock is taken to ask what is missing and
// taken again to fold what came back; in between, the pager keeps painting the
// gap row.
func (c *Client) Ensure(ctx context.Context, from, to Anchor) error {
	for range ensureRounds {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		gap, ok := c.store.firstGap(from, to)
		fetch, before := c.store.Fetcher(), c.store.Count()
		c.mu.Unlock()
		if !ok {
			return nil
		}
		if fetch == nil {
			return ErrNoFetcher
		}
		got, err := fetch(ctx, fillAt(gap), fillLimit)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.store.Absorbed(got)
		grew := c.store.Count() != before
		if grew {
			c.closedRev++
		}
		c.mu.Unlock()
		if !grew {
			return ErrStalled
		}
	}
	return ErrStalled
}

// ForEachSegment walks [from, to] as runs and holes, under the client's lock
// (see Store.ForEachSegment). This is the renderer's GAP-AWARE read path: the
// pager draws one sentinel row per hole, so it is the one consumer that must
// see them.
func (c *Client) ForEachSegment(from, to Anchor, msg func(Message) bool, gap func(Gap) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.ForEachSegment(from, to, msg, gap)
}

// Count is how many closed messages the store retains.
func (c *Client) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Count()
}

// TailFrom is the anchor of the n-th message from the end (see Store.TailFrom).
func (c *Client) TailFrom(n int) (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.TailFrom(n)
}

// Skip is the anchor of the n-th message at or after a (see Store.Skip).
func (c *Client) Skip(a Anchor, n int) (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Skip(a, n)
}

// Before is the anchor n messages before a, and how far it got (see
// Store.Before) — a windowed reader lowering its floor over history the store
// already holds.
func (c *Client) Before(a Anchor, n int) (Anchor, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Before(a, n)
}

// EvictBefore forgets everything below a. Eviction and never-fetched are the
// same state, so what this costs is a possible re-read if the reader turns
// around — exactly what it costs to have never held it.
func (c *Client) EvictBefore(a Anchor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a == (Anchor{}) || c.store.Count() == 0 {
		return
	}
	before := c.store.Count()
	c.store.Evict(Anchor{}, a.Prev())
	if c.store.Count() != before {
		c.closedRev++
	}
}

// SetMoreBefore records whether anything precedes the oldest retained message.
// Only the wire knows: a backward read reports it (Page.More.Before), and an
// empty backward read proves the floor. A PUSHED frame does not — its More
// describes the delta window, not the conversation — which is why this is set
// by whoever performed the read rather than folded in Apply.
func (c *Client) SetMoreBefore(more bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.store.More()
	m.Before = more
	c.store.SetMore(m)
}

// MoreBefore reports whether older history is believed to exist.
func (c *Client) MoreBefore() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.More().Before
}

// SetClosedLimit bounds retained closed messages. Zero keeps the default,
// unbounded behavior.
func (c *Client) SetClosedLimit(limit int) {
	c.mu.Lock()
	c.closedLimit = limit
	c.trimClosed()
	c.mu.Unlock()
}

// Cursor is the highest fully sealed turn — the resume point for a re-read.
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
	return c.store.OpenAnimating()
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
		// THE OPEN-TURN SLOTS ARE ONE BUFFER, AND ONLY ONE TURN MAY HOLD THEM.
		// Decided once, here, rather than re-derived by each branch: three branches
		// disagreeing about who owns the buffer is what let a page of history claim
		// the slots and then destroy the live turn on its way out. See claimsOpen.
		staged := c.claimsOpen(part)

		// The inquiry is TEXT ON THE TURN, not a node, and it commits before the
		// agent has said anything — so it arrives on a part of its own, with no
		// nodes and no Live. Hold it until the head slice carries it away, and open
		// the turn now so the question paints the instant it is asked.
		if part.Inquiry != "" {
			// RECORDING is bookkeeping and always safe — including on a
			// clipped-head part, which is what a backward page into history is
			// made of. It used to sit inside the ClippedHead guard, against its
			// own comment, so paging up through an old aria never learned any of
			// its questions and the head slice, when it arrived, drew none.
			c.inquiry[id] = heldInquiry{text: part.Inquiry, segments: part.InquirySegments}
			// OPENING is what history is refused: a part whose head was clipped
			// describes a turn we hold only the tail of, and claiming the open
			// slot for it would destroy the live turn on its way past.
			if staged && !part.ClippedHead {
				c.store.ClaimOpen(id)
			}
		}

		// Adopt any nodes the part carries. From is the positional id of
		// Nodes[0], so a clipped part slots into place rather than replacing.
		//
		// STAGED parts only. A sealed part carries its own run at its own offset
		// and is released directly below — it never needs the buffer.
		if len(part.Nodes) > 0 && staged {
			c.store.ClaimOpen(id)
			// A part CLIPPED off the head of a turn is ALL we hold of it: the
			// nodes below From were never delivered, so they are not ours to
			// release. Floor the emit cursor at From, or absorb's padding slots
			// go out as a slice starting at node 0 — a HEAD slice, which the
			// renderers draw the question above, except that a clipped part
			// carries no question. That is how a turn too big for one page lost
			// its inquiry in every surface at once.
			if n := int(part.From); n > c.emitted[id] && n > c.store.OpenLen() {
				c.emitted[id] = n
			}
			c.store.Absorb(part.From, part.Nodes)
		}

		if part.Live != nil && staged {
			c.store.ClaimOpen(id)
			c.store.SetLiveFrom(part.Live.From)
			for _, nd := range part.Live.Nodes {
				c.store.FoldAt(nd)
			}
			// Everything below Live.From is closed for good. Release it now so
			// the head of a long turn reaches scrollback instead of riding the
			// live region until the turn seals.
			if n := int(c.store.LiveFrom()); n > c.emitted[id] && n <= c.store.OpenLen() {
				finalized = append(finalized, c.message(id, c.emitted[id], c.store.OpenSlice(c.emitted[id], n)))
				c.emitted[id] = n
			}
			if len(part.Live.Nodes) > 0 {
				c.store.SetOpenV(part.Live.V)
			} else if c.store.OpenV() != part.Live.V && len(part.Nodes) == 0 {
				// A close marker whose version we never reached: we missed
				// frames, so ask for a catch-up rather than show a gap.
				desync = c.lastCommittedLT
			}
		}

		if !part.Sealed {
			continue
		}
		if c.seenClosed(id) {
			// A turn already seen closed can still be COMPLETED DOWNWARD. A
			// backward page delivers the tail of the oldest turn it reaches, so
			// the page after it carries that turn's head — and skipping it
			// wholesale dropped the opening nodes, and the question with them.
			// Only what lies below the floor is adopted; the rest we already hold.
			if floor, ok := c.closedFrom[id]; ok && len(part.Nodes) > 0 && int(part.From) < floor {
				n := min(floor-int(part.From), len(part.Nodes))
				finalized = append(finalized, c.message(id, int(part.From),
					append([]livedoc.Node(nil), part.Nodes[:n]...)))
				c.closedFrom[id] = int(part.From)
			}
			c.advanceCommitted(id)
			continue
		}
		c.closedSeen[id] = true
		if staged {
			// The turn we were holding open has sealed. Release what we STREAMED,
			// not what the part restates: a seal often arrives as a bare marker,
			// and the buffer is then the fuller record.
			nodes := part.Nodes
			if c.store.OpenLen() >= len(nodes) {
				nodes = c.store.OpenNodes()
			}
			// Everything not already released closes as one message. A turn that
			// produced nothing at all (interrupted before its first block) still
			// closes one, carrying the inquiry — otherwise the question the user
			// asked would never reach scrollback.
			if s := c.emitted[id]; s < len(nodes) {
				finalized = append(finalized, c.message(id, s, nodes[s:]))
				c.store.SetTurnLen(uint64(id), uint64(len(nodes)))
			} else if s == 0 && c.inquiry[id].text != "" {
				finalized = append(finalized, c.message(id, 0, nil))
				c.store.SetTurnLen(uint64(id), 1)
			} else if len(nodes) > 0 {
				c.store.SetTurnLen(uint64(id), uint64(len(nodes)))
			}
		} else {
			// HISTORY. THE PART IS THE RECORD, and part.From is where it belongs.
			// This is the answer for a clipped sealed slice too: the buffer only
			// ever existed to give absorb() somewhere to pad to, and padding is
			// how a clipped slice used to be reconstructed. Reading the offset off
			// the part instead makes the padding unnecessary — message() attaches
			// the inquiry only at offset 0, which is exactly the slice entitled to
			// draw the question.
			if len(part.Nodes) > 0 {
				finalized = append(finalized, c.message(id, int(part.From),
					append([]livedoc.Node(nil), part.Nodes...)))
				// A part that is not clipped at the tail states the turn's
				// extent, and the extent is what lets the store decide that
				// (t, last) and (t+1, 0) are neighbours rather than a hole.
				if !part.ClippedTail {
					c.store.SetTurnLen(uint64(id), part.From+uint64(len(part.Nodes)))
				}
			} else if c.inquiry[id].text != "" {
				finalized = append(finalized, c.message(id, 0, nil))
				if !part.ClippedTail {
					c.store.SetTurnLen(uint64(id), 1)
				}
			}
		}
		delete(c.emitted, id)
		c.noteClosedFrom(id, finalized)
		// The held question is spent only once the HEAD slice has carried it away.
		// A part clipped at the head is not that slice: the page holding the head
		// arrives later, and discarding here is how a turn met by scrolling up
		// lost its question for good.
		if !part.ClippedHead {
			delete(c.inquiry, id)
		}
		c.advanceCommitted(id)
		if c.store.OpenTurn() == id {
			c.store.ResetOpen()
		}
	}

	if len(finalized) > 0 {
		c.store.Insert(finalized...)
		c.closedRev++
	}
	c.trimClosed()

	haveLive := c.store.OpenTurn() != 0
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

// claimsOpen reports whether a part is entitled to the OPEN-TURN SLOTS —
// openTurn, openFrom, openV and openNodesSlice.
//
// Those four are a single staging buffer, and exactly one turn may hold them at
// a time. Apply used to let every branch decide for itself, which meant a page
// of sealed HISTORY could claim them (the inquiry branch and the nodes branch
// both did `if c.openTurn != id { resetOpen(); openTurn = id }`) and then the
// sealed branch, seeing openTurn == id, called resetOpen() and threw the LIVE
// turn away.
//
// That is the ^T bug: submitting to an existing, not-running aria paints the
// question in incipit, and entering the pager does a catch-up ReadBefore whose
// page is all sealed history — which silently closed the turn the question
// belonged to. It needed prior history to show, because seenClosed() is false
// for turns this process has never seen, and it healed itself the moment the
// model's first node re-opened the turn.
//
// The rule, in order:
//
//   - the turn we ALREADY hold is still ours, sealed or not: that is how a
//     streaming turn folds its deltas and then closes from what we buffered.
//   - a turn OLDER than the open one never displaces it. Newer output cannot be
//     invalidated by older, and a backward read is full of older.
//   - a part carrying Live is live BY THE SERVER'S OWN STATEMENT. This is what
//     lets a catch-up read join a turn already in flight.
//   - a SEALED part is history. It carries its whole run at a known offset and
//     is released directly; it has nothing to stage.
//   - a turn already finalized is never re-opened.
//
// Anything left is an unsealed, unseen, not-older turn — the inquiry push that
// opens a turn before the model has said a word.
func (c *Client) claimsOpen(part TurnPart) bool {
	id := int(part.ID)
	switch {
	case c.store.OpenTurn() == id:
		return true
	case c.store.OpenTurn() != 0 && id < c.store.OpenTurn():
		return false
	case part.Live != nil:
		return true
	case part.Sealed:
		return false
	case c.seenClosed(id):
		return false
	}
	return true
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

// message builds one slice of a turn. THE SLICE THAT STARTS THE TURN CARRIES
// THE QUESTION, and no other does: a turn is an inquiry followed by the answer
// to it, and a question drawn above node 64 of 130 says an exchange began where
// the reader is merely standing.
//
// The rule is `from == 0` and it holds for the live suffix too. That matters:
// once a long turn releases its head to scrollback the open region sits at
// from>0, so a rule that let a later slice speak made every long turn re-ask
// its own question halfway down, in the pager AND inline.
//
// A reader who holds only the tail of a turn therefore sees no question. That
// is the honest state — the head is not here — and it is temporary: paging up
// delivers the head, and the question comes with it
// (TestScrollingBackCompletesATurnsHead).
func (c *Client) message(turn, from int, nodes []livedoc.Node) Message {
	m := Message{Turn: turn, From: uint64(from), Role: turnRole(nodes), Nodes: nodes}
	if from == 0 {
		held := c.inquiry[turn]
		m.Inquiry, m.InquirySegments = held.text, held.segments
	}
	return m
}

// openMessage is the open turn's suffix as a message. Caller holds the lock.
func (c *Client) openMessage() Message {
	turn := c.store.OpenTurn()
	e := c.emitted[turn]
	return c.message(turn, c.store.OpenBase(e), c.store.OpenSuffix(e))
}

// trimClosed enforces the retention limit. The store keeps its messages in
// (Turn, From) order by construction, so the sort this used to do — over a
// list in ARRIVAL order, which interleaves when a live-sealed message precedes
// a catch-up read of older history — is now the substrate's job.
func (c *Client) trimClosed() {
	if c.closedLimit <= 0 || c.store.Count() <= c.closedLimit {
		return
	}
	c.closedRev++
	c.store.TrimOldestTo(c.closedLimit)
	// Rebuild by WALKING the retained set, not by materializing a copy of it:
	// this runs on every Apply once an aria is longer than the limit, and the
	// copy doubled the retention path's allocation.
	first, n := 0, 0
	c.closedSeen = make(map[int]bool, c.closedLimit)
	c.closedFrom = make(map[int]int, c.closedLimit)
	c.store.ForEach(func(m Message) bool {
		if n == 0 {
			first = m.Turn
		}
		n++
		c.closedSeen[m.Turn] = true
		return true
	})
	if n > 0 && first > c.closedFloor {
		c.closedFloor = first
	}
	// A question held for a turn whose head never arrived would otherwise
	// outlive every slice of it. Retention is the one place that knows.
	for id := range c.inquiry {
		if id < c.closedFloor {
			delete(c.inquiry, id)
		}
	}
}

// noteClosedFrom records the lowest offset finalized for a turn, so a later
// page carrying earlier nodes knows how much of itself is new.
func (c *Client) noteClosedFrom(id int, finalized []Message) {
	for _, m := range finalized {
		if m.Turn != id {
			continue
		}
		if cur, ok := c.closedFrom[id]; !ok || int(m.From) < cur {
			c.closedFrom[id] = int(m.From)
		}
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
	if c.store.OpenTurn() == 0 {
		return nil
	}
	m := c.openMessage()
	return &m
}

// View returns a snapshot of the current local state.
func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Ordered by the FULL identity (Turn, From), which the store maintains: a
	// turn arrives as several slices, and arrival order interleaves when a
	// live-sealed message precedes a catch-up Read of older history.
	//
	// This is the flat list View has always returned — the one place a hole is
	// invisible. It is the phase-1 shim; consumers move to Store.Query, which
	// cannot lie about adjacency, one at a time afterwards.
	v := View{Closed: c.store.All()}
	if c.store.OpenTurn() != 0 {
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
		case "input":
			n.Input = livedoc.Apply(n.Input, dl)
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
	case "sender":
		// Dropping this silently would make every STREAMED input block look
		// unattributed while a re-read showed its sender — the same exchange
		// telling two stories, which is exactly how the role field broke once.
		n.Sender = asStr(v)
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
	case "input":
		n.Input = asStr(v)
	case "id":
		n.ID = asStr(v)
	case "opened_at":
		n.OpenedAt = asInt64(v)
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
