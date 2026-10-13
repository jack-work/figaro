package cli

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

type intrinsicMirrors struct {
	mu      sync.Mutex
	runtime *formMirror
	queue   *formMirror
	gen     uint64 // fences a dead connection's deltas off a live subject's mirrors
}

func newIntrinsicMirrors() *intrinsicMirrors {
	return &intrinsicMirrors{runtime: &formMirror{}, queue: &formMirror{}}
}

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

type queueRow struct {
	ID     uint64
	Text   string
	Sender string
	State  rpc.QueueState
	Into   uint64
	Turn   uint64
}

// A committed row is already in the transcript: runTurn emits the frame before it
// marks the message committed.
func (r queueRow) live() bool {
	switch r.State {
	case rpc.QueueStateQueued, rpc.QueueStateCommitting:
		return true
	}
	return false
}

// `order` carries FIFO; rows it does not name follow, sorted by id, since a
// departed message is still in the form.
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

// A delta for an intrinsic this build does not follow is ignored, not an error.
func (in *interactiveInput) applyIntrinsicDelta(params json.RawMessage) {
	var d rpc.FormDelta
	if json.Unmarshal(params, &d) != nil {
		return
	}
	if d.Intrinsic == "" {
		return // the board has its own path
	}
	mirror := in.intrinsics.of(d.Intrinsic)
	if mirror == nil {
		return
	}
	switch mirror.apply(d) {
	case formResync:
		go in.resyncIntrinsic(d.Intrinsic)
		return
	case formIncompatible:
		return
	}
	in.onIntrinsicChanged(d.Intrinsic)
}

// resyncIntrinsic is an RPC: never call it on the notify goroutine, which must not
// block on the wire it reads.
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

func (in *interactiveInput) seedIntrinsics() {
	for _, name := range intrinsicNames() {
		go in.resyncIntrinsic(name)
	}
}

// onIntrinsicChanged takes the render lock: a delta lands on the notifier's
// goroutine mid-keystroke.
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

// A row that arrives while the aria is idle is lifted again within a few frames;
// it is drawn only if it outlives the grace.
const queueDebutGrace = 150 * time.Millisecond

// debut carries each row's deadline across calls so a second mutation inside the
// grace does not restart it; the caller owes next a wake-up.
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

func (in *interactiveInput) projectQueue() {
	snap, _, _ := in.intrinsics.queue.state()
	rows := readQueue(snap)
	if mark.Enabled() {
		for _, r := range rows {
			mark.Mark("queue.row", "id", r.ID, "state", string(r.State))
		}
	}
	// Every queue mutation is a compare-and-set against the epoch of the
	// snapshot its rows came from; no test covers this assignment.
	epoch, _ := lookupString(snap, "epoch")
	// Busy comes from the runtime mirror, not our own send's reply; unseeded
	// counts as idle.
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
		in.debutTimer = time.AfterFunc(next.Sub(now), in.projectQueue)
	}
	in.mu.Unlock()
}

// Queue ids are per aria: a subject change invalidates every deadline.
func (in *interactiveInput) forgetQueueDebuts() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.queueDebut = nil
	if in.debutTimer != nil {
		in.debutTimer.Stop()
		in.debutTimer = nil
	}
}
