package store

// A tombstone is STATE, not an event in memory.
//
// A derived form that must be rebuildable from the log cannot learn about a
// deletion nobody wrote down, and a subscriber that was offline when one
// happened has nothing to catch up to. So a dying form's last act is an
// ordinary patch on its own channel, which every subscriber hears through
// the mechanism it already uses, and which a replay reproduces exactly.
//
// After it, the form is SEALED: further writes are refused. The record is
// the truth and there is nothing left to say.

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/message"
)

// TombstoneKey marks a form as dead. Harness-owned: nothing off a wire may
// write or clear it.
const TombstoneKey = "system.tombstone"

// Tombstone marks the form dead, durably, and seals it. Idempotent: a form
// already tombstoned reports its existing version rather than failing, so a
// delete that is retried after a crash does not have to know whether it got
// there the first time.
func (f *Form) Tombstone(reason string) (uint64, error) {
	if snap, v := f.Snapshot(); snap.Has(TombstoneKey) {
		return v, nil
	}
	if reason == "" {
		reason = "deleted"
	}
	raw, err := json.Marshal(reason)
	if err != nil {
		return 0, err
	}
	v, _, err := f.ApplyEffectPrivileged(
		message.Patch{Set: map[string]json.RawMessage{TombstoneKey: raw}}, 0)
	if err != nil {
		return 0, err
	}
	f.sealed.Store(true)
	return v, nil
}

// Tombstoned reports whether this form has been marked dead. Read from the
// published state, so it survives a restart without anyone re-declaring it.
func (f *Form) Tombstoned() bool {
	snap, _ := f.Snapshot()
	return snap.Has(TombstoneKey)
}

// errSealed is what a write to a dead form gets.
var errSealed = fmt.Errorf("form is tombstoned: no further patches")

// Reclaimable reports whether this form is dead and nobody is still reading
// it: the condition a delete waits on before unlinking anything.
//
// THE LEASE REGISTRY IS THE SUBSCRIBER SET, and for a single process that is
// not a simplification but the whole of it. A durable refcount cannot tell
// "still reading" from "died holding a reference"; an in-memory set answers
// both, because every holder dies when the process does and a restart is a
// clean sweep rather than a TTL to wait out.
//
// A TTL only covers a holder that is alive but silent, which today is nobody
// and later is a node on another machine. When that exists, this becomes
// {id, holder, expires} and the sweep drops the stale; nothing above it
// changes.
func (f *Form) Reclaimable() bool {
	if !f.Tombstoned() {
		return false
	}
	return len(*f.subs.Load()) == 0
}
