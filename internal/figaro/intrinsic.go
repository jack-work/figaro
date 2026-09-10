package figaro

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// AN INTRINSIC FORM is a form the harness publishes as PART OF a host rather
// than alongside it: `<host>/queue`, `<host>/runtime`. It is derived, it is
// never persisted, and it dies with its host.
//
// "Intrinsic" is the load-bearing word: these are not attached to an aria, they
// are properties OF one, the way a turn's disposition is a property of the turn
// rather than a document about it. Nothing mints them, nothing forks them, and
// no `figaro set` reaches them.
//
// The nearest neighbour in this tree is store.Libretto, which is also a derived
// form with exactly one source -- and which is refcounted, stumped and DURABLE.
// The difference is the whole distinction:
//
//	A LIBRETTO IS WRITTEN DOWN. AN INTRINSIC FORM IS ONLY EVER LIVE.
//
// `state` is the reserved IDENTITY segment: it names the host's own form and
// is implied, so `fig form show <id>` and `fig form show <id>/state` are the
// same address. An intrinsic form may not be called `state`, and the empty
// string on the wire means `state` -- which is what every form delta has always
// meant, so the wire field is backward compatible by the same rule that makes
// the shorthand work.
const (
	// IntrinsicState is the identity segment: the host's own form. It is NOT a
	// intrinsic; it is the name of what a intrinsic hangs from.
	IntrinsicState = "state"

	// IntrinsicRuntime carries the aria's turn disposition: what it is doing
	// right now, as opposed to what it has said.
	IntrinsicRuntime = "runtime"

	// IntrinsicQueue carries the messages accepted but not yet answered, and
	// the state each is in as it travels from a client to an inquiry.
	IntrinsicQueue = "queue"
)

// intrinsic is one such form plus the fanout that publishes it. The form is
// over a MemFormLog: paged, never durable, dead when the agent is.
type intrinsic struct {
	name string
	form *store.Form

	// mu serializes PROJECTION, not the form. The form has its own actor and
	// is safe to submit to from anywhere; what needs ordering is the
	// read-modify-write of "here is the whole desired state, diff it against
	// what is there". Two projectors racing would each diff against a
	// snapshot the other had already moved past, and the loser's patch would
	// re-assert stale keys.
	mu sync.Mutex
}

func newIntrinsic(name string) *intrinsic {
	return &intrinsic{name: name, form: store.NewMemForm()}
}

// publish states what the intrinsic should hold and lets the form work out the
// difference. THE DIFF IS THE POINT: form.Build diffs against the current
// snapshot, so republishing unchanged state yields an identity patch, which
// the form drops and no delta goes out. Callers therefore never have to know
// whether anything actually moved -- they say what is true and the algebra
// decides whether that is news.
func (c *intrinsic) publish(set map[string]any, remove []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	raw := make(map[string]json.RawMessage, len(set))
	for k, v := range set {
		b, err := json.Marshal(v)
		if err != nil {
			slog.Warn("intrinsic: value will not marshal", "intrinsic", c.name, "key", k, "err", err)
			continue
		}
		raw[k] = b
	}
	snap, version := c.form.Snapshot()
	patch := form.Build(snap, raw, remove)
	if patch.IsIdentity() {
		return
	}
	// PRIVILEGED, BECAUSE A INTRINSIC FORM HAS NO OTHER WRITER. The protection
	// catalog (api/form.CheckWritable) exists to stop a HUMAN typing `figaro
	// set model=...` into a board the harness owns. A intrinsic has no
	// user-writable path at all -- it is not bound, not settable, and every
	// key in it is harness-written by construction -- so the check has nothing
	// to protect here and refusing is the only thing it can do.
	//
	// MEASURED, and it is why this comment is long: publishing `model`
	// through the unprivileged path made the WHOLE runtime intrinsic empty.
	// Every publish was refused, every refusal was a slog.Warn nobody reads,
	// and `fig form show <id>/runtime` answered `{}` -- a dead feature that
	// looked exactly like a feature with nothing to say yet.
	if _, _, err := c.form.ApplyEffectPrivileged(patch, version); err != nil {
		// LOUD. A intrinsic that cannot write is not degraded, it is ABSENT,
		// and the status bar it feeds silently reverts to guessing.
		slog.Error("intrinsic: publish refused; this intrinsic is now stale and the "+
			"client reading it will see stale or missing state",
			"intrinsic", c.name, "err", err)
	}
}

// snapshot is the whole of what the intrinsic holds, for a seed read.
func (c *intrinsic) Snapshot() (form.Snapshot, uint64) {
	if c == nil {
		return form.Snapshot{}, 0
	}
	return c.form.Snapshot()
}

func (c *intrinsic) close() {
	if c != nil && c.form != nil {
		c.form.Close()
	}
}

// openIntrinsics mints the agent's intrinsic forms and wires each to the aria's own
// fanout. They ride the SAME socket and the SAME notification as the board's
// deltas, distinguished only by the Intrinsic field -- so a client that already
// follows form deltas needs one switch, not a second transport.
func (a *Agent) openIntrinsics() {
	a.runtime = newIntrinsic(IntrinsicRuntime)
	a.queue = newIntrinsic(IntrinsicQueue)
	for _, c := range []*intrinsic{a.runtime, a.queue} {
		name := c.name
		c.form.OnCommit(func(version uint64, patch message.Patch) {
			a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodFormDelta,
				Params: rpc.FormDelta{
					Schema: rpc.FormDeltaSchema, AriaID: a.id, Intrinsic: name,
					Version: version, Patch: patch, At: time.Now().UnixMilli(),
				}})
		})
	}
	a.teardown = append(a.teardown, func() {
		a.runtime.close()
		a.queue.close()
	})
	// The inbox is the ONE publisher of the queue intrinsic. Every enqueue,
	// mutation, lift, commit, coalesce and drain already passes through it; a
	// publisher anywhere else can disagree with the queue's own state, and
	// then there are two answers to "what is queued" and no way to tell which
	// is the queue.
	a.inbox.OnChange(func(snap InboxSnapshot) { a.publishQueue(snap) })
	a.publishRuntime(rpc.RuntimeIdle, "")
	a.publishQueue(a.inbox.Project())
}

// Intrinsic returns a named intrinsic's form, or nil. `state` is not one: it is
// the host's own form, and the caller already has that.
func (a *Agent) Intrinsic(name string) *store.Form {
	switch name {
	case IntrinsicRuntime:
		if a.runtime != nil {
			return a.runtime.form
		}
	case IntrinsicQueue:
		if a.queue != nil {
			return a.queue.form
		}
	}
	return nil
}

// publishRuntime states the aria's turn disposition. Called from the turn
// loop's own goroutine at the points that already move turnRunning, so the
// intrinsic is a PUBLISHED VIEW of state the agent already holds rather than
// bookkeeping of its own.
//
// reason is the verdict of the turn that just ended, and is carried only into
// idle; every other state clears it, because a reason attached to a running
// turn describes the previous one and reads as if it described this one.
func (a *Agent) publishRuntime(state rpc.RuntimeState, reason string) {
	if a.runtime == nil {
		return
	}
	set := map[string]any{
		"turn":       string(state),
		"turn.id":    a.turnID,
		"turn.since": time.Now().UnixMilli(),
		"inflight":   a.inbox.InflightCount(),
		"epoch":      a.inbox.Epoch(),
	}
	var remove []string
	if reason != "" {
		set["turn.reason"] = reason
	} else {
		remove = []string{"turn.reason"}
	}
	if m := a.currentModel(); m != "" {
		set["model"] = m
	}
	a.runtime.publish(set, remove)
}

// publishQueue projects the inbox onto the queue intrinsic.
//
// The keys are FLAT AND DOTTED because that is the form's native shape: a
// change to one message's state is one key, so the patch is tens of bytes and
// the tree view renders it without being taught anything. `order` carries FIFO
// because a form is a map and a map has none.
func (a *Agent) publishQueue(snap InboxSnapshot) {
	if a.queue == nil {
		return
	}
	set := map[string]any{
		"epoch": snap.Epoch,
		"len":   len(snap.Items),
		"order": snap.Order,
	}
	for _, it := range snap.Items {
		p := "items." + itoa(it.ID) + "."
		set[p+"state"] = string(it.State)
		set[p+"at"] = it.At
		if it.Text != "" {
			set[p+"text"] = it.Text
		}
		if it.Sender != "" {
			set[p+"sender"] = it.Sender
		}
		if len(it.Merged) > 0 {
			set[p+"merged"] = it.Merged
		}
		if it.Into != 0 {
			set[p+"into"] = it.Into
		}
		if it.Turn != 0 {
			set[p+"turn"] = it.Turn
		}
	}
	// Whatever the projection no longer names must GO, or a message that left
	// the queue would sit in the form forever. The projection is the whole
	// truth every time it runs; this is what makes that true of the form too.
	snapNow, _ := a.queue.Snapshot()
	live := map[string]bool{}
	for _, it := range snap.Items {
		live[itoa(it.ID)] = true
	}
	var remove []string
	for key := range snapNow.All() {
		id, ok := queueItemID(key)
		if !ok || live[id] {
			continue
		}
		remove = append(remove, key)
	}
	a.queue.publish(set, remove)
}

// queueItemID pulls the id out of an "items.<id>.<field>" key.
func queueItemID(key string) (string, bool) {
	const prefix = "items."
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return "", false
	}
	rest := key[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			return rest[:i], true
		}
	}
	return "", false
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
