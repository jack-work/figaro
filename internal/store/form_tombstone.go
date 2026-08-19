package store

// A tombstone is STATE, not an event in memory.

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/internal/message"
)

// TombstoneKey marks a form as dead. Harness-owned: nothing off a wire may
// write or clear it.
const TombstoneKey = "system.tombstone"

// ErrFormClosed is what a write to a Form whose writer has gone gets.
var ErrFormClosed = errors.New("form is closed")

// ErrFormMoved is the stale-ifVersion refusal, as a sentinel: a caller doing
// read-modify-write on a form needs to tell "somebody else got there first,
// retry" apart from "this write is wrong", and a string never let it.
var ErrFormMoved = errors.New("form moved")

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
func (f *Form) Subscribed() bool { return len(*f.subs.Load()) > 0 }

func (f *Form) Reclaimable() bool {
	if !f.Tombstoned() {
		return false
	}
	return len(*f.subs.Load()) == 0
}
