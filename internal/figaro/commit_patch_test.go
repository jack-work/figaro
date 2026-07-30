package figaro_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/jack-work/figaro/internal/tool"
)

// chalkFailBackend fails every chalkboard append. Everything else is real.
type chalkFailBackend struct {
	store.Backend
	fail bool
}

func (b *chalkFailBackend) ApplyChalkboard(id string, p message.Patch) (uint64, error) {
	if b.fail {
		return 0, errors.New("disk on fire")
	}
	return b.Backend.ApplyChalkboard(id, p)
}

// DURABILITY PRECEDES VISIBILITY: a chalkboard write that did not land must not
// be visible in memory.
//
// This is the bug the commitPatch unification fixed. Two call sites advanced the
// board independently and disagreed: the control path bailed on a failed write,
// while the turn path logged the error and applied the patch to memory anyway.
// The agent then ran on state the log did not have — and, worse, the model was
// handed that state as a <system-reminder>, so a restart erased the value but
// not the reply that depended on it. Memory may lag the log; it may never lead.
func TestFailedChalkboardWriteLeavesBoardUntouched(t *testing.T) {
	real, id := backedConvForCommit(t)
	be := &chalkFailBackend{Backend: real}
	cb, _ := chalkboard.Open("")
	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         id,
		Backend:    be,
		Chalkboard: cb,
		Tools:      tool.NewRegistry(),
	})
	defer a.Kill()

	// A write that succeeds is visible, and carries the durable version.
	_, _, err := a.Set(chalkboard.Patch{Set: map[string]json.RawMessage{
		"keep": json.RawMessage(`"first"`),
	}})
	require.NoError(t, err)
	eventually(t, func() bool { _, ok := cb.Snapshot().Get("keep"); return ok }, "first write never became visible")
	goodVersion := cb.Version()
	assert.NotZero(t, goodVersion, "a landed write must record a version")

	// Now the store refuses. The patch must reach neither the board nor the
	// version — the two must stay exactly where the last durable write left them.
	be.fail = true
	_, _, err = a.Set(chalkboard.Patch{Set: map[string]json.RawMessage{
		"ghost": json.RawMessage(`"never"`),
	}})
	require.NoError(t, err) // Set only enqueues; the failure happens on the drain loop

	// Wait for the drain loop to have PROCESSED the failing event, rather than
	// sleeping and hoping. The board must be unchanged when it settles.
	eventually(t, func() bool { return a.Info().State == "idle" }, "agent never went idle")
	_, present := cb.Snapshot().Get("ghost")
	assert.False(t, present, "a patch whose append failed became visible in memory")
	assert.Equal(t, goodVersion, cb.Version(), "a failed append moved the version")
	kept, ok := cb.Snapshot().Get("keep")
	require.True(t, ok, "the last durable value was lost")
	assert.JSONEq(t, `"first"`, string(kept))
}

func backedConvForCommit(t *testing.T) (store.Backend, string) {
	t.Helper()
	b, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	l, err := b.CreateLoadout("d", message.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}})
	require.NoError(t, err)
	conv, err := b.CreateConversation(l)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b, conv
}

// eventually polls until cond holds or the deadline passes.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}
