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
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
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

// live reports whether the row belongs in the drawer at all. A message that
// has been answered, dropped or drained is history, and the drawer is a list
// of things that are still happening.
func (r queueRow) live() bool {
	switch r.State {
	case rpc.QueueStateQueued, rpc.QueueStateCommitting, rpc.QueueStateCommitted:
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
		snap, _, _ := in.intrinsics.queue.state()
		rows := readQueue(snap)
		items := make([]queuedItem, 0, len(rows))
		for _, r := range rows {
			if !r.live() {
				continue
			}
			// A COMMITTED MESSAGE IS HELD IN THE DRAWER UNTIL THE TRANSCRIPT
			// HAS IT. That is the whole of "held until it is roundtripped":
			// the daemon says which turn the message became, and the client
			// drops the row when its OWN window has adopted that turn -- a
			// fact it knows, rather than a timer it would have to guess.
			if r.State == rpc.QueueStateCommitted && in.hasTurn(r.Turn) {
				continue
			}
			items = append(items, queuedItem{id: r.ID, text: r.Text, state: r.State})
		}
		in.mu.Lock()
		if in.lt.setTranscriptQueued(items, "") {
			in.lt.render()
		}
		in.mu.Unlock()
	case intrinsicRuntime:
		snap, _, _ := in.intrinsics.runtime.state()
		rt := readRuntime(snap)
		in.mu.Lock()
		if in.lt.status.setRuntime(rt) {
			in.lt.render()
		}
		in.mu.Unlock()
	}
}

// hasTurn reports whether this client's window already carries a turn, which
// is how a committed queue row knows it has been superseded by the real thing.
func (in *interactiveInput) hasTurn(turn uint64) bool {
	if turn == 0 {
		return false
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.lt == nil || in.lt.client == nil {
		return false
	}
	_, ok := in.lt.client.InquiryOf(int(turn))
	return ok
}

var _ = sdk.DialAria // the mirrors live beside the client that feeds them
