package figaro

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
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
	cb, _ := form.Open("")
	a := &Agent{
		id:      "recon-test",
		form:    cb,
		figLog:  log,
		ariaSrv: aria.NewServer(),
		inbox:   NewInbox(context.Background()),
		// The engine may not import internal/compose; a test binary may.
		proj: uiir.New(nil),
	}
	a.bindProvider(reconcileNoopProvider{})
	return a
}

// reconcileAriaServer on a broken or empty figLog must not wipe committed state.
func TestReconcileAriaServer_PreservesStateOnShorterHistory(t *testing.T) {
	a := newBareAgent(t, store.NewMemLog[message.Message]()) // empty log
	a.id = "recon-001"
	for i := uint64(1); i <= 3; i++ {
		a.ariaSrv.Commit(aria.Turn{
			ID:     i,
			Sealed: true,
			Nodes:  []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleInput, Markdown: "hi"}},
		})
	}

	require.Equal(t, uint64(3), a.ariaSrv.LastTurn())
	require.False(t, a.ariaSrv.HasOpen())

	a.reconcileAriaServer()

	assert.Equal(t, uint64(3), a.ariaSrv.LastTurn(),
		"reconcileAriaServer must not shrink history on an empty figLog read")

	got := a.ariaSrv.Read(aria.Anchor{}, 1<<20)
	assert.Len(t, got.Parts, 3,
		"a read after reconcile must still return the three original turns")
}

// When history covers the current state, Restore proceeds.
func TestReconcileAriaServer_AllowsGrow(t *testing.T) {
	logMem := store.NewMemLog[message.Message]()
	_, err := logMem.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")},
	}})
	require.NoError(t, err)
	_, err = logMem.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput, Content: []message.Content{message.TextContent("world")},
	}})
	require.NoError(t, err)

	// A prompt and its reply are one exchange, not two units.
	turns := compose.Turns(unwrapMessages(logMem.Read()))
	require.Len(t, turns, 1)

	a := newBareAgent(t, logMem)
	a.id = "recon-002"
	a.reconcileAriaServer()
	assert.Equal(t, uint64(1), a.ariaSrv.LastTurn(),
		"reconcile against a readable log should adopt its turns")
	assert.Len(t, a.ariaSrv.Turns()[0].Nodes, 1,
		"the turn holds the reply; the question is Turn.Inquiry, not a node")
	assert.Equal(t, "hello", a.ariaSrv.Turns()[0].Inquiry,
		"the adopted turn carries its question as text")
}
