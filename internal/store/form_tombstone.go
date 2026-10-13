package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/form"
)

// TombstoneKey is harness-owned: nothing off a wire may write or clear it.
const TombstoneKey = "system.tombstone"

var ErrFormClosed = errors.New("form is closed")

// ErrFormMoved is the stale-ifVersion refusal: retry, the write itself is not wrong.
var ErrFormMoved = errors.New("form moved")

// Tombstone marks the form dead durably and seals it. Idempotent, so a delete
// retried after a crash need not know whether it got there the first time.
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
		form.Build(form.Snapshot{}, map[string]json.RawMessage{TombstoneKey: raw}, nil), 0)
	if err != nil {
		return 0, err
	}
	f.sealed.Store(true)
	return v, nil
}

func (f *Form) Tombstoned() bool {
	snap, _ := f.Snapshot()
	return snap.Has(TombstoneKey)
}

var errSealed = fmt.Errorf("form is tombstoned: no further patches")

func (f *Form) Subscribed() bool { return len(*f.subs.Load()) > 0 }

// Reclaimable: dead and unread, the condition a delete waits on before unlinking.
func (f *Form) Reclaimable() bool {
	if !f.Tombstoned() {
		return false
	}
	return len(*f.subs.Load()) == 0
}
