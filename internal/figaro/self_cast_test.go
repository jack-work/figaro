package figaro_test

// THE SELF-CAST DEADLOCK, which the notes have carried since session 1:
// "fig cast on your own aria from inside a turn hangs, because the cast
// rides the inbox and the inbox is running the turn that issued it."
//
// It is the same bug as the displaced tool_result from the other end: one
// hangs because it NEEDS the loop, one corrupted because it went AROUND the
// loop. durable-forms' phase 9 says making study an ordinary patch on a
// separate node should fix both, and that it should be CHECKED against both.
// This is that check.

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

	roleID, _, err := backend.CreateForm("", form.Patch{
		Set: map[string]json.RawMessage{"name": json.RawMessage(`"the role"`)},
	})
	require.NoError(t, err)

	submitPrompt(a, "a long turn")
	awaitEntered(t, entered) // the loop is now busy running the turn

	// A cast issued while the loop is occupied -- which is exactly what a
	// tool call doing `fig cast` produces. It must NOT wait for the turn.
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

	// And it really cast: the role points here and the board studies it.
	snap, err := backend.FormState(roleID)
	require.NoError(t, err)
	target, ok := snap.Get("target-aria")
	require.True(t, ok, "the role was not pointed at the caster")
	require.Contains(t, a.StudyList(), roleID, "the caster does not study the role")
	_ = target

	openGate(gate, 1)
	awaitTurnDone(t, ch)
}

// Cast lost its actor-loop serialization, so the case it protected must hold
// without it: EIGHT CASTS OF ONE FIGARO AT ONCE, into eight different roles.
//
// Each cast does a version-guarded read-modify-write of the same board (the
// study set) and a patch on its own role. If the retry were wrong, studies
// would be lost silently -- the board would name fewer roles than were cast,
// and each missing one is a role pointing at a figaro that does not know it.
func TestConcurrentCastsOfOneFigaro(t *testing.T) {
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, _ := fuzzAgent(t, prov, nil)

	const casts = 8
	roles := make([]string, casts)
	for i := range roles {
		id, _, err := backend.CreateForm("", form.Patch{
			Set: map[string]json.RawMessage{"name": json.RawMessage(`"role"`)},
		})
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

	// The refcounts must agree with the board, which is what the sweep checks.
	if rec, ok := backend.(interface {
		ReconcileLibrettos() (store.LibrettoAudit, error)
	}); ok {
		audit, err := rec.ReconcileLibrettos()
		require.NoError(t, err)
		require.Zero(t, audit.Corrected, "the sweep disagreed after concurrent casts: %+v", audit)
	}
}
