package cli

// THE SESSION'S INTRINSIC FORM MIRRORS: the client's live copy of `<aria>/runtime`
// and `<aria>/queue`, kept current by the patch protocol rather than by
// asking.
//
// What this replaces is the whole of the original complaint. The queue was
// PULLED, twice a second, off a timer in startPagerClock -- whose own comment
// admitted the defect: "THE QUEUE is a function of state nobody announces -- a
// prompt enters it with no frame emitted, so a pager that does not ask never
// learns." So an open drawer was up to 500ms stale, `:send` had to kick a
// manual refresh to paper over it, and a message between the drain loop's lift
// and the turn frame existed nowhere at all.
//
// Pushed and authoritative, the drawer is current whether it is open or not,
// and the row for a message in flight is a row about a message that is
// somewhere rather than a row that has blinked away.

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/internal/mark"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// intrinsicMirrors holds one mirror per intrinsic, and the resync each needs
// when a delta does not follow the one before.
type intrinsicMirrors struct {
	mu      sync.Mutex
	runtime *formMirror
	queue   *formMirror
	// gen fences a dead connection's deltas off a live subject's mirrors, the
	// same way subjectGen fences its frames.
	gen uint64
}

func newIntrinsicMirrors() *intrinsicMirrors {
	return &intrinsicMirrors{runtime: &formMirror{}, queue: &formMirror{}}
}

// reset drops both mirrors: a new subject has different forms, and folding the
// old aria's queue into the new one's view is the exact class of bug the
// subject generation exists to prevent.
func (m *intrinsicMirrors) reset(gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtime, m.queue = &formMirror{}, &formMirror{}
	m.gen = gen
}

func (m *intrinsicMirrors) of(name string) *formMirror {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch name {
	case intrinsicRuntime:
		return m.runtime
	case intrinsicQueue:
		return m.queue
	}
	return nil
}

// runtimeView is `<aria>/runtime` as the status bar reads it.
type runtimeView struct {
	State    rpc.RuntimeState
	Reason   string
	Inflight int
	Model    string
	Known    bool // false until a patch or a seed has landed
}

func readRuntime(snap form.Snapshot) runtimeView {
	v := runtimeView{}
	if s, ok := lookupString(snap, "turn.state"); ok {
		v.State, v.Known = rpc.RuntimeState(s), true
	}
	v.Reason, _ = lookupString(snap, "turn.reason")
	v.Model, _ = lookupString(snap, "model")
	v.Inflight, _ = lookupInt(snap, "inflight")
	return v
}

// queueRow is one message as the drawer shows it.
type queueRow struct {
	ID     uint64
	Text   string
	Sender string
	State  rpc.QueueState
	Into   uint64
	Turn   uint64
}

// live reports whether the row belongs in the drawer at all.
//
// THE DRAWER IS WHAT HAS NOT BEEN ASKED YET. A message that has become part of
// the conversation is IN the conversation -- it is on screen, in the
// transcript, above the drawer -- and a second copy of it below is not
// reassurance, it is a duplicate the reader has to reconcile.
//
// An earlier version kept `committed` rows with a checkmark until the client's
// own window had adopted the turn. That was solving a problem that ordering
// already solves: runTurn appends the prompt (which emits the frame) BEFORE it
// marks the message committed, so by the time this state is published the
// transcript has already been told. There is no gap to cover, so there is no
// reason to hold the row -- and holding it meant a checkmark state, a
// per-client adoption check, and a reaper to clean up after both.
//
// Deleting a state is the cheapest way to be sure it cannot be wrong.
func (r queueRow) live() bool {
	switch r.State {
	case rpc.QueueStateQueued, rpc.QueueStateCommitting:
		return true
	}
	return false
}

// readQueue projects `<aria>/queue` into ordered rows.
//
// `order` carries FIFO because a form is a map and a map has none. Anything
// the order does not name is sorted by id behind it: a departed message is
// still in the form (that is the point -- it is how a client sees where its
// message went) and must not be dropped merely because it is no longer queued.
func readQueue(snap form.Snapshot) []queueRow {
	byID := map[uint64]*queueRow{}
	for key := range snap.All() {
		id, field, ok := splitQueueKey(key)
		if !ok {
			continue
		}
		row := byID[id]
		if row == nil {
			row = &queueRow{ID: id}
			byID[id] = row
		}
		switch field {
		case "text":
			row.Text, _ = lookupString(snap, key)
		case "sender":
			row.Sender, _ = lookupString(snap, key)
		case "state":
			s, _ := lookupString(snap, key)
			row.State = rpc.QueueState(s)
		case "into":
			n, _ := lookupInt(snap, key)
			row.Into = uint64(n)
		case "turn":
			n, _ := lookupInt(snap, key)
			row.Turn = uint64(n)
		}
	}

	seen := map[uint64]bool{}
	var out []queueRow
	for _, id := range queueOrder(snap) {
		if r, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, *r)
		}
	}
	var rest []queueRow
	for id, r := range byID {
		if !seen[id] {
			rest = append(rest, *r)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].ID < rest[j].ID })
	return append(out, rest...)
}

func queueOrder(snap form.Snapshot) []uint64 {
	raw, ok := snap.Get("order")
	if !ok {
		return nil
	}
	var ids []uint64
	if json.Unmarshal(raw, &ids) != nil {
		return nil
	}
	return ids
}

// splitQueueKey pulls the id and field out of "items.<id>.<field>".
func splitQueueKey(key string) (uint64, string, bool) {
	const prefix = "items."
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return 0, "", false
	}
	rest := key[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			id, err := strconv.ParseUint(rest[:i], 10, 64)
			if err != nil {
				return 0, "", false
			}
			return id, rest[i+1:], true
		}
	}
	return 0, "", false
}

func lookupString(snap form.Snapshot, key string) (string, bool) {
	raw, ok := snap.Get(key)
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func lookupInt(snap form.Snapshot, key string) (int, bool) {
	raw, ok := snap.Get(key)
	if !ok {
		return 0, false
	}
	var n int
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return n, true
}

// applyIntrinsicDelta folds one delta into its mirror and reports whether the
// caller should repaint. A delta for a intrinsic this client does not follow is
// not an error: it is a newer daemon publishing something this build has no
// opinion about, and ignoring it is the whole of forwards compatibility.
func (in *interactiveInput) applyIntrinsicDelta(params json.RawMessage) {
	var d rpc.FormDelta
	if json.Unmarshal(params, &d) != nil {
		return
	}
	if d.Intrinsic == "" {
		return // the board; the transcript has its own path for that
	}
	mirror := in.intrinsics.of(d.Intrinsic)
	if mirror == nil {
		return
	}
	switch mirror.apply(d) {
	case formResync:
		// A GAP IS NOT A GUESS. The mirror says it missed one; re-read the
		// whole form rather than carrying on with a state that is silently
		// wrong. This is the same discipline `form show` has always used.
		go in.resyncIntrinsic(d.Intrinsic)
		return
	case formIncompatible:
		return
	}
	in.onIntrinsicChanged(d.Intrinsic)
}

// resyncIntrinsic re-reads one intrinsic whole. Off the notify goroutine: it is
// an RPC, and the notify pump must never block on the wire it is reading.
func (in *interactiveInput) resyncIntrinsic(name string) {
	cli := in.aria()
	if cli == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := cli.FormOf(ctx, name)
	if err != nil {
		return
	}
	mirror := in.intrinsics.of(name)
	if mirror == nil {
		return
	}
	mirror.reset(resp.Snapshot, resp.Version)
	in.onIntrinsicChanged(name)
}

// seedIntrinsics reads both intrinsic forms once, at connect. A mirror that has never
// been seeded shows nothing, which for the status bar means it would sit on
// whatever the client last guessed.
func (in *interactiveInput) seedIntrinsics() {
	for _, name := range intrinsicNames() {
		go in.resyncIntrinsic(name)
	}
}

// onIntrinsicChanged is what a landed patch DOES: project the mirror onto the
// pager's model and repaint. Under the render lock, because a delta arrives on
// the notifier's goroutine while a keystroke is being handled.
func (in *interactiveInput) onIntrinsicChanged(name string) {
	switch name {
	case intrinsicQueue:
		in.projectQueue()
	case intrinsicRuntime:
		snap, _, _ := in.intrinsics.runtime.state()
		rt := readRuntime(snap)
		mark.Mark("runtime", "state", string(rt.State))
		in.mu.Lock()
		if in.lt.status.setRuntime(rt) {
			in.lt.render()
		}
		in.mu.Unlock()
	}
}

// queueDebutGrace is how long a row that arrived while the aria was IDLE must
// survive before it may be drawn.
//
// A prompt sent into an idle figaro is queued and lifted again in the same
// breath: measured on this bench, the row was live for 8 to 66 ms, which is
// one to four frames of a row appearing, the drawer opening under it, and both
// going away before the turn starts. Nothing about that is information. A row
// that is still there after the grace is a row that will really sit there, and
// it is drawn then.
//
// The grace is spent only against an idle aria. Send into one that is already
// working and the row is drawn at once, because that queue is the answer to
// "where did my message go" and it will be on screen for seconds.
const queueDebutGrace = 150 * time.Millisecond

// queueDebut decides which live rows may be drawn NOW.
//
// debut is the map of id to the earliest time each row may appear, carried
// across calls: a row keeps the deadline it was given when it first arrived,
// so a queue that mutates twice inside the grace does not restart it. Rows
// that are no longer live are forgotten. next is the earliest deadline still
// in the future, zero if none, and the caller owes it a wake-up.
func queueDebut(rows []queueRow, busy bool, now time.Time, debut map[uint64]time.Time) (items []queuedItem, next time.Time) {
	live := make(map[uint64]bool, len(rows))
	items = make([]queuedItem, 0, len(rows))
	for _, r := range rows {
		if !r.live() {
			continue
		}
		live[r.ID] = true
		at, seen := debut[r.ID]
		if !seen {
			at = now
			if !busy {
				at = now.Add(queueDebutGrace)
			}
			debut[r.ID] = at
		}
		if at.After(now) {
			if next.IsZero() || at.Before(next) {
				next = at
			}
			continue
		}
		items = append(items, queuedItem{id: r.ID, text: r.Text, state: r.State})
	}
	for id := range debut {
		if !live[id] {
			delete(debut, id)
		}
	}
	return items, next
}

// projectQueue folds the queue mirror onto the pager's model: the rows the
// reader may see, and the epoch their ids came from.
func (in *interactiveInput) projectQueue() {
	snap, _, _ := in.intrinsics.queue.state()
	rows := readQueue(snap)
	if mark.Enabled() {
		for _, r := range rows {
			mark.Mark("queue.row", "id", r.ID, "state", string(r.State))
		}
	}
	// THE EPOCH TRAVELS WITH THE ROWS. Every queue mutation is a
	// compare-and-set against the generation its ids came from, and the
	// generation that produced THESE rows is the one in THIS snapshot.
	//
	// KEEP THIS ASSIGNMENT UNDER GUARD BY HAND: queueEpoch is written here
	// and read elsewhere, so dropping it compiles, and the projection test
	// (TestQueueProjectionCarriesTheEpoch) covers readQueue, not this line.
	// Losing it once made `x` on a queued row answer "stale (no epoch
	// supplied)".
	epoch, _ := lookupString(snap, "epoch")
	// THE ARIA'S DISPOSITION WHEN THE ROW ARRIVED is what decides the grace,
	// and it is read from the runtime mirror rather than from the reply to our
	// own send: a row put there by somebody else's prompt gets the same answer.
	// An unseeded mirror counts as idle, so a client that does not yet know
	// waits the split second rather than flashing.
	rtSnap, _, _ := in.intrinsics.runtime.state()
	busy := readRuntime(rtSnap).State.Busy()

	now := time.Now()
	in.mu.Lock()
	if in.queueDebut == nil {
		in.queueDebut = map[uint64]time.Time{}
	}
	items, next := queueDebut(rows, busy, now, in.queueDebut)
	in.queueEpoch = epoch
	if in.lt.setTranscriptQueued(items, "") {
		mark.Mark("queue.draw", "rows", len(items))
		in.lt.render()
	}
	if in.debutTimer != nil {
		in.debutTimer.Stop()
		in.debutTimer = nil
	}
	if !next.IsZero() {
		// The wake-up is the whole of the delay: if the row is gone by then
		// the projection finds nothing to show and no frame is spent.
		in.debutTimer = time.AfterFunc(next.Sub(now), in.projectQueue)
	}
	in.mu.Unlock()
}

// forgetQueueDebuts drops the deadlines a subject change invalidates: the new
// aria's ids are its own, and an id that means one message here meant another
// there.
func (in *interactiveInput) forgetQueueDebuts() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.queueDebut = nil
	if in.debutTimer != nil {
		in.debutTimer.Stop()
		in.debutTimer = nil
	}
}
