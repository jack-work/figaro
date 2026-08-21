package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/stretchr/testify/require"
)

type liveForkFigaro struct {
	id         string
	killed     bool
	turnActive bool
}

func (f *liveForkFigaro) ID() string         { return f.id }
func (f *liveForkFigaro) SocketPath() string { return "" }
func (f *liveForkFigaro) Interrupt()         {}
func (f *liveForkFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{ID: f.id, State: "active", MessageCount: 12, Provider: "provider"}
}
func (f *liveForkFigaro) Kill()            { f.killed = true }
func (f *liveForkFigaro) TurnActive() bool { return f.turnActive }

type liveForkBackend struct {
	store.Backend
	forked     bool
	parentMeta *store.AriaMeta
	childMeta  *store.AriaMeta
	owner      store.OwnerInfo
	nodes      map[string]store.NodeView
	form       map[string]message.Patch
	formVer    uint64
	log        store.Log[message.Message]
}

// Open backs forkPointOf, which maps a turn id to a fork LT. The log holds one
// completed exchange so turn 1 resolves.
func (f *liveForkBackend) OpenFigIR(string) (store.Log[message.Message], error) {
	if f.log == nil {
		l := store.NewMemLog[message.Message]()
		l.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, TurnID: 1}})
		l.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleOutput, TurnID: 1}})
		f.log = l
	}
	return f.log, nil
}

func (f *liveForkBackend) ApplyForm(ariaID string, patch message.Patch) (uint64, error) {
	if f.form == nil {
		f.form = map[string]message.Patch{}
	}
	f.form[ariaID] = patch
	f.formVer++
	return f.formVer, nil
}

func (b *liveForkBackend) Fork(id string) (string, string, error) {
	b.forked = true
	return id, "alternative", nil
}

func (b *liveForkBackend) ForkWith(_ string, _ uint64, patch message.Patch) (string, uint64, error) {
	if patch.IsEmpty() {
		return "", 0, fmt.Errorf("fork-with: a fork must carry a patch")
	}
	b.forked = true
	if b.form == nil {
		b.form = map[string]message.Patch{}
	}
	b.form["alternative"] = patch
	b.formVer++
	return "alternative", b.formVer, nil
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
			MessageCount:  12,
			Provider:      "provider",
			Model:         "model",
			Mantra:        "mantra",
			Cwd:           "work",
			OutfitName:    "outfit",
			OutfitVersion: "version",
		},
		owner: store.OwnerInfo{IsRoot: true},
	}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}
	params, err := json.Marshal(rpc.ForkRequest{FigaroID: "parent", AtTurn: 1})
	require.NoError(t, err)

	_, err = h.fork(t.Context(), params)
	require.NoError(t, err)
	require.Zero(t, backend.childMeta.MessageCount)
	require.Empty(t, backend.childMeta.Provider)
	require.Empty(t, backend.childMeta.Model)
	require.Empty(t, backend.childMeta.Mantra)
	require.Empty(t, backend.childMeta.Cwd)
	require.Empty(t, backend.childMeta.OutfitName)
	require.Empty(t, backend.childMeta.OutfitVersion)
}

// A fork must NOT be handed to any agent's actor.
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
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("complete")},
		StopReason: message.StopEnd,
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestForkDuringActiveStreamKeepsContinuationRunning(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	outfit, err := backend.CreateOutfit("fork", message.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"active-fork"`),
		"system.model":    json.RawMessage(`"test"`),
	}})
	require.NoError(t, err)
	id, err := backend.CreateConversation(outfit)
	require.NoError(t, err)
	snapshot, err := backend.FormState(id)
	require.NoError(t, err)
	cb, _ := form.Open("")
	cb.Apply(snapshot.AsPatch())

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
		Form:       cb,
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

	alternative, err := backend.OpenFigIR(response.Alternative)
	require.NoError(t, err)
	require.Equal(t, 1, message.CountMessages(unwrapMessages(alternative.Read())))

	release()
	continuation, err := backend.OpenFigIR(id)
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
			Role:       message.RoleOutput,
			Content:    []message.Content{call},
			StopReason: message.StopToolInvoke,
		}
	} else {
		bus.PushDelta(message.TextContent("finished"))
		msg = message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("finished")},
			StopReason: message.StopEnd,
		}
	}
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
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	outfit, err := backend.CreateOutfit("fork-tool", message.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"active-tool"`),
		"system.model":    json.RawMessage(`"test"`),
	}})
	require.NoError(t, err)
	id, err := backend.CreateConversation(outfit)
	require.NoError(t, err)
	snapshot, err := backend.FormState(id)
	require.NoError(t, err)
	cb, _ := form.Open("")
	cb.Apply(snapshot.AsPatch())

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
		ID:       id,
		Provider: &activeToolProvider{},
		Tools:    tools,
		Backend:  backend,
		Form:     cb,
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

	alternative, err := backend.OpenFigIR(response.Alternative)
	require.NoError(t, err)
	alternativeCount := message.CountMessages(unwrapMessages(alternative.Read()))
	require.GreaterOrEqual(t, alternativeCount, 1)

	release()
	continuation, err := backend.OpenFigIR(id)
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

// selfForkTool forks the aria it is running inside, from inside the tool call,
// and waits for the answer. That is what `figaro fork --id <me>` does: the tool
// blocks on an RPC that targets its own aria.
type selfForkTool struct {
	h      *handlers
	ariaID string
	result chan forkResult
}

func (*selfForkTool) Name() string        { return "blocking" }
func (*selfForkTool) Description() string { return "forks its own aria" }
func (*selfForkTool) Parameters() any     { return map[string]any{"type": "object"} }
func (s *selfForkTool) Execute(ctx context.Context, _ map[string]any, _ tool.OnOutput) ([]message.Content, error) {
	params, _ := json.Marshal(rpc.ForkRequest{FigaroID: s.ariaID})
	value, err := s.h.fork(ctx, params)
	s.result <- forkResult{value: value, err: err}
	return []message.Content{message.TextContent("forked")}, nil
}

// A FIGARO MUST BE ABLE TO FORK ITSELF.
//
// This is the deadlock the whole trunk-index effort exists to remove, reduced
// to its smallest shape. The fork used to be handed to the target's inbox and
// waited on; a self-fork therefore queued behind the very turn whose tool call
// was waiting for it. The tool could not return until the fork ran, and the
// fork could not run until the tool returned.
//
// figwal already excludes a fork from concurrent appends via lockLineage, so
// the inbox hop was a second lock over the first, and it was the second one
// that closed the circle. This test hangs on the old code and passes on the
// new; the timeout IS the assertion.
func TestFigaroCanForkItselfFromInsideItsOwnTurn(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	outfit, err := backend.CreateOutfit("selffork", message.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"active-tool"`),
		"system.model":    json.RawMessage(`"test"`),
	}})
	require.NoError(t, err)
	id, err := backend.CreateConversation(outfit)
	require.NoError(t, err)
	snapshot, err := backend.FormState(id)
	require.NoError(t, err)
	cb, _ := form.Open("")
	cb.Apply(snapshot.AsPatch())

	registry := NewRegistry()
	h := &handlers{angelus: &Angelus{Registry: registry, Backend: backend}}
	forkTool := &selfForkTool{h: h, ariaID: id, result: make(chan forkResult, 1)}
	tools := tool.NewRegistry()
	tools.MustRegister(forkTool)

	agent := figaro.NewAgent(figaro.Config{
		ID: id, Provider: &activeToolProvider{}, Tools: tools,
		Backend: backend, Form: cb,
	})
	t.Cleanup(agent.Kill)
	require.NoError(t, registry.Register(agent))

	agent.SubmitPrompt(rpc.QuaRequest{Text: "fork yourself"})

	select {
	case result := <-forkTool.result:
		require.NoError(t, result.err, "self-fork returned an error")
		resp := result.value.(rpc.ForkResponse)
		require.Equal(t, id, resp.Parent)
		require.NotEmpty(t, resp.Alternative, "no alternative minted")
		require.NotEqual(t, resp.Alternative, resp.Continuation)
	case <-time.After(15 * time.Second):
		t.Fatal("DEADLOCK: a figaro could not fork itself from inside its own turn")
	}
}
