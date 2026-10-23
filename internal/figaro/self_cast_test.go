package figaro_test

// A cast from inside this aria's own turn must not wait on the loop running that turn.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCastDuringOwnTurnDoesNotDeadlock(t *testing.T) {
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, _ := fuzzAgent(t, prov, nil)
	ch, unsub := subscribeChan(a)
	defer unsub()

	roleID, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"the role"`)}, nil))
	require.NoError(t, err)

	submitPrompt(a, "a long turn")
	awaitEntered(t, entered) // the loop is now busy running the turn

	// A cast issued while the loop is occupied must not wait for the turn.
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, err := a.Cast(ctx, roleID, nil)
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "cast during an in-flight turn failed")
	case <-time.After(4 * time.Second):
		t.Fatal("cast blocked on a turn that had not finished: the self-cast deadlock")
	}

	snap, err := backend.FormState(roleID)
	require.NoError(t, err)
	target, ok := snap.Get("target-aria")
	require.True(t, ok, "the role was not pointed at the caster")
	require.Contains(t, a.StudyList(), roleID, "the caster does not study the role")
	_ = target

	openGate(gate, 1)
	awaitTurnDone(t, ch)
}

// Eight concurrent casts of one figaro: each is a version-guarded
// read-modify-write of the study set, and a wrong retry loses studies silently.
func TestConcurrentCastsOfOneFigaro(t *testing.T) {
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, _ := fuzzAgent(t, prov, nil)

	const casts = 8
	roles := make([]string, casts)
	for i := range roles {
		id, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"role"`)}, nil))
		require.NoError(t, err)
		roles[i] = id
	}

	errs := make(chan error, casts)
	for _, roleID := range roles {
		go func(roleID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, err := a.Cast(ctx, roleID, nil)
			errs <- err
		}(roleID)
	}
	for range roles {
		require.NoError(t, <-errs)
	}

	studies := a.StudyList()
	for _, roleID := range roles {
		require.Contains(t, studies, roleID,
			"a concurrent cast lost its study: the role points at a figaro that does not know it")
		snap, err := backend.FormState(roleID)
		require.NoError(t, err)
		_, ok := snap.Get("target-aria")
		require.True(t, ok, "a concurrent cast left a role unpointed")
	}
	require.Len(t, studies, casts, "the study set gained or lost entries: %v", studies)

	// The refcounts must agree with the bound form.
	if rec, ok := backend.(interface {
		ReconcileLibrettos() (store.LibrettoAudit, error)
	}); ok {
		audit, err := rec.ReconcileLibrettos()
		require.NoError(t, err)
		require.Zero(t, audit.Corrected, "the sweep disagreed after concurrent casts: %+v", audit)
	}
}
