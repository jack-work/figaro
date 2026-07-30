package figaro

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Inbox is the per-aria user-RPC event queue.
//
// IDENTITY. Every queued user prompt carries an id, minted here, so the CRUD
// surface has something to address. Ids are dense and per-INBOX, which makes
// them typeable (`figaro queue rm 3`) but NOT durable: a new Inbox — a daemon
// restart, a dormant→attach, a panic-restart of the drain loop — starts again
// at 1. A client holding an id from a previous generation would otherwise
// delete whatever holds that number now.
//
// So the inbox also mints an EPOCH: 8 bytes of crypto/rand, hex, once per
// inbox. Every mutation must present the epoch its ids were read against, and
// a mismatch is refused rather than resolved. The epoch is compared only for
// EQUALITY — never ordered — which is exactly why a random nonce is the right
// primitive here and a clock is not (a clock can go backwards), nor the log
// tail (it does not advance when an agent boots, queues, and dies without
// appending: the precise case that reproduces a colliding id).
type Inbox struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []event
	wake   chan struct{} // queue-change signal only; events remain in the FIFO
	closed bool

	epoch  string
	nextID uint64

	// lifted holds prompts the drain loop has taken but not yet appended, and
	// committed the last few it did append. Together they are what lets a
	// refusal say WHICH kind of too-late it was — "committing" vs "committed"
	// vs "never heard of it" — instead of collapsing all three into unknown.
	lifted    []promptRef
	committed []promptRef
}

// promptRef remembers an id after its event has left the queue. Merged travels
// with it so an id folded away by an interrupt keeps resolving to its
// survivor even after that survivor is itself drained.
type promptRef struct {
	id     uint64
	merged []uint64
}

// committedRing bounds the memory of answered ids. It only has to outlive a
// client's read→mutate round trip; past that, "unknown" is the honest answer.
const committedRing = 64

func NewInbox(ctx context.Context) *Inbox {
	b := &Inbox{wake: make(chan struct{}, 1), epoch: mintEpoch()}
	b.cond = sync.NewCond(&b.mu)
	go func() {
		<-ctx.Done()
		b.Close()
	}()
	return b
}

// mintEpoch returns a fresh inbox generation token. crypto/rand cannot
// meaningfully fail here, but if it ever did, a fixed string would silently
// make every stale id look current — so fall back to something unique enough
// to still differ from the previous generation.
func mintEpoch() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

// Epoch is the generation queued ids belong to.
func (b *Inbox) Epoch() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

// Send enqueues an event. Returns false if closed.
func (b *Inbox) Send(evt event) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	// Only prompts are addressable, so only prompts consume an id: a dense
	// sequence a human can type beats one with gaps where a `set` went by.
	// An event arriving with an id already set is a restored one (Prepend
	// routes around Send, but be explicit) and keeps it.
	if evt.typ == eventUserPrompt && evt.id == 0 {
		b.nextID++
		evt.id = b.nextID
		if evt.at == 0 {
			evt.at = time.Now().UnixMilli()
		}
	}
	b.queue = append(b.queue, evt)
	b.cond.Signal()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

func (b *Inbox) Wake() <-chan struct{} { return b.wake }

func (b *Inbox) Recv() (event, bool) {
	b.mu.Lock()
	for len(b.queue) == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.closed && len(b.queue) == 0 {
		b.mu.Unlock()
		return event{}, false
	}
	evt := b.queue[0]
	copy(b.queue, b.queue[1:])
	b.queue = b.queue[:len(b.queue)-1]
	b.liftLocked(evt)
	b.mu.Unlock()
	return evt, true
}

// TakeReadyUserPrompts removes the contiguous user-prompt prefix. It never
// jumps prompts over an earlier control or fork event.
func (b *Inbox) TakeReadyUserPrompts() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for n < len(b.queue) && b.queue[n].typ == eventUserPrompt {
		n++
	}
	taken := append([]event(nil), b.queue[:n]...)
	copy(b.queue, b.queue[n:])
	b.queue = b.queue[:len(b.queue)-n]
	for _, evt := range taken {
		b.liftLocked(evt)
	}
	b.signalReadyForkLocked()
	return taken
}

// liftLocked records that a prompt left the queue for the drain loop. Until
// MarkCommitted moves it on, a delete aimed at it is refused as "committing"
// — it is becoming a message right now, and that is a different fact from
// "already answered" or "never existed".
func (b *Inbox) liftLocked(evt event) {
	if evt.typ != eventUserPrompt || evt.id == 0 {
		return
	}
	b.lifted = append(b.lifted, promptRef{id: evt.id, merged: evt.merged})
}

// MarkCommitted moves ids from in-flight to answered: the drain loop has
// durably appended them to the IR, so a delete can no longer be honoured and
// the refusal should say so precisely.
func (b *Inbox) MarkCommitted(events []event) {
	if len(events) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, evt := range events {
		if evt.typ != eventUserPrompt || evt.id == 0 {
			continue
		}
		b.dropLiftedLocked(evt.id)
		b.committed = append(b.committed, promptRef{id: evt.id, merged: evt.merged})
	}
	if excess := len(b.committed) - committedRing; excess > 0 {
		b.committed = append([]promptRef(nil), b.committed[excess:]...)
	}
}

func (b *Inbox) dropLiftedLocked(id uint64) {
	for i := range b.lifted {
		if b.lifted[i].id == id {
			b.lifted = append(b.lifted[:i], b.lifted[i+1:]...)
			return
		}
	}
}

// Prepend restores events that could not be durably processed.
func (b *Inbox) Prepend(events []event) bool {
	if len(events) == 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	// They are queued again, so they are no longer in flight: a delete aimed
	// at one must go back to being honoured rather than refused as
	// "committing" forever.
	for _, evt := range events {
		if evt.typ == eventUserPrompt && evt.id != 0 {
			b.dropLiftedLocked(evt.id)
		}
	}
	queue := make([]event, 0, len(events)+len(b.queue))
	queue = append(queue, events...)
	queue = append(queue, b.queue...)
	b.queue = queue
	b.cond.Signal()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

func (b *Inbox) TakeReadyForks() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for n < len(b.queue) {
		if b.queue[n].typ != eventFork {
			break
		}
		n++
	}
	taken := append([]event(nil), b.queue[:n]...)
	copy(b.queue, b.queue[n:])
	b.queue = b.queue[:len(b.queue)-n]
	return taken
}

// TakeReadySet removes the contiguous eventSet prefix — the chalkboard-patch
// analog of TakeReadyForks. It never jumps a set over an earlier prompt or
// fork, so FIFO order across event kinds is preserved by the caller's drain
// loop.
func (b *Inbox) TakeReadySet() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for n < len(b.queue) && b.queue[n].typ == eventSet {
		n++
	}
	taken := append([]event(nil), b.queue[:n]...)
	copy(b.queue, b.queue[n:])
	b.queue = b.queue[:len(b.queue)-n]
	return taken
}

// CoalesceUserPromptRuns folds each CONTIGUOUS RUN of queued user prompts
// into one event, parked at that run's first position and keeping its id.
//
// Called from Agent.Interrupt and NOWHERE ELSE. That is the whole guard: the
// normal submit path (Send), the mid-turn steering drain
// (TakeReadyUserPrompts) and the durability retry (Prepend) have no way to
// reach this, so no flag has to be threaded anywhere and no shared helper has
// to ask whether it is being interrupted. The result is an ORDINARY queue —
// the drain loop cannot tell a fold happened, because there is nothing to
// tell: one event holding one multi-line message is a shape it already
// handles.
//
// A QUEUED SET OR FORK IS A BARRIER and is never crossed. All three takers
// above are prefix-only precisely so FIFO across event kinds is preserved,
// and this must not be the one place that reorders across them: a `set`
// exists to change context BEFORE the prompt behind it, so folding that
// prompt in front of the set would answer it against a chalkboard it was
// never written against — with no error, no log line, and nothing to notice.
// Across a fork it is worse: the message would land in the wrong trunk.
//
// In the gesture this exists for — a person with several messages typed
// during one long turn, hitting Ctrl-C — set and fork arrive by CLI rather
// than the composer, so there is no interleaved control event and run
// coalescing IS whole-queue coalescing.
func (b *Inbox) CoalesceUserPromptRuns() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) < 2 {
		return
	}
	out := make([]event, 0, len(b.queue))
	for i := 0; i < len(b.queue); {
		if b.queue[i].typ != eventUserPrompt {
			out = append(out, b.queue[i])
			i++
			continue
		}
		j := i
		for j < len(b.queue) && b.queue[j].typ == eventUserPrompt {
			j++
		}
		if merged, ok := mergePromptEvents(b.queue[i:j]); ok {
			out = append(out, merged)
		}
		i = j
	}
	b.queue = out
}

// DrainUserPrompts removes every queued user prompt and returns them
// VERBATIM, in FIFO order, each with its own id — not folded. Control events
// (sets, forks) are left in the queue: this drops the questions, it does not
// cancel the chalkboard mutation or the fork someone else asked for.
//
// Verbatim is the point. What is drained is handed back so it can be written
// to disk instead of lost, and a caller who typed three messages wants their
// three messages back — not one blob that has to be unpicked.
func (b *Inbox) DrainUserPrompts() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return nil
	}
	drained := make([]event, 0, len(b.queue))
	kept := make([]event, 0, len(b.queue))
	for _, e := range b.queue {
		if e.typ == eventUserPrompt {
			drained = append(drained, e)
			continue
		}
		kept = append(kept, e)
	}
	b.queue = kept
	return drained
}

func (b *Inbox) signalReadyForkLocked() {
	if len(b.queue) == 0 || b.queue[0].typ != eventFork {
		return
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// IsIdle reports whether the inbox is empty (no events queued).
func (b *Inbox) IsIdle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue) == 0
}

// SnapshotPrompts returns a copy of every queued user prompt in FIFO order,
// WITHOUT removing them. carriers==false omits empty-text prompts (pure
// chalkboard carriers), which is what every display surface wants and what
// this has always returned; the CRUD surface asks for them because it must be
// able to address what it can delete.
//
// Non-prompt events (sets, forks) are skipped by design: this is the "what am
// I about to be asked next?" view, not a dump of the actor's mailbox.
func (b *Inbox) SnapshotPrompts(carriers bool) []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]event, 0, len(b.queue))
	for _, e := range b.queue {
		if e.typ != eventUserPrompt {
			continue
		}
		if !carriers && e.text == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (b *Inbox) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}
