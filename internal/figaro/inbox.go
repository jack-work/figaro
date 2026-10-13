package figaro

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/actor"
)

type Inbox struct {
	// q is the same single-writer queue a Form writes through.
	q    *actor.Queue[event]
	wake chan struct{} // signal only; events stay in the FIFO

	mu     sync.Mutex // always taken inside q's lock
	epoch  string
	nextID uint64

	// lifted holds prompts the drain loop took but has not appended; committed the
	// last few it did. They let a refusal say which kind of too-late it was.
	lifted    []promptRef
	committed []promptRef
	// departed holds the recently dropped and drained: two different facts.
	departed []promptRef

	onChange func(InboxSnapshot)
}

// promptRef remembers an id after its event has left the queue; merged keeps
// a folded id resolving to its survivor.
type promptRef struct {
	id     uint64
	merged []uint64
	// text and at let a lifted or committed message still be shown.
	text   string
	sender string
	at     int64
	// turn is the turn a committed message became; zero until the turn opens.
	turn  uint64
	state rpc.QueueState
}

// QueueItem is one message as the queue intrinsic publishes it.
type QueueItem struct {
	ID     uint64
	Text   string
	Sender string
	State  rpc.QueueState
	At     int64
	Merged []uint64
	// Into is the surviving id for a merged message.
	Into uint64
	Turn uint64
}

// InboxSnapshot is the whole queue at one instant: live FIFO plus recent departures.
type InboxSnapshot struct {
	Epoch string
	Order []uint64
	Items []QueueItem
}

// committedRing only has to outlive a client's read-mutate round trip.
const committedRing = 64

func NewInbox(ctx context.Context) *Inbox {
	b := &Inbox{wake: make(chan struct{}, 1), epoch: mintEpoch()}
	b.q = actor.Start[event](ctx, nil, nil)
	return b
}

// OnChange installs the queue intrinsic's publisher. The inbox is the only
// publisher. The sink runs on whatever goroutine moved the queue and must not block.
func (b *Inbox) OnChange(fn func(InboxSnapshot)) {
	b.mu.Lock()
	b.onChange = fn
	b.mu.Unlock()
	fn(b.Project())
}

// notify runs after the queue's lock is released: the sink writes a form with
// its own actor, and holding both would order two unrelated serialization points.
func (b *Inbox) notify() {
	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn == nil {
		return
	}
	fn(b.Project())
}

func (b *Inbox) Project() InboxSnapshot {
	var pending []event
	b.q.Read(func(p []event) { pending = append([]event(nil), p...) })

	b.mu.Lock()
	defer b.mu.Unlock()

	snap := InboxSnapshot{Epoch: b.epoch}
	for _, e := range pending {
		if e.typ != eventUserPrompt || e.id == 0 {
			continue
		}
		snap.Order = append(snap.Order, e.id)
		snap.Items = append(snap.Items, QueueItem{
			ID: e.id, Text: e.text, Sender: senderOf(e), State: rpc.QueueStateQueued,
			At: e.at, Merged: e.merged,
		})
		// A folded message is absorbed, not gone: say so with the survivor's id.
		for _, m := range e.merged {
			snap.Items = append(snap.Items, QueueItem{
				ID: m, State: rpc.QueueStateMerged, Into: e.id, At: e.at,
			})
		}
	}
	for _, refs := range [][]promptRef{b.lifted, b.committed, b.departed} {
		for _, r := range refs {
			snap.Items = append(snap.Items, QueueItem{
				ID: r.id, Text: r.text, Sender: r.sender, State: r.state,
				At: r.at, Merged: r.merged, Turn: r.turn,
			})
			for _, m := range r.merged {
				snap.Items = append(snap.Items, QueueItem{
					ID: m, State: rpc.QueueStateMerged, Into: r.id, At: r.at,
				})
			}
		}
	}
	return snap
}

// InflightCount is the queue plus whatever the drain loop is holding.
func (b *Inbox) InflightCount() int {
	n := 0
	b.q.Read(func(pending []event) {
		for _, e := range pending {
			if e.typ == eventUserPrompt && e.id != 0 {
				n++
			}
		}
	})
	b.mu.Lock()
	n += len(b.lifted)
	b.mu.Unlock()
	return n
}

// MarkTurn stamps the turn a committed message became.
func (b *Inbox) MarkTurn(ids []uint64, turn uint64) {
	if len(ids) == 0 || turn == 0 {
		return
	}
	b.mu.Lock()
	for _, id := range ids {
		for i := range b.committed {
			if b.committed[i].id == id {
				b.committed[i].turn = turn
			}
		}
	}
	b.mu.Unlock()
	b.notify()
}

func senderOf(e event) string {
	if len(e.segments) == 0 {
		return ""
	}
	return e.segments[0].sender
}

// On a rand failure a fixed string would make every stale id look current.
func mintEpoch() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func (b *Inbox) Epoch() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

// signalWake is only a "look again"; the event stays in the FIFO.
func (b *Inbox) signalWake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Send returns false if closed. An event arriving with an id is a restored one.
func (b *Inbox) Send(evt event) bool {
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
	b.notify()
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
	b.notify()
	return evt, true
}

// TakeReadyUserPrompts never jumps prompts over an earlier control or fork event.
func (b *Inbox) TakeReadyUserPrompts() []event {
	taken := b.q.TakeWhile(func(e event) bool { return e.typ == eventUserPrompt })
	b.mu.Lock()
	for _, evt := range taken {
		b.liftLocked(evt)
	}
	b.mu.Unlock()
	if len(taken) > 0 {
		b.notify()
	}
	return taken
}

// liftLocked records that a prompt left the queue; until MarkCommitted, a delete is refused as "committing".
func (b *Inbox) liftLocked(evt event) {
	if evt.typ != eventUserPrompt || evt.id == 0 {
		return
	}
	b.lifted = append(b.lifted, promptRef{
		id: evt.id, merged: evt.merged, text: evt.text, sender: senderOf(evt),
		at: evt.at, state: rpc.QueueStateCommitting,
	})
}

// MarkCommitted moves ids from in-flight to answered.
func (b *Inbox) MarkCommitted(events []event) {
	if len(events) == 0 {
		return
	}
	b.markCommitted(events)
	b.notify()
}

func (b *Inbox) markCommitted(events []event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, evt := range events {
		if evt.typ != eventUserPrompt || evt.id == 0 {
			continue
		}
		// Every id this event speaks for leaves the lifted set: a coalesced run is
		// lifted as N events and committed as one.
		b.dropLiftedLocked(evt.id)
		for _, m := range evt.merged {
			b.dropLiftedLocked(m)
		}
		b.committed = append(b.committed, promptRef{
			id: evt.id, merged: evt.merged, text: evt.text, sender: senderOf(evt),
			at: evt.at, state: rpc.QueueStateCommitted,
		})
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

func (b *Inbox) Prepend(events []event) bool {
	if len(events) == 0 {
		return true
	}
	if b.q.Closed() {
		return false
	}
	b.q.Do(func(pending []event) []event {
		// Queued again means no longer in flight: a delete must be honoured again.
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
	b.notify()
	return true
}

// TakeReadySet removes the contiguous prefix of round-boundary events, never jumping an earlier prompt.
func (b *Inbox) TakeReadySet() []event {
	return b.q.TakeWhile(func(e event) bool {
		return e.typ == eventStudyMark
	})
}

// CoalesceUserPromptRuns folds each contiguous run of prompts into one event at the run's first position.
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
	b.notify()
}

// DrainUserPrompts removes every queued prompt verbatim; control events stay.
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
	b.remember(drained, rpc.QueueStateDrained)
	b.notify()
	return drained
}

// remember parks departed messages so their fate is published, not inferred from absence.
func (b *Inbox) remember(events []event, state rpc.QueueState) {
	if len(events) == 0 {
		return
	}
	b.mu.Lock()
	for _, e := range events {
		if e.typ != eventUserPrompt || e.id == 0 {
			continue
		}
		b.departed = append(b.departed, promptRef{
			id: e.id, merged: e.merged, text: e.text, sender: senderOf(e),
			at: e.at, state: state,
		})
	}
	if excess := len(b.departed) - committedRing; excess > 0 {
		b.departed = append([]promptRef(nil), b.departed[excess:]...)
	}
	b.mu.Unlock()
}

// DeletePrompts reports, per id, what happened.
func (b *Inbox) DeletePrompts(epoch string, ids []uint64, all bool) (string, []rpc.QueueResult) {
	var epochOut string
	var results []rpc.QueueResult
	var dropped []event
	b.q.Do(func(pending []event) []event {
		var kept []event
		kept, epochOut, results, dropped = b.deleteLocked(pending, epoch, ids, all)
		return kept
	})
	b.remember(dropped, rpc.QueueStateDropped)
	b.notify()
	return epochOut, results
}

// deleteLocked runs inside the queue's lock and takes the bookkeeping lock second; nothing takes them the other way.
func (b *Inbox) deleteLocked(queue []event, epoch string, ids []uint64, all bool) ([]event, string, []rpc.QueueResult, []event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var dropped []event
	if all {
		var results []rpc.QueueResult
		kept := make([]event, 0, len(queue))
		for _, e := range queue {
			if e.typ != eventUserPrompt {
				kept = append(kept, e)
				continue
			}
			dropped = append(dropped, e)
			results = append(results, rpc.QueueResult{ID: e.id, Outcome: rpc.QueueDeleted})
		}
		return kept, b.epoch, results, dropped
	}

	results := make([]rpc.QueueResult, 0, len(ids))
	if reason, detail, stale := b.staleLocked(epoch); stale {
		for _, id := range ids {
			results = append(results, rpc.QueueResult{
				ID: id, Outcome: rpc.QueueRejected, Reason: reason, Detail: detail,
			})
		}
		return queue, b.epoch, results, nil
	}
	for _, id := range ids {
		if i := indexOf(queue, id); i >= 0 {
			dropped = append(dropped, queue[i])
			queue = append(queue[:i], queue[i+1:]...)
			results = append(results, rpc.QueueResult{ID: id, Outcome: rpc.QueueDeleted})
			continue
		}
		results = append(results, b.refuseLocked(queue, id))
	}
	return queue, b.epoch, results, dropped
}

// UpdatePrompt follows the same per-id outcome and compare-and-swap rule as DeletePrompts.
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
	b.notify()
	return epochOut, result
}

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

// refuseLocked answers in chronological order (folded, in flight, answered,
// unknown) so the reason is the most specific true one.
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
	for _, ref := range b.departed {
		if ref.id == id || containsID(ref.merged, id) {
			if ref.state == rpc.QueueStateDrained {
				return reject(rpc.RejectUnknown,
					"an interrupt cleared the queue and this went with it", 0)
			}
			return reject(rpc.RejectUnknown, "already deleted from this generation", 0)
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

func (b *Inbox) IsIdle() bool { return b.q.IsIdle() }

// SnapshotPrompts copies queued prompts without removing them; carriers==false omits empty-text form carriers.
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

// pending is for tests only: reading the queue to decide what to do races the drain loop.
func (b *Inbox) pending() []event {
	var out []event
	b.q.Read(func(p []event) { out = append([]event(nil), p...) })
	return out
}
