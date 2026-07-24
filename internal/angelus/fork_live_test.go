package angelus

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/stretchr/testify/require"
)

type liveForkFigaro struct {
	id     string
	killed bool
}

func (f *liveForkFigaro) ID() string                 { return f.id }
func (f *liveForkFigaro) SocketPath() string         { return "" }
func (f *liveForkFigaro) Interrupt()                 {}
func (f *liveForkFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{ID: f.id, State: "active", MessageCount: 12, Provider: "provider"}
}
func (f *liveForkFigaro) Kill() { f.killed = true }

type liveForkBackend struct {
	store.Backend
	forked     bool
	parentMeta *store.AriaMeta
	childMeta  *store.AriaMeta
	owner      store.OwnerInfo
	nodes      map[string]store.NodeView
	chalk      map[string]message.Patch
}

func (f *liveForkBackend) ApplyChalkboard(ariaID string, patch message.Patch) error {
	if f.chalk == nil {
		f.chalk = map[string]message.Patch{}
	}
	f.chalk[ariaID] = patch
	return nil
}

type coordinatingForkFigaro struct {
	liveForkFigaro
	coordinated atomic.Int32
}

func (f *coordinatingForkFigaro) CoordinateFork(run func() error) error {
	f.coordinated.Add(1)
	return run()
}

func (b *liveForkBackend) Fork(id string) (string, string, error) {
	b.forked = true
	return id, "alternative", nil
}

func (b *liveForkBackend) ForkAt(id string, _ uint64) (string, string, error) {
	b.forked = true
	return id, "alternative", nil
}

func (b *liveForkBackend) OwnerResolution(string, uint64) (store.OwnerInfo, error) {
	return b.owner, nil
}

func (b *liveForkBackend) Node(id string) (store.NodeView, bool) {
	node, ok := b.nodes[id]
	return node, ok
}

func (b *liveForkBackend) Meta(string) (*store.AriaMeta, error) {
	return b.parentMeta, nil
}

func (b *liveForkBackend) SetMeta(_ string, meta *store.AriaMeta) error {
	b.childMeta = meta
	return nil
}

func TestForkKeepsLiveAgentRunning(t *testing.T) {
	registry := NewRegistry()
	live := &liveForkFigaro{id: "live"}
	require.NoError(t, registry.Register(live))
	backend := &liveForkBackend{parentMeta: &store.AriaMeta{MessageCount: 12, Provider: "provider"}}
	h := &handlers{angelus: &Angelus{Registry: registry, Backend: backend}}
	params, err := json.Marshal(rpc.ForkRequest{FigaroID: live.id})
	require.NoError(t, err)

	_, err = h.fork(t.Context(), params)
	require.NoError(t, err)
	require.True(t, backend.forked)
	require.False(t, live.killed)
	require.Same(t, live, registry.Get(live.id))
	require.Equal(t, 12, backend.childMeta.MessageCount)
	require.Equal(t, "provider", backend.childMeta.Provider)
}

func TestInteriorForkAtRootDoesNotCopyConversationState(t *testing.T) {
	backend := &liveForkBackend{
		parentMeta: &store.AriaMeta{
			MessageCount:   12,
			Provider:       "provider",
			Model:          "model",
			Mantra:         "mantra",
			Cwd:            "work",
			LoadoutName:    "loadout",
			LoadoutVersion: "version",
		},
		owner: store.OwnerInfo{IsRoot: true},
	}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}
	params, err := json.Marshal(rpc.ForkRequest{FigaroID: "parent", AtMainLT: 1})
	require.NoError(t, err)

	_, err = h.fork(t.Context(), params)
	require.NoError(t, err)
	require.Zero(t, backend.childMeta.MessageCount)
	require.Empty(t, backend.childMeta.Provider)
	require.Empty(t, backend.childMeta.Model)
	require.Empty(t, backend.childMeta.Mantra)
	require.Empty(t, backend.childMeta.Cwd)
	require.Empty(t, backend.childMeta.LoadoutName)
	require.Empty(t, backend.childMeta.LoadoutVersion)
}

func TestInteriorForkCoordinatesOwningTrunk(t *testing.T) {
	target := &coordinatingForkFigaro{liveForkFigaro: liveForkFigaro{id: "target"}}
	owner := &coordinatingForkFigaro{liveForkFigaro: liveForkFigaro{id: "owner"}}
	registry := NewRegistry()
	require.NoError(t, registry.Register(target))
	require.NoError(t, registry.Register(owner))
	backend := &liveForkBackend{
		parentMeta: &store.AriaMeta{},
		owner:      store.OwnerInfo{Trunk: owner.id},
	}
	h := &handlers{angelus: &Angelus{Registry: registry, Backend: backend}}
	params, err := json.Marshal(rpc.ForkRequest{FigaroID: target.id, AtMainLT: 1})
	require.NoError(t, err)

	_, err = h.fork(t.Context(), params)
	require.NoError(t, err)
	require.Equal(t, int32(0), target.coordinated.Load())
	require.Equal(t, int32(1), owner.coordinated.Load())
}

type activeForkProvider struct {
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
	calls    atomic.Int32
}

type forkResult struct {
	value interface{}
	err   error
}

func (p *activeForkProvider) Name() string        { return "active-fork" }
func (p *activeForkProvider) Fingerprint() string { return "active-fork/v1" }
func (p *activeForkProvider) SetModel(string)     {}
func (p *activeForkProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *activeForkProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	call := p.calls.Add(1)
	bus.PushDelta(message.TextContent("streaming"))
	if call == 1 {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			close(p.canceled)
			return ctx.Err()
		}
	}
	msg := message.Message{
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent("complete")},
		StopReason: message.StopEnd,
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestForkDuringActiveStreamKeepsContinuationRunning(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	loadout, err := backend.CreateLoadout("fork", message.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"active-fork"`),
		"system.model":    json.RawMessage(`"test"`),
	}})
	require.NoError(t, err)
	id, err := backend.CreateConversation(loadout)
	require.NoError(t, err)
	snapshot, err := backend.ChalkboardState(id)
	require.NoError(t, err)
	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: snapshot})

	prov := &activeForkProvider{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(prov.release) }) }
	agent := figaro.NewAgent(figaro.Config{
		ID:         id,
		SocketPath: "",
		Provider:   prov,
		Backend:    backend,
		Chalkboard: cb,
	})
	t.Cleanup(func() {
		release()
		agent.Kill()
	})
	registry := NewRegistry()
	require.NoError(t, registry.Register(agent))
	h := &handlers{angelus: &Angelus{Registry: registry, Backend: backend}}

	agent.SubmitPrompt(rpc.QuaRequest{Text: "first"})
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider stream did not start")
	}

	params, err := json.Marshal(rpc.ForkRequest{FigaroID: id})
	require.NoError(t, err)
	forked := make(chan forkResult, 1)
	go func() {
		value, err := h.fork(t.Context(), params)
		forked <- forkResult{value: value, err: err}
	}()

	var response rpc.ForkResponse
	select {
	case result := <-forked:
		require.NoError(t, result.err)
		response = result.value.(rpc.ForkResponse)
	case <-time.After(5 * time.Second):
		t.Fatal("fork waited for the active stream to finish")
	}
	require.Equal(t, id, response.Continuation)
	require.NotEmpty(t, response.Alternative)
	require.Same(t, agent, registry.Get(id))
	require.Equal(t, "active", agent.Info().State)
	select {
	case <-prov.canceled:
		t.Fatal("fork canceled the active provider stream")
	default:
	}

	alternative, err := backend.Open(response.Alternative)
	require.NoError(t, err)
	require.Equal(t, 1, message.CountMessages(unwrapMessages(alternative.Read())))

	release()
	continuation, err := backend.Open(id)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return agent.Info().State == "idle" && message.CountMessages(unwrapMessages(continuation.Read())) == 2
	}, 5*time.Second, 10*time.Millisecond)

	agent.SubmitPrompt(rpc.QuaRequest{Text: "second"})
	require.Eventually(t, func() bool {
		return agent.Info().State == "idle" && message.CountMessages(unwrapMessages(continuation.Read())) == 4
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, message.CountMessages(unwrapMessages(alternative.Read())))
}

func unwrapMessages(entries []store.Entry[message.Message]) []message.Message {
	out := make([]message.Message, len(entries))
	for i, entry := range entries {
		out[i] = entry.Payload
	}
	return out
}

type activeToolProvider struct {
	calls atomic.Int32
}

func (p *activeToolProvider) Name() string        { return "active-tool" }
func (p *activeToolProvider) Fingerprint() string { return "active-tool/v1" }
func (p *activeToolProvider) SetModel(string)     {}
func (p *activeToolProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *activeToolProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	var msg message.Message
	if p.calls.Add(1) == 1 {
		call := message.Content{
			Type:       message.ContentToolInvoke,
			ToolCallID: "fork-tool-call",
			ToolName:   "blocking",
			Arguments:  map[string]interface{}{},
		}
		bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
		bus.PushToolReady(call)
		msg = message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{call},
			StopReason: message.StopToolInvoke,
		}
	} else {
		bus.PushDelta(message.TextContent("finished"))
		msg = message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent("finished")},
			StopReason: message.StopEnd,
		}
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

type blockingForkTool struct {
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
	calls    atomic.Int32
}

func (*blockingForkTool) Name() string        { return "blocking" }
func (*blockingForkTool) Description() string { return "blocks until released" }
func (*blockingForkTool) Parameters() any     { return map[string]any{"type": "object"} }
func (t *blockingForkTool) Execute(ctx context.Context, _ map[string]any, output tool.OnOutput) ([]message.Content, error) {
	t.calls.Add(1)
	close(t.started)
	output([]byte("partial"))
	select {
	case <-t.release:
		return []message.Content{message.TextContent("tool complete")}, nil
	case <-ctx.Done():
		close(t.canceled)
		return nil, ctx.Err()
	}
}

func TestForkDuringActiveToolKeepsToolAndContinuationRunning(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	loadout, err := backend.CreateLoadout("fork-tool", message.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"active-tool"`),
		"system.model":    json.RawMessage(`"test"`),
	}})
	require.NoError(t, err)
	id, err := backend.CreateConversation(loadout)
	require.NoError(t, err)
	snapshot, err := backend.ChalkboardState(id)
	require.NoError(t, err)
	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: snapshot})

	blocking := &blockingForkTool{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocking.release) }) }
	tools := tool.NewRegistry()
	tools.MustRegister(blocking)
	agent := figaro.NewAgent(figaro.Config{
		ID:         id,
		Provider:   &activeToolProvider{},
		Tools:      tools,
		Backend:    backend,
		Chalkboard: cb,
	})
	t.Cleanup(func() {
		release()
		agent.Kill()
	})
	registry := NewRegistry()
	require.NoError(t, registry.Register(agent))
	h := &handlers{angelus: &Angelus{Registry: registry, Backend: backend}}

	agent.SubmitPrompt(rpc.QuaRequest{Text: "use the tool"})
	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}

	params, err := json.Marshal(rpc.ForkRequest{FigaroID: id})
	require.NoError(t, err)
	forked := make(chan forkResult, 1)
	go func() {
		value, err := h.fork(t.Context(), params)
		forked <- forkResult{value: value, err: err}
	}()

	var response rpc.ForkResponse
	select {
	case result := <-forked:
		require.NoError(t, result.err)
		response = result.value.(rpc.ForkResponse)
	case <-time.After(5 * time.Second):
		t.Fatal("fork waited for the active tool to finish")
	}
	require.Equal(t, id, response.Continuation)
	require.Same(t, agent, registry.Get(id))
	require.Equal(t, "active", agent.Info().State)
	require.Equal(t, int32(1), blocking.calls.Load())
	select {
	case <-blocking.canceled:
		t.Fatal("fork canceled the active tool")
	default:
	}

	alternative, err := backend.Open(response.Alternative)
	require.NoError(t, err)
	alternativeCount := message.CountMessages(unwrapMessages(alternative.Read()))
	require.GreaterOrEqual(t, alternativeCount, 1)

	release()
	continuation, err := backend.Open(id)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return agent.Info().State == "idle" && message.CountMessages(unwrapMessages(continuation.Read())) == 4
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), blocking.calls.Load())
	require.Equal(t, alternativeCount, message.CountMessages(unwrapMessages(alternative.Read())))
}

type blockingInfoFigaro struct {
	liveForkFigaro
	entered chan struct{}
	release chan struct{}
}

func (f *blockingInfoFigaro) Info() figaro.FigaroInfo {
	close(f.entered)
	<-f.release
	return f.liveForkFigaro.Info()
}

func TestRegistryListDoesNotHoldRegistryLockDuringInfo(t *testing.T) {
	registry := NewRegistry()
	f := &blockingInfoFigaro{
		liveForkFigaro: liveForkFigaro{id: "live"},
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	require.NoError(t, registry.Register(f))
	listed := make(chan struct{})
	go func() {
		registry.List()
		close(listed)
	}()
	<-f.entered

	bound := make(chan error, 1)
	go func() { bound <- registry.Bind(42, f.id, 0) }()
	select {
	case err := <-bound:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Bind blocked behind Figaro.Info")
	}
	close(f.release)
	<-listed
}
