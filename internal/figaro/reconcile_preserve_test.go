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
	// Seed ariaSrv with three committed units — imagine a healthy aria.
	for lt, role := range []string{"user", "assistant", "user"} {
		a.ariaSrv.Commit(aria.Message{
			LT:    lt + 1,
			Role:  role,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "hi"}},
		})
	}
	a.unitLT = 3

	// Sanity: three committed, none open.
	require.Equal(t, 3, a.ariaSrv.LastCommittedLT())
	require.False(t, a.ariaSrv.HasOpen())

	// figLog is empty -> compose.Units returns nothing. Prior behavior:
	// Restore([]) would wipe closed to zero units. Fix: state preserved.
	a.reconcileAriaServer()

	assert.Equal(t, 3, a.ariaSrv.LastCommittedLT(),
		"reconcileAriaServer must not shrink closed history on empty figLog read")

	got := a.ariaSrv.Read(0)
	assert.Len(t, got.Committed, 3,
		"Read(0) after reconcile must still return the three original units")
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

	// Ensure the log actually yields two units under compose.
	units := compose.Units(unwrapMessages(logMem.Read()), nil, nil)
	require.Len(t, units, 2)

	a := newBareAgent(t, logMem)
	a.id = "recon-002"
	// Seed one committed — history (2) > current (1), Restore should run.
	a.ariaSrv.Commit(aria.Message{LT: 1, Role: "user", Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "hello"}}})
	a.unitLT = 1

	a.reconcileAriaServer()
	assert.Equal(t, 2, a.ariaSrv.LastCommittedLT(), "reconcile with growing history should refresh")
}
