package store

import (
	"encoding/json"
	"errors"

	"github.com/jack-work/figaro/api/form"
)

const TombstoneKey = form.TombstoneKey

var ErrFormClosed = errors.New("form is closed")

var ErrFormUnavailable = errors.New("form unavailable")

var ErrSeqConditional = errors.New("a write with a client seq may not carry Assert or IfVersion: its refusal is not durable, so a resend after a restart could be judged against a different form")

// ErrFormMoved is the stale-ifVersion refusal: retry, the write itself is not wrong.
var ErrFormMoved = errors.New("form moved")

// Tombstone marks the form dead durably and seals it. Idempotent, so a delete
// retried after a crash need not know whether it got there the first time.
func (f *Form) Tombstone(reason string) (uint64, error) {
	if snap, v := f.Snapshot(); form.RuntimeHas(snap, TombstoneKey) {
		return v, nil
	}
	if reason == "" {
		reason = "deleted"
	}
	raw, err := json.Marshal(reason)
	if err != nil {
		return 0, err
	}
	snap, _ := f.Snapshot()
	v, _, err := f.ApplyEffectPrivileged(
		form.RuntimeSet(snap, map[string]json.RawMessage{TombstoneKey: raw}), 0)
	if err != nil {
		return 0, err
	}
	f.sealed.Store(true)
	return v, nil
}

func (f *Form) Tombstoned() bool {
	snap, _ := f.Snapshot()
	return form.RuntimeHas(snap, TombstoneKey)
}

var ErrFormSealed = errors.New("form is tombstoned: no further patches")

var errSealed = ErrFormSealed

func (f *Form) Subscribed() bool { return len(*f.subs.Load()) > 0 }

// Reclaimable: dead and unread, the condition a delete waits on before unlinking.
func (f *Form) Reclaimable() bool {
	if !f.Tombstoned() {
		return false
	}
	return len(*f.subs.Load()) == 0
}
