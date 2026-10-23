package figaro

import (
	"encoding/json"
	"github.com/jack-work/figaro/internal/mark"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// An intrinsic form is derived, never persisted, and dies with its host:
// `<host>/queue`, `<host>/runtime`. Nothing mints, forks, or sets one.
//
// `state` is the reserved identity segment naming the host's own form; the
// empty string on the wire means `state`.
const (
	// IntrinsicState names the host's own form; it is not an intrinsic.
	IntrinsicState = "state"

	// IntrinsicRuntime carries the aria's turn disposition.
	IntrinsicRuntime = "runtime"

	// IntrinsicQueue carries the messages accepted but not yet answered.
	IntrinsicQueue = "queue"
)

// intrinsic is one such form plus the fanout that publishes it.
type intrinsic struct {
	name        string
	form        *store.Form
	incarnation uint64

	// mu serializes projection, not the form: two projectors racing would each
	// diff against a snapshot the other had moved past.
	mu sync.Mutex
}

func newIntrinsic(name string) *intrinsic {
	return &intrinsic{name: name, form: store.NewMemForm(), incarnation: nextIncarnation()}
}

var lastIncarnation atomic.Uint64

func nextIncarnation() uint64 {
	for {
		last := lastIncarnation.Load()
		next := max(uint64(time.Now().UnixNano()), last+1)
		if lastIncarnation.CompareAndSwap(last, next) {
			return next
		}
	}
}

// publish states the desired whole; form.Build diffs it, so an unchanged
// state yields an identity patch and no delta goes out.
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
	// Privileged: an intrinsic has no user-writable path, so the protection
	// catalog has nothing to protect here and would refuse every publish.
	if _, _, err := c.form.ApplyEffectPrivileged(patch, version); err != nil {
		// An intrinsic that cannot write is absent, not degraded.
		slog.Error("intrinsic: publish refused; this intrinsic is now stale and the "+
			"client reading it will see stale or missing state",
			"intrinsic", c.name, "err", err)
	}
}

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

// openIntrinsics wires each intrinsic to the aria's fanout: same socket and
// notification as the bound form's deltas, distinguished by the Intrinsic field.
func (a *Agent) openIntrinsics() {
	a.runtime = newIntrinsic(IntrinsicRuntime)
	a.queue = newIntrinsic(IntrinsicQueue)
	for _, c := range []*intrinsic{a.runtime, a.queue} {
		name, incarnation := c.name, c.incarnation
		c.form.OnCommit(func(version uint64, patch message.Patch) {
			a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodFormDelta,
				Params: rpc.FormDelta{
					Schema: rpc.FormDeltaSchema, AriaID: a.id, Intrinsic: name,
					Incarnation: incarnation, Version: version, Patch: patch,
					At: time.Now().UnixMilli(),
				}})
		})
	}
	a.teardown = append(a.teardown, func() {
		a.runtime.close()
		a.queue.close()
	})
	// The inbox is the one publisher of the queue intrinsic.
	a.inbox.OnChange(func(snap InboxSnapshot) { a.publishQueue(snap) })
	a.publishRuntime(rpc.RuntimeIdle, "")
	a.publishQueue(a.inbox.Project())
}

// Intrinsic returns a named intrinsic's form, or nil; `state` is not one.
func (a *Agent) Intrinsic(name string) *store.Form {
	if c := a.intrinsic(name); c != nil {
		return c.form
	}
	return nil
}

func (a *Agent) intrinsic(name string) *intrinsic {
	switch name {
	case IntrinsicRuntime:
		return a.runtime
	case IntrinsicQueue:
		return a.queue
	}
	return nil
}

// publishRuntime runs on the turn loop's goroutine. reason is carried only
// into idle; on any other state it would describe the previous turn.
func (a *Agent) publishRuntime(state rpc.RuntimeState, reason string) {
	if a.runtime == nil {
		return
	}
	// `turn.state`, not `turn`: a key may not be both a leaf and a branch
	// prefix, and a scalar written to a branch is silently dropped by form.Build.
	mark.Mark("runtime", "aria", a.id, "state", string(state))
	set := map[string]any{
		"turn.state": string(state),
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

// publishQueue projects the inbox onto flat dotted keys; `order` carries FIFO
// because a map has none.
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
	// Remove whatever the projection no longer names, or a departed message sits in the form forever.
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
