package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func birthPatch(v string) form.Patch {
	raw, _ := json.Marshal(v)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"birth.mark": raw}, nil)
}

func within(t *testing.T, what string, d time.Duration, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		require.NoError(t, err, what)
	case <-time.After(d):
		t.Fatalf("%s waited on a birth in progress", what)
	}
}

// A birth holds the store's lock shared: another birth, and any open of a
// figaro's head, runs beside it instead of queueing behind its fsyncs.
func TestABirthDoesNotHoldBackAnotherBirthOrAnOpen(t *testing.T) {
	be, id := realAria(t, 6, 64)
	other := mustNewAriaTB(t, be)

	be.store.mu.RLock()
	within(t, "a fork", 5*time.Second, func() error {
		_, _, _, err := be.store.ForkWith(other, 0, birthPatch("beside"))
		return err
	})
	within(t, "an open", 5*time.Second, func() error {
		x, err := be.store.openNode(id)
		if err == nil {
			x.Close()
		}
		return err
	})
	be.store.mu.RUnlock()
}

// A removal still reshapes alone: it waits for the birth in progress.
func TestARemovalWaitsForABirthInProgress(t *testing.T) {
	be, _ := realAria(t, 2, 16)
	victim := mustNewAriaTB(t, be)

	be.store.mu.RLock()
	removed := make(chan error, 1)
	go func() { removed <- be.store.removeLocked(victim, false) }()
	select {
	case err := <-removed:
		be.store.mu.RUnlock()
		t.Fatalf("the removal ran beside a birth in progress (err %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	be.store.mu.RUnlock()
	select {
	case err := <-removed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the removal never ran after the birth ended")
	}
}
