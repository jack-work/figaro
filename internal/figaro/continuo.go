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

// A CONTINUO is a non-persistent builtin form bound to a host form, published
// by the harness and addressed as a named segment of its host: `<host>/queue`,
// `<host>/runtime`.
//
// The word is basso continuo: the part that sounds continuously beneath the
// work, REALIZED LIVE FROM FIGURES AND NEVER WRITTEN OUT IN FULL, and
// meaningless apart from the piece it accompanies. Continuous, derived,
// ephemeral, bound.
//
// It is the ephemeral cousin of store.Libretto, which is a derived form with
// exactly one source -- refcounted, stumped and DURABLE. The difference that
// matters is persistence, and it is worth stating as a slogan because it is
// the whole distinction:
//
//	THE LIBRETTO IS WRITTEN DOWN. THE CONTINUO IS REALIZED IN PERFORMANCE.
//
// `state` is the reserved IDENTITY segment: it names the host's own form and
// is implied, so `fig form show <id>` and `fig form show <id>/state` are the
// same address. A continuo may not be called `state`, and the empty string on
// the wire means `state` -- which is what every form delta has always meant,
// so the wire field is backward compatible by the same rule that makes the
// shorthand work.
const (
	// ContinuoState is the identity segment: the host's own form. It is NOT a
	// continuo; it is the name of what a continuo hangs from.
	ContinuoState = "state"

	// ContinuoRuntime carries the aria's turn disposition: what it is doing
	// right now, as opposed to what it has said.
	ContinuoRuntime = "runtime"

	// ContinuoQueue carries the messages accepted but not yet answered, and
	// the state each is in as it travels from a client to an inquiry.
	ContinuoQueue = "queue"
)

// continuo is one such form plus the fanout that publishes it. The form is
// over a MemFormLog: paged, never durable, dead when the agent is.
type continuo struct {
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

func newContinuo(name string) *continuo {
	return &continuo{name: name, form: store.NewMemForm()}
}

// publish states what the continuo should hold and lets the form work out the
// difference. THE DIFF IS THE POINT: form.Build diffs against the current
// snapshot, so republishing unchanged state yields an identity patch, which
// the form drops and no delta goes out. Callers therefore never have to know
// whether anything actually moved -- they say what is true and the algebra
// decides whether that is news.
func (c *continuo) publish(set map[string]any, remove []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	raw := make(map[string]json.RawMessage, len(set))
	for k, v := range set {
		b, err := json.Marshal(v)
		if err != nil {
			slog.Warn("continuo: value will not marshal", "continuo", c.name, "key", k, "err", err)
			continue
		}
		raw[k] = b
	}
	snap, version := c.form.Snapshot()
	patch := form.Build(snap, raw, remove)
	if patch.IsIdentity() {
		return
	}
	if _, err := c.form.Apply(patch, version); err != nil {
		slog.Warn("continuo: apply", "continuo", c.name, "err", err)
	}
}

// snapshot is the whole of what the continuo holds, for a seed read.
func (c *continuo) Snapshot() (form.Snapshot, uint64) {
	if c == nil {
		return form.Snapshot{}, 0
	}
	return c.form.Snapshot()
}

func (c *continuo) close() {
	if c != nil && c.form != nil {
		c.form.Close()
	}
}

// openContinuos mints the agent's continuos and wires each to the aria's own
// fanout. They ride the SAME socket and the SAME notification as the board's
// deltas, distinguished only by the Continuo field -- so a client that already
// follows form deltas needs one switch, not a second transport.
func (a *Agent) openContinuos() {
	a.runtime = newContinuo(ContinuoRuntime)
	a.queue = newContinuo(ContinuoQueue)
	for _, c := range []*continuo{a.runtime, a.queue} {
		name := c.name
		c.form.OnCommit(func(version uint64, patch message.Patch) {
			a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodFormDelta,
				Params: rpc.FormDelta{
					Schema: rpc.FormDeltaSchema, AriaID: a.id, Continuo: name,
					Version: version, Patch: patch, At: time.Now().UnixMilli(),
				}})
		})
	}
	a.teardown = append(a.teardown, func() {
		a.runtime.close()
		a.queue.close()
	})
	// The inbox is the ONE publisher of the queue continuo. Every enqueue,
	// mutation, lift, commit, coalesce and drain already passes through it; a
	// publisher anywhere else can disagree with the queue's own state, and
	// then there are two answers to "what is queued" and no way to tell which
	// is the queue.
	a.inbox.OnChange(func(snap InboxSnapshot) { a.publishQueue(snap) })
	a.publishRuntime(rpc.RuntimeIdle, "")
	a.publishQueue(a.inbox.Project())
}

// Continuo returns a named continuo's form, or nil. `state` is not one: it is
// the host's own form, and the caller already has that.
func (a *Agent) Continuo(name string) *store.Form {
	switch name {
	case ContinuoRuntime:
		if a.runtime != nil {
			return a.runtime.form
		}
	case ContinuoQueue:
		if a.queue != nil {
			return a.queue.form
		}
	}
	return nil
}

// publishRuntime states the aria's turn disposition. Called from the turn
// loop's own goroutine at the points that already move turnRunning, so the
// continuo is a PUBLISHED VIEW of state the agent already holds rather than
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

// publishQueue projects the inbox onto the queue continuo.
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
