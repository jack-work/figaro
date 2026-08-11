package figaro

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/actor"
	"github.com/jack-work/figaro/internal/rpc"
)

// Inbox is the per-aria user-RPC event queue.
//
// IDENTITY. Every queued user prompt carries an id, minted here, so the CRUD
// surface has something to address. Ids are dense and per-INBOX, which makes
// them typeable (`figaro queue rm 3`) but NOT durable: a new Inbox, a daemon
// restart, a dormant→attach, a panic-restart of the drain loop: starts again
// at 1. A client holding an id from a previous generation would otherwise
// delete whatever holds that number now.
//
// So the inbox also mints an EPOCH: 8 bytes of crypto/rand, hex, once per
// inbox. Every mutation must present the epoch its ids were read against, and
// a mismatch is refused rather than resolved. The epoch is compared only for
// EQUALITY: never ordered: which is exactly why a random nonce is the right
// primitive here and a clock is not (a clock can go backwards), nor the log
// tail (it does not advance when an agent boots, queues, and dies without
// appending: the precise case that reproduces a colliding id).
type Inbox struct {
	// q is the runtime: internal/actor, the same single-writer queue a Form
	// writes through. Everything below it is prompt bookkeeping, which is what
	// an inbox IS beyond the queue.
	q    *actor.Queue[event]
	wake chan struct{} // queue-change signal only; events remain in the FIFO

	mu     sync.Mutex // guards the bookkeeping, always taken INSIDE q's lock
	epoch  string
	nextID uint64

	// lifted holds prompts the drain loop has taken but not yet appended, and
	// committed the last few it did append. Together they are what lets a
	// refusal say WHICH kind of too-late it was: "committing" vs "committed"
	// vs "never heard of it": instead of collapsing all three into unknown.
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
	// No handler: an inbox is drained by the agent's own loop calling Recv, not
	// by a goroutine the queue owns. Start still gives the FIFO, the wait, and
	// the close semantics: the parts that were worth having once.
	b.q = actor.Start[event](ctx, nil, nil)
	return b
}

// mintEpoch returns a fresh inbox generation token. crypto/rand cannot
// meaningfully fail here, but if it ever did, a fixed string would silently
// make every stale id look current: so fall back to something unique enough
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

// signalWake nudges a waiter that the queue changed. The event stays in the
// FIFO; this is only a "look again".
func (b *Inbox) signalWake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Send enqueues an event. Returns false if closed.
func (b *Inbox) Send(evt event) bool {
	// Only prompts are addressable, so only prompts consume an id: a dense
	// sequence a human can type beats one with gaps where a `set` went by. An
	// event arriving with an id already set is a restored one and keeps it.
	if evt.typ == eventUserPrompt && evt.id == 0 {
		b.mu.Lock()
		b.nextID++
		evt.id = b.nextID
		b.mu.Unlock()
		if evt.at == 0 {
			evt.at = time.Now().UnixMilli()
		}
	}
	if !b.q.Send(evt) {
		return false
	}
	b.signalWake()
	return true
}

func (b *Inbox) Wake() <-chan struct{} { return b.wake }

func (b *Inbox) Recv() (event, bool) {
	evt, ok := b.q.Recv()
	if !ok {
		return event{}, false
	}
	b.mu.Lock()
	b.liftLocked(evt)
	b.mu.Unlock()
	return evt, true
}

// TakeReadyUserPrompts removes the contiguous user-prompt prefix. It never
// jumps prompts over an earlier control or fork event.
func (b *Inbox) TakeReadyUserPrompts() []event {
	taken := b.q.TakeWhile(func(e event) bool { return e.typ == eventUserPrompt })
	b.mu.Lock()
	for _, evt := range taken {
		b.liftLocked(evt)
	}
	b.mu.Unlock()
	return taken
}

// liftLocked records that a prompt left the queue for the drain loop. Until
// MarkCommitted moves it on, a delete aimed at it is refused as "committing"
// : it is becoming a message right now, and that is a different fact from
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
	if b.q.Closed() {
		return false
	}
	b.q.Do(func(pending []event) []event {
		// They are queued again, so they are no longer in flight: a delete
		// aimed at one must go back to being honoured rather than refused as
		// "committing" forever.
		b.mu.Lock()
		for _, evt := range events {
			if evt.typ == eventUserPrompt && evt.id != 0 {
				b.dropLiftedLocked(evt.id)
			}
		}
		b.mu.Unlock()
		return append(append(make([]event, 0, len(events)+len(pending)), events...), pending...)
	})
	b.signalWake()
	return true
}

// TakeReadySet removes the contiguous eventSet prefix. It never jumps a set
// over an earlier prompt, so FIFO order across event kinds is preserved by the
// caller's drain loop.
func (b *Inbox) TakeReadySet() []event {
	return b.q.TakeWhile(func(e event) bool { return e.typ == eventSet })
}

// CoalesceUserPromptRuns folds each CONTIGUOUS RUN of queued user prompts
// into one event, parked at that run's first position and keeping its id.
//
// Called from Agent.Interrupt and NOWHERE ELSE. That is the whole guard: the
// normal submit path (Send), the mid-turn steering drain
// (TakeReadyUserPrompts) and the durability retry (Prepend) have no way to
// reach this, so no flag has to be threaded anywhere and no shared helper has
// to ask whether it is being interrupted. The result is an ORDINARY queue -
// the drain loop cannot tell a fold happened, because there is nothing to
// tell: one event holding one multi-line message is a shape it already
// handles.
//
// A QUEUED SET OR FORK IS A BARRIER and is never crossed. All three takers
// above are prefix-only precisely so FIFO across event kinds is preserved,
// and this must not be the one place that reorders across them: a `set`
// exists to change context BEFORE the prompt behind it, so folding that
// prompt in front of the set would answer it against a form it was
// never written against: with no error, no log line, and nothing to notice.
// Across a fork it is worse: the message would land in the wrong trunk.
//
// In the gesture this exists for, a person with several messages typed
// during one long turn, hitting Ctrl-C: set and fork arrive by CLI rather
// than the composer, so there is no interleaved control event and run
// coalescing IS whole-queue coalescing.
func (b *Inbox) CoalesceUserPromptRuns() {
	b.q.Do(func(pending []event) []event {
		if len(pending) < 2 {
			return pending
		}
		out := make([]event, 0, len(pending))
		for i := 0; i < len(pending); {
			if pending[i].typ != eventUserPrompt {
				out = append(out, pending[i])
				i++
				continue
			}
			j := i
			for j < len(pending) && pending[j].typ == eventUserPrompt {
				j++
			}
			if merged, ok := mergePromptEvents(pending[i:j]); ok {
				out = append(out, merged)
			}
			i = j
		}
		return out
	})
}

// DrainUserPrompts removes every queued user prompt and returns them
// VERBATIM, in FIFO order, each with its own id: not folded. Control events
// (sets, forks) are left in the queue: this drops the questions, it does not
// cancel the form mutation or the fork someone else asked for.
//
// Verbatim is the point. What is drained is handed back so it can be written
// to disk instead of lost, and a caller who typed three messages wants their
// three messages back: not one blob that has to be unpicked.
func (b *Inbox) DrainUserPrompts() []event {
	var drained []event
	b.q.Do(func(pending []event) []event {
		if len(pending) == 0 {
			return pending
		}
		kept := make([]event, 0, len(pending))
		drained = make([]event, 0, len(pending))
		for _, e := range pending {
			if e.typ == eventUserPrompt {
				drained = append(drained, e)
				continue
			}
			kept = append(kept, e)
		}
		return kept
	})
	return drained
}

// DeletePrompts drops queued messages and reports, PER ID, what happened.
//
// A refusal is a decision, not a fault: an id the drain loop has already
// lifted, or already answered, or that an interrupt folded into another
// message, each get their own reason so the caller can act on the difference
// instead of guessing at "it didn't work".
//
// epoch is a compare-and-swap token and is required whenever ids are named:
// ids restart with every inbox, so resolving one against the wrong generation
// would delete a different message than the caller read. A mismatch refuses
// the WHOLE request: nothing is mutated: rather than deleting the ids that
// happen to exist in both.
//
// The all-form names no id, so it needs no epoch and reports one result per
// message actually removed (possibly none).
func (b *Inbox) DeletePrompts(epoch string, ids []uint64, all bool) (string, []rpc.QueueResult) {
	var epochOut string
	var results []rpc.QueueResult
	b.q.Do(func(pending []event) []event {
		var kept []event
		kept, epochOut, results = b.deleteLocked(pending, epoch, ids, all)
		return kept
	})
	return epochOut, results
}

// deleteLocked runs inside the queue's lock; it takes the bookkeeping lock
// second, and nothing takes them the other way round.
func (b *Inbox) deleteLocked(queue []event, epoch string, ids []uint64, all bool) ([]event, string, []rpc.QueueResult) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if all {
		var results []rpc.QueueResult
		kept := make([]event, 0, len(queue))
		for _, e := range queue {
			if e.typ != eventUserPrompt {
				kept = append(kept, e)
				continue
			}
			results = append(results, rpc.QueueResult{ID: e.id, Outcome: rpc.QueueDeleted})
		}
		return kept, b.epoch, results
	}

	results := make([]rpc.QueueResult, 0, len(ids))
	if reason, detail, stale := b.staleLocked(epoch); stale {
		for _, id := range ids {
			results = append(results, rpc.QueueResult{
				ID: id, Outcome: rpc.QueueRejected, Reason: reason, Detail: detail,
			})
		}
		return queue, b.epoch, results
	}
	for _, id := range ids {
		if i := indexOf(queue, id); i >= 0 {
			queue = append(queue[:i], queue[i+1:]...)
			results = append(results, rpc.QueueResult{ID: id, Outcome: rpc.QueueDeleted})
			continue
		}
		results = append(results, b.refuseLocked(queue, id))
	}
	return queue, b.epoch, results
}

// UpdatePrompt replaces the text of one queued message, with the same
// per-id outcome and the same compare-and-swap rule as DeletePrompts.
func (b *Inbox) UpdatePrompt(epoch string, id uint64, text string) (string, rpc.QueueResult) {
	var epochOut string
	var result rpc.QueueResult
	b.q.Do(func(pending []event) []event {
		b.mu.Lock()
		defer b.mu.Unlock()
		epochOut = b.epoch
		if reason, detail, stale := b.staleLocked(epoch); stale {
			result = rpc.QueueResult{ID: id, Outcome: rpc.QueueRejected, Reason: reason, Detail: detail}
			return pending
		}
		if i := indexOf(pending, id); i >= 0 {
			pending[i].text = text
			result = rpc.QueueResult{ID: id, Outcome: rpc.QueueUpdated}
			return pending
		}
		result = b.refuseLocked(pending, id)
		return pending
	})
	return epochOut, result
}

// staleLocked reports whether a caller's epoch disqualifies its ids.
func (b *Inbox) staleLocked(epoch string) (rpc.QueueRejection, string, bool) {
	if b.q.Closed() {
		return rpc.RejectClosed, "the aria is stopping", true
	}
	if epoch == "" {
		return rpc.RejectStale, "no epoch supplied: read the queue first, then mutate against what you read", true
	}
	if epoch != b.epoch {
		return rpc.RejectStale, "the queue was rebuilt (the agent restarted); re-read it and try again", true
	}
	return "", "", false
}

func indexOf(queue []event, id uint64) int {
	if id == 0 {
		return -1
	}
	for i := range queue {
		if queue[i].typ == eventUserPrompt && queue[i].id == id {
			return i
		}
	}
	return -1
}

// refuseLocked explains why an id that is not in the queue cannot be mutated.
// The order is chronological: folded, then in flight, then answered, then
// never heard of: so the reason is always the most specific true one.
func (b *Inbox) refuseLocked(queue []event, id uint64) rpc.QueueResult {
	reject := func(reason rpc.QueueRejection, detail string, into uint64) rpc.QueueResult {
		return rpc.QueueResult{
			ID: id, Outcome: rpc.QueueRejected, Reason: reason, Detail: detail, Into: into,
		}
	}
	for i := range queue {
		if queue[i].typ == eventUserPrompt && containsID(queue[i].merged, id) {
			return reject(rpc.RejectMerged,
				"an interrupt folded this message into another one still queued", queue[i].id)
		}
	}
	for _, ref := range b.lifted {
		if ref.id == id || containsID(ref.merged, id) {
			return reject(rpc.RejectCommitting,
				"the agent lifted this message out of the queue and is committing it now", ref.id)
		}
	}
	for _, ref := range b.committed {
		if ref.id == id || containsID(ref.merged, id) {
			return reject(rpc.RejectCommitted,
				"already part of the conversation; it cannot be unasked", ref.id)
		}
	}
	return reject(rpc.RejectUnknown, "no such queued message in this generation", 0)
}

func containsID(ids []uint64, id uint64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// IsIdle reports whether the inbox is empty (no events queued).
func (b *Inbox) IsIdle() bool { return b.q.IsIdle() }

// SnapshotPrompts returns a copy of every queued user prompt in FIFO order,
// WITHOUT removing them. carriers==false omits empty-text prompts (pure
// form carriers), which is what every display surface wants and what
// this has always returned; the CRUD surface asks for them because it must be
// able to address what it can delete.
//
// Non-prompt events (sets, forks) are skipped by design: this is the "what am
// I about to be asked next?" view, not a dump of the actor's mailbox.
func (b *Inbox) SnapshotPrompts(carriers bool) []event {
	var out []event
	b.q.Read(func(pending []event) {
		out = make([]event, 0, len(pending))
		for _, e := range pending {
			if e.typ != eventUserPrompt {
				continue
			}
			if !carriers && e.text == "" {
				continue
			}
			out = append(out, e)
		}
	})
	return out
}

func (b *Inbox) Close() { b.q.Close() }

// pending is the queued events, for a test that asserts on order across event
// kinds. Not for production: a caller that reads the queue to decide what to do
// with it is racing the drain loop.
func (b *Inbox) pending() []event {
	var out []event
	b.q.Read(func(p []event) { out = append([]event(nil), p...) })
	return out
}
