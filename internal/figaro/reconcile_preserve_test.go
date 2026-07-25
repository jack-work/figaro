package figaro

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type reconcileNoopProvider struct{}

func (reconcileNoopProvider) Name() string        { return "noop" }
func (reconcileNoopProvider) Fingerprint() string { return "noop/v0" }
func (reconcileNoopProvider) SetModel(string)     {}
func (reconcileNoopProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (reconcileNoopProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	return nil
}

func newBareAgent(t *testing.T, log store.Log[message.Message]) *Agent {
	t.Helper()
	cb, _ := chalkboard.Open("")
	return &Agent{
		id:         "recon-test",
		prov:       reconcileNoopProvider{},
		chalkboard: cb,
		figLog:     log,
		ariaSrv:    aria.NewServer(),
		inbox:      NewInbox(context.Background()),
	}
}

// TestReconcileAriaServer_PreservesStateOnShorterHistory pins the fix
// for the interrupted-mid-turn bug: when reconcileAriaServer runs on a
// broken/empty figLog it must NOT wipe already-committed ariaSrv state.
// Without this guard, a mid-turn error path that reads back fewer units
// than the ariaSrv already holds (because Open failed silently, cachedLog
// was constructed on a stale head/fork ancestry, or the log was replaced
// with an empty ephemeral fallback) causes Read to return 0 units and
// the live-subscribe stream to go silent. Cleanly-idle arias are
// unaffected because they never enter this code path — matches the
// reported symptom.
func TestReconcileAriaServer_PreservesStateOnShorterHistory(t *testing.T) {
	a := newBareAgent(t, store.NewMemLog[message.Message]()) // empty log
	a.id = "recon-001"
	// Seed ariaSrv with three sealed turns — imagine a healthy aria.
	for i := uint64(1); i <= 3; i++ {
		a.ariaSrv.Commit(aria.Turn{
			ID:     i,
			Sealed: true,
			Nodes:  []livedoc.Node{{Type: livedoc.NodeProse, Role: "user", Markdown: "hi"}},
		})
	}

	require.Equal(t, uint64(3), a.ariaSrv.LastTurn())
	require.False(t, a.ariaSrv.HasOpen())

	// figLog is empty -> compose.Turns returns nothing. Prior behavior:
	// Restore(nil) would wipe history. Fix: state preserved.
	a.reconcileAriaServer()

	assert.Equal(t, uint64(3), a.ariaSrv.LastTurn(),
		"reconcileAriaServer must not shrink history on an empty figLog read")

	got := a.ariaSrv.Read(aria.Anchor{}, 1<<20)
	assert.Len(t, got.Parts, 3,
		"a read after reconcile must still return the three original turns")
}

// TestReconcileAriaServer_AllowsGrow verifies the guard doesn't block
// the intended path: when history >= current closed, Restore proceeds
// normally and the ariaSrv gets refreshed.
func TestReconcileAriaServer_AllowsGrow(t *testing.T) {
	logMem := store.NewMemLog[message.Message]()
	// Two durable messages that produce two compose.Units.
	_, err := logMem.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")},
	}})
	require.NoError(t, err)
	_, err = logMem.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("world")},
	}})
	require.NoError(t, err)

	// Ensure the log actually yields one turn under compose (a prompt and its
	// reply are ONE exchange now, not two units).
	turns := compose.Turns(unwrapMessages(logMem.Read()), nil, nil)
	require.Len(t, turns, 1)

	a := newBareAgent(t, logMem)
	a.id = "recon-002"
	a.reconcileAriaServer()
	assert.Equal(t, uint64(1), a.ariaSrv.LastTurn(),
		"reconcile against a readable log should adopt its turns")
	assert.Len(t, a.ariaSrv.Turns()[0].Nodes, 2,
		"the turn holds the prompt and the reply as one exchange")
}
