package figaro_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	client "github.com/jack-work/figaro/sdk/aria"
)

type mockProvider struct {
	response string
	cache    store.Log[[]json.RawMessage] // nil = no cache (tests don't need one)
}

func (m *mockProvider) Name() string                                             { return "mock" }
func (m *mockProvider) Fingerprint() string                                      { return "mock/v0" }
func (m *mockProvider) SetModel(model string)                                    {}
func (m *mockProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (m *mockProvider) encode(_ message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role": livedoc.RoleInput,"content":[]}`)}, nil
}

func (m *mockProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	mockCatchUp(in.FigLog, m.cache, m.encode, m.Fingerprint())
	mockPushAssistant(in.FigLog, m.cache, bus, m.encode, m.Fingerprint(), m.response)
	return nil
}

// streamingMockProvider delays its append so a pre-append live frame can compose.
type streamingMockProvider struct{ response string }

func (m *streamingMockProvider) Name() string        { return "mock" }
func (m *streamingMockProvider) Fingerprint() string { return "mock/v0" }
func (m *streamingMockProvider) SetModel(string)     {}
func (m *streamingMockProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (m *streamingMockProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.Content{Type: message.ContentProse, Text: m.response})
	time.Sleep(60 * time.Millisecond) // drain loop composes the streaming frame
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent(m.response)},
		StopReason: message.StopEnd,
	}
	time.Sleep(30 * time.Millisecond) // pre-append window: the loop has not seen it yet
	bus.PushFigaro(msg)
	return nil
}

type metricsProvider struct{}

func (metricsProvider) Name() string                                         { return "metrics" }
func (metricsProvider) Fingerprint() string                                  { return "metrics/v1" }
func (metricsProvider) SetModel(string)                                      {}
func (metricsProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (metricsProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("done")},
		StopReason: message.StopEnd,
		Usage: &message.Usage{
			InputTokens:  12000,
			OutputTokens: 3000,
		},
	}
	bus.PushDelta(message.TextContent("done"))
	bus.PushFigaro(msg)
	return nil
}

type fakeCatalog struct {
	mu    sync.Mutex
	snap  form.Snapshot
	wants []string
}

func (c *fakeCatalog) Snapshot() form.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

func (c *fakeCatalog) Want(provider, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wants = append(c.wants, provider+"/"+model)
}

func (c *fakeCatalog) fill(provider, model string, window int) {
	raw, _ := json.Marshal(models.Provider{Models: map[string]models.Entry{model: {Context: models.Context{Default: window}}}})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap = c.snap.Apply(form.Build(c.snap, map[string]json.RawMessage{provider: raw}, nil))
}

func (c *fakeCatalog) wanted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.wants...)
}

type mockEncodeFn func(msg message.Message, prev form.Snapshot) ([]json.RawMessage, error)

// mockCatchUp mirrors what real providers do at the top of Send; skipped when cache is nil.
func mockCatchUp(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], encode mockEncodeFn, fingerprint string) {
	snap := form.Snapshot{}
	for _, e := range figLog.Read() {
		msg := e.Payload
		msg.LogicalTime = e.LT
		if cache != nil {
			if _, ok := cache.Lookup(msg.LogicalTime); !ok {
				if payload, err := encode(msg, snap); err == nil {
					_, _ = cache.Append(store.Entry[[]json.RawMessage]{
						FigaroLT:    msg.LogicalTime,
						Payload:     payload,
						Fingerprint: fingerprint,
					})
				}
			}
		} else {
			_, _ = encode(msg, snap)
		}
		for _, p := range msg.Patches {
			snap = snap.Apply(p)
		}
	}
}

// mockPushAssistant simulates a streaming turn: delta, figLog append, cache write, PushFigaro.
func mockPushAssistant(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], bus provider.Bus, encode mockEncodeFn, fingerprint, text string) {
	if text == "" {
		return
	}
	bus.PushDelta(message.Content{Type: message.ContentProse, Text: text})
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent(text)},
		StopReason: message.StopEnd,
	}
	entry, err := figLog.Append(store.Entry[message.Message]{Payload: msg})
	if err == nil {
		msg.LogicalTime = entry.LT
		if cache != nil {
			if payload, eErr := encode(msg, form.Snapshot{}); eErr == nil {
				_, _ = cache.Append(store.Entry[[]json.RawMessage]{
					FigaroLT:    entry.LT,
					Payload:     payload,
					Fingerprint: fingerprint,
				})
			}
		}
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
}

// chanNotifier pipes every fanout call into a channel; close-once is guarded by mu.
type chanNotifier struct {
	mu     sync.Mutex
	ch     chan rpc.Notification
	closed bool
}

func (c *chanNotifier) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	select {
	case c.ch <- rpc.Notification{JSONRPC: "2.0", Method: method, Params: params}:
	default:
	}
	return nil
}

func (c *chanNotifier) closeOnce() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.ch)
	}
}

// subscribeChan returns the receive end and an unsubscribe func that also closes the channel.
func subscribeChan(a *figaro.Agent) (<-chan rpc.Notification, func()) {
	sink := &chanNotifier{ch: make(chan rpc.Notification, 128)}
	unsub := a.Subscribe(sink)
	return sink.ch, func() {
		unsub()
		sink.closeOnce()
	}
}

func submitPrompt(a *figaro.Agent, text string) {
	a.SubmitPrompt(rpc.QuaRequest{Text: text})
}

// submitSteer sends an ordinary prompt; what makes it a steer is that a turn is running.
func submitSteer(a *figaro.Agent, text string) {
	a.SubmitPrompt(rpc.QuaRequest{Text: text})
}

func newTestAgent(t *testing.T, response string) *figaro.Agent {
	t.Helper()
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}, nil))
	be, id := store.NewTestAria(t, "d", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}, nil))
	return figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        id,
		Provider:  &mockProvider{response: response},
		Backend:   be,
		Form:      cb,
	})
}

func TestAgentPersistsCompleteListMetadata(t *testing.T) {
	backend, id := backedConv(t, t.TempDir())
	_, applyErr := backend.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra":     json.RawMessage(`"initial"`),
		"system.cwd": json.RawMessage(`"work"`),
	}, nil))
	require.NoError(t, applyErr)
	snapshot, err := backend.FormState(id)
	require.NoError(t, err)
	cb, _ := form.Open("")
	cb.Apply(snapshot.AsPatch())
	createdAt := time.UnixMilli(1_000)
	lastActive := time.UnixMilli(2_000)

	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         id,
		Provider:   &mockProvider{},
		Backend:    backend,
		Form:       cb,
		CreatedAt:  createdAt,
		LastActive: lastActive,
	})
	t.Cleanup(a.Kill)

	meta, err := backend.Meta(id)
	require.NoError(t, err)
	require.Equal(t, "mock", meta.Provider)
	require.Equal(t, "mock-model-v1", meta.Model)
	require.Equal(t, "initial", meta.Mantra)
	require.Equal(t, "work", meta.Cwd)
	require.Equal(t, "d", meta.OutfitName)
	require.NotEmpty(t, meta.OutfitVersion)
	require.Equal(t, createdAt.UnixMilli(), meta.CreatedAtMS)

	_, _, err = a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"updated"`),
	}, nil), 0)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		meta, err = backend.Meta(id)
		return err == nil && meta != nil && meta.Mantra == "updated"
	}, time.Second, 10*time.Millisecond)
}

func backedConv(t *testing.T, dir string) (store.Backend, string) {
	t.Helper()
	b, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	l, err := b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}, nil))
	require.NoError(t, err)
	conv, err := b.CreateConversation(l)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, b.Close())
	})
	return b, conv
}

func unwrapForTest(entries []store.Entry[message.Message]) []message.Message {
	out := make([]message.Message, len(entries))
	for i, e := range entries {
		out[i] = e.Payload
	}
	return out
}

// nonGenesis drops the structural birth tics and the outfit's reminder tic.
func nonGenesis(msgs []message.Message) []message.Message {
	var out []message.Message
	for _, m := range msgs {
		if m.Role == message.RoleGenesis {
			continue
		}
		if m.Role == message.RoleInput && len(m.Content) == 0 && len(m.Patches) == 0 {
			continue // outfit birth tic
		}
		out = append(out, m)
	}
	return out
}

func TestAgent_ID(t *testing.T) {
	a := newTestAgent(t, "hi")
	defer a.Kill()
	assert.NotEmpty(t, a.ID())
}

func TestAgent_PromptAndSubscribe(t *testing.T) {
	a := newTestAgent(t, "4")
	defer a.Kill()

	ch, _ := subscribeChan(a)

	submitPrompt(a, "What is 2+2?")

	var notifications []rpc.Notification
	timeout := time.After(5 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			notifications = append(notifications, n)
			if n.Method == rpc.MethodTurnDone {
				break loop
			}

		case <-timeout:
			t.Fatal("timeout waiting for notifications")
		}
	}

	methods := make([]string, len(notifications))
	for i, n := range notifications {
		methods[i] = n.Method
	}
	assert.Contains(t, methods, rpc.MethodAriaFrame)
	assert.Contains(t, methods, rpc.MethodTurnDone)
}

func TestAgentContextMetricsTrackCurrentSession(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"metrics"`),
		"system.model":    json.RawMessage(`"gpt-5.6-terra"`),
		"mantra":          json.RawMessage(`"keep session accounting visible"`),
	}, nil))
	cat := &fakeCatalog{}
	cat.fill("metrics", "gpt-5.6-terra", 128000)
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  metricsProvider{},
		Form:      cb,
		Models:    cat,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "hello")
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto done
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for turn completion")
		}
	}
done:

	info := a.Info()
	assert.Equal(t, 12000, info.TokensIn)
	assert.Equal(t, 3000, info.TokensOut)
	assert.Equal(t, 15000, info.ContextTokens)
	assert.Equal(t, 128000, info.ContextLimit)
	assert.True(t, info.ContextExact)

	read := a.Read(aria.Anchor{Turn: 0}, 1<<20)
	require.NotNil(t, read.Metrics)
	assert.Equal(t, "keep session accounting visible", read.Metrics.Mantra)
	assert.Equal(t, 15000, read.Metrics.ContextTokens)

	raw, err := a.Handle(context.Background(), rpc.MethodContext, nil)
	require.NoError(t, err)
	contextResp := raw.(rpc.ContextResponse)
	require.NotNil(t, contextResp.Metrics)
	assert.Equal(t, 15000, contextResp.Metrics.ContextTokens)
}

// The agent asks the catalog for the board's own provider x model at its
// metrics boundary, and a fill that lands later is what Info reports: nothing
// about the limit is captured at construction.
func TestAgentWantsItsModelAndReportsALateFill(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"metrics"`),
		"system.model":    json.RawMessage(`"gpt-5.6-terra"`),
	}, nil))
	cat := &fakeCatalog{}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  metricsProvider{},
		Form:      cb,
		Models:    cat,
	})
	defer a.Kill()

	assert.Equal(t, 0, a.Info().ContextLimit)
	assert.Contains(t, cat.wanted(), "metrics/gpt-5.6-terra")

	cat.fill("metrics", "gpt-5.6-terra", 128000)
	assert.Equal(t, 128000, a.Info().ContextLimit)
}

func TestAgent_ReadCatchUp(t *testing.T) {
	a := newTestAgent(t, "the reply")
	defer a.Kill()

	if r := a.Read(aria.Anchor{Turn: 0}, 1<<20); len(r.Parts) != 0 || r.LiveTail() != nil {
		t.Fatalf("fresh agent: want empty read, got %+v", r)
	}

	ch, _ := subscribeChan(a)
	submitPrompt(a, "the question")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:

	r := a.Read(aria.Anchor{Turn: 0}, 1<<20)
	if r.LiveTail() != nil {
		t.Fatalf("idle agent should have no live unit: %+v", r.LiveTail())
	}
	if len(r.Parts) != 1 {
		t.Fatalf("want a single turn, got %d: %+v", len(r.Parts), r.Parts)
	}
	if r.Parts[0].Inquiry != "the question" {
		t.Errorf("the turn must carry its question as text: %+v", r.Parts[0])
	}
	if nodesContain(r.Parts[0].Nodes, "the question") {
		t.Errorf("the question is not a node: %+v", r.Parts[0])
	}
	if !nodesContain(r.Parts[0].Nodes, "the reply") {
		t.Errorf("the turn must hold the reply: %+v", r.Parts[0])
	}
}

func nodesContain(nodes []livedoc.Node, sub string) bool {
	for _, n := range nodes {
		if strings.Contains(n.Markdown, sub) || strings.Contains(n.Output, sub) {
			return true
		}
	}
	return false
}

func TestAgent_Context(t *testing.T) {
	a := newTestAgent(t, "hello")
	defer a.Kill()

	ch, _ := subscribeChan(a)

	for _, m := range a.Context() {
		assert.True(t, message.IsCeremonial(m), "unexpected message before the first prompt: %+v", m)
	}

	submitPrompt(a, "say hi")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:

	var msgs []message.Message
	for _, m := range a.Context() {
		if !message.IsCeremonial(m) {
			msgs = append(msgs, m)
		}
	}
	require.GreaterOrEqual(t, len(msgs), 2)
	assert.Equal(t, message.RoleInput, msgs[0].Role)
	assert.Equal(t, message.RoleOutput, msgs[1].Role)
}

func TestAgent_FIFOOrdering(t *testing.T) {
	// Two prompts back to back reach the model in order; whether as one turn or two is timing.
	a := newTestAgent(t, "")
	a.Kill() // kill the default one

	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a = figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  &mockProvider{response: "ok"},
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)

	submitPrompt(a, "first")
	submitPrompt(a, "second")

	timeout := time.After(5 * time.Second)
	for settled := false; !settled; {
		select {
		case n := <-ch:
			if n.Method != rpc.MethodTurnDone {
				continue
			}
			var d rpc.DoneEntry
			raw, _ := json.Marshal(n.Params)
			_ = json.Unmarshal(raw, &d)
			settled = d.Idle == nil || *d.Idle
		case <-timeout:
			t.Fatal("timeout waiting for the aria to go idle")
		}
	}

	var input string
	for _, m := range a.Context() {
		if m.Role != message.RoleInput {
			continue
		}
		for _, c := range m.Content {
			if c.Type == message.ContentProse {
				input += c.Text + "\n"
			}
		}
	}
	first := strings.Index(input, "first")
	second := strings.Index(input, "second")
	require.GreaterOrEqual(t, first, 0, "the first prompt never reached the conversation")
	require.GreaterOrEqual(t, second, 0, "the second prompt never reached the conversation")
	assert.Less(t, first, second, "prompts must reach the model in submission order")
}

func TestAgent_MultipleSubscribers(t *testing.T) {
	a := newTestAgent(t, "hi")
	defer a.Kill()

	ch1, _ := subscribeChan(a)
	ch2, _ := subscribeChan(a)

	submitPrompt(a, "hello")

	timeout := time.After(5 * time.Second)
	got1, got2 := false, false
	for !got1 || !got2 {
		select {
		case n := <-ch1:
			if n.Method == rpc.MethodTurnDone {
				got1 = true
			}
		case n := <-ch2:
			if n.Method == rpc.MethodTurnDone {
				got2 = true
			}
		case <-timeout:
			t.Fatalf("timeout: got1=%v got2=%v", got1, got2)
		}
	}
}

func TestAgent_Unsubscribe(t *testing.T) {
	a := newTestAgent(t, "hi")
	defer a.Kill()

	ch, unsub := subscribeChan(a)
	unsub()

	_, open := <-ch
	assert.False(t, open, "unsubscribed channel should be closed")
}

func TestAgent_Kill(t *testing.T) {
	a := newTestAgent(t, "hi")
	ch, _ := subscribeChan(a)

	a.Kill()

	// Kill clears subscribers but does not close test channels.
	select {
	case n, ok := <-ch:
		if ok {
			t.Fatalf("unexpected notification after Kill: %+v", n)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

type panicProvider struct {
	panicCount int
	response   string
}

func (p *panicProvider) Name() string                                             { return "panic-mock" }
func (p *panicProvider) Fingerprint() string                                      { return "panic-mock/v0" }
func (p *panicProvider) SetModel(model string)                                    {}
func (p *panicProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *panicProvider) encode(_ message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role": livedoc.RoleInput,"content":[]}`)}, nil
}

func (p *panicProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.panicCount > 0 {
		p.panicCount--
		panic("simulated crash")
	}
	mockCatchUp(in.FigLog, nil, p.encode, p.Fingerprint())
	mockPushAssistant(in.FigLog, nil, bus, p.encode, p.Fingerprint(), p.response)
	return nil
}

func TestAgent_PanicRecovery(t *testing.T) {
	prov := &panicProvider{panicCount: 1, response: "recovered"}

	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)

	// Every turn terminates with turn.done; an error surfaces as the reason.
	submitPrompt(a, "trigger panic")

	timeout := time.After(5 * time.Second)
	var doneReason string
	gotDone := false
	for !gotDone {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				if p, ok := n.Params.(rpc.DoneEntry); ok {
					doneReason = p.Reason
				}
				gotDone = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn.done after panic")
		}
	}
	assert.Contains(t, doneReason, "error", "panic should surface as an error turn.done")

	submitPrompt(a, "after recovery")

	gotDone = false
	timeout = time.After(5 * time.Second)
	for !gotDone {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				gotDone = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for done after recovery")
		}
	}

	// Send-path panics are recovered in the goroutine and preserve history.
	msgs := a.Context()
	require.NotEmpty(t, msgs)
	assert.Equal(t, message.RoleOutput, msgs[len(msgs)-1].Role)
}

func TestAgent_PanicRecovery_ContextReset(t *testing.T) {
	prov := &panicProvider{panicCount: 1, response: "ok"}

	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)

	submitPrompt(a, "boom")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto errorReceived
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
errorReceived:

	msgs := a.Context()
	require.NotEmpty(t, msgs, "user prompt is preserved across Send panics")
	assert.Equal(t, message.RoleInput, msgs[len(msgs)-1].Role)

	info := a.Info()
	assert.Equal(t, 0, info.TokensIn)
	assert.Equal(t, 0, info.TokensOut)
}

func TestAgent_Info(t *testing.T) {
	a := newTestAgent(t, "hi")
	defer a.Kill()

	info := a.Info()
	assert.Equal(t, a.ID(), info.ID)
	assert.Equal(t, "mock", info.Provider)
	assert.Equal(t, "mock-model-v1", info.Model)
	assert.False(t, info.CreatedAt.IsZero())
}

func TestAgent_PersistenceFlushesOnPrompt(t *testing.T) {
	storeDir := t.TempDir()
	backend, conv := backedConv(t, storeDir)

	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        conv,
		Provider:  &mockProvider{response: "persisted reply"},
		Backend:   backend,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "save me")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto firstDone
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
firstDone:

	log, err := backend.OpenFigIR(conv)
	require.NoError(t, err)
	turns := nonGenesis(unwrapForTest(log.Read()))
	require.GreaterOrEqual(t, len(turns), 2, "user + assistant should be durable")
	assert.Equal(t, message.RoleInput, turns[0].Role)
	assert.Equal(t, message.RoleOutput, turns[1].Role)

}

func TestAgent_PersistenceRestoresOnCreate(t *testing.T) {
	storeDir := t.TempDir()
	backend, conv := backedConv(t, storeDir)

	a1 := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        conv,
		Provider:  &mockProvider{response: "first reply"},
		Backend:   backend,
	})

	ch, _ := subscribeChan(a1)
	submitPrompt(a1, "hello")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto firstDone
			}
		case <-timeout:
			t.Fatal("timeout on first prompt")
		}
	}
firstDone:
	a1.Kill()

	a2 := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        conv,
		Provider:  &mockProvider{response: "second reply"},
		Backend:   backend,
	})
	defer a2.Kill()

	turns := nonGenesis(a2.Context())
	require.GreaterOrEqual(t, len(turns), 2, "should restore messages from disk")
	assert.Equal(t, message.RoleInput, turns[0].Role)
	assert.Equal(t, message.RoleOutput, turns[1].Role)
}

func TestAgent_PersistenceKillFlushes(t *testing.T) {
	storeDir := t.TempDir()
	backend, conv := backedConv(t, storeDir)

	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        conv,
		Provider:  &mockProvider{response: "will be saved"},
		Backend:   backend,
	})

	ch, _ := subscribeChan(a)
	submitPrompt(a, "save on kill")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:
	a.Kill()
	// Flusher persistence, not Close-drain: poll the segment bytes before any Close.
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		filepath.WalkDir(filepath.Join(storeDir, "ir"), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			if raw, rerr := os.ReadFile(path); rerr == nil && strings.Contains(string(raw), "will be saved") {
				found = true
			}
			return nil
		})
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assistant turn never flushed to disk")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Single-writer flock: the first backend must close before a second opens the store.
	require.NoError(t, backend.Close())

	b2, err := store.NewXwalBackend(storeDir, 0)
	require.NoError(t, err)
	defer b2.Close()
	log, err := b2.OpenFigIR(conv)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(nonGenesis(unwrapForTest(log.Read()))), 2)
}

// Boot repair appends interrupted tool-result tics after a dangling tool_use tail.
func TestAgent_BootRepairsDanglingToolUse(t *testing.T) {
	storeDir := t.TempDir()
	backend, conv := backedConv(t, storeDir)

	pre, err := backend.OpenFigIR(conv)
	require.NoError(t, err)
	_, err = pre.Append(store.Entry[message.Message]{
		Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("run a tool")},
		},
	})
	require.NoError(t, err)
	_, err = pre.Append(store.Entry[message.Message]{
		Payload: message.Message{
			Role: message.RoleOutput,
			Content: []message.Content{
				{Type: message.ContentToolInvoke, ToolCallID: "tc_boot", ToolName: "bash"},
			},
			StopReason: message.StopToolInvoke,
		},
	})
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        conv,
		Provider:  &mockProvider{response: "ignored"},
		Backend:   backend,
	})
	defer a.Kill()

	msgs := a.Context()
	tail := msgs[len(msgs)-1]
	require.Equal(t, message.RoleInput, tail.Role)
	require.Len(t, tail.Content, 1)
	assert.Equal(t, message.ContentToolResult, tail.Content[0].Type)
	assert.Equal(t, "tc_boot", tail.Content[0].ToolCallID)
	assert.True(t, tail.Content[0].IsError)
	assert.Contains(t, tail.Content[0].Text, "process died mid-turn")
}

func TestAgent_EphemeralWhenNoBackend(t *testing.T) {
	tmpDir := t.TempDir()

	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  &mockProvider{response: "gone"},
	})

	ch, _ := subscribeChan(a)
	submitPrompt(a, "vanish")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:
	a.Kill()

	entries, _ := os.ReadDir(tmpDir)
	assert.Empty(t, entries, "no files should be written without a Backend")
}

type slowProvider struct {
	started chan struct{} // closed once Send has been entered
}

func (s *slowProvider) Name() string                                             { return "slow" }
func (s *slowProvider) Fingerprint() string                                      { return "slow/v0" }
func (s *slowProvider) SetModel(model string)                                    {}
func (s *slowProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

// Send blocks until ctx is cancelled, as an HTTP SSE stream does.
func (s *slowProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	if s.started != nil {
		close(s.started)
		s.started = nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestAgent_Interrupt(t *testing.T) {
	started := make(chan struct{})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  &slowProvider{started: started},
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "take forever please")

	// Interrupt only once the provider is streaming: the mid-turn path, not a pre-start race.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never started")
	}

	a.Interrupt()

	var doneReason string
	timeout := time.After(3 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				if p, ok := n.Params.(rpc.DoneEntry); ok {
					doneReason = p.Reason
				}
				break loop
			}
		case <-timeout:
			t.Fatal("timeout waiting for interrupt to flow through")
		}
	}

	assert.Equal(t, "interrupted", doneReason)

	// Eventually idle, not instantly: a form watch can still be delivering the birth patch.
	var state string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if state = a.Info().State; state == "idle" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.Equal(t, "idle", state, "agent should be idle after interrupt")
}

func TestAgentInfoReportsRunningTurnActive(t *testing.T) {
	started := make(chan struct{})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  &slowProvider{started: started},
	})
	defer a.Kill()

	submitPrompt(a, "take forever please")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never started")
	}
	assert.Equal(t, "active", a.Info().State)
	a.Interrupt()
}

// Interrupt on an idle agent emits nothing.
func TestAgent_InterruptWhenIdle(t *testing.T) {
	a := newTestAgent(t, "hi")
	defer a.Kill()

	ch, _ := subscribeChan(a)
	a.Interrupt()

	select {
	case n := <-ch:
		t.Fatalf("unexpected notification from idle interrupt: %+v", n)
	case <-time.After(150 * time.Millisecond):
	}
}

var _ = json.RawMessage(nil)

// composeTurn's window is a main-LT cursor, not an entry count, and the
// in-flight assembly yields to its appended copy; both bugs duplicate content.
func TestSecondTurnDoesNotRecomposePriorTurn(t *testing.T) {
	backend, id := backedConv(t, t.TempDir())
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}, nil))
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        id,
		Provider:  &streamingMockProvider{response: "ALPHA"},
		Backend:   backend,
		Form:      cb,
	})
	defer a.Kill()

	ch, closeCh := subscribeChan(a)
	defer closeCh()

	runTurn := func(prompt string) []rpc.Notification {
		submitPrompt(a, prompt)
		var out []rpc.Notification
		timeout := time.After(5 * time.Second)
		for {
			select {
			case n := <-ch:
				out = append(out, n)
				if n.Method == rpc.MethodTurnDone {
					return out
				}
			case <-timeout:
				t.Fatalf("turn timed out")
			}
		}
	}
	turn1 := runTurn("first prompt")
	turn2 := runTurn("second prompt")

	checkFrames := func(label string, frames []rpc.Notification) {
		for _, fr := range frames {
			b, _ := json.Marshal(fr.Params)
			if got := strings.Count(string(b), `"ALPHA"`); got > 1 {
				t.Fatalf("%s frame carries the reply %d times (dup): %s", label, got, string(b))
			}
		}
	}
	checkFrames("turn1", turn1)
	checkFrames("turn2", turn2)

	// Fold every frame through a real client: id churn lands as two node ids across frames.
	cl := client.NewClient()
	for _, fr := range append(append([]rpc.Notification{}, turn1...), turn2...) {
		if fr.Method != rpc.MethodAriaFrame {
			continue
		}
		b, _ := json.Marshal(fr.Params)
		var r aria.Page
		require.NoError(t, json.Unmarshal(b, &r))
		cl.Apply(r, client.Notify)
	}
	// A turn can close as several messages, so aggregate by turn before judging.
	byTurn := map[int][]livedoc.Node{}
	inquiry := map[int]string{}
	order := []int{}
	for _, m := range cl.View().Closed {
		if len(m.Nodes) == 0 {
			continue
		}
		if _, seen := byTurn[m.Turn]; !seen {
			order = append(order, m.Turn)
		}
		if m.Inquiry != "" {
			inquiry[m.Turn] = m.Inquiry
		}
		byTurn[m.Turn] = append(byTurn[m.Turn], m.Nodes...)
	}
	for _, lt := range order {
		nodes := byTurn[lt]
		if inquiry[lt] == "" {
			t.Fatalf("turn %d lost its inquiry", lt)
		}
		if nodes[0].Role == livedoc.RoleInput {
			t.Fatalf("turn %d node 0 speaks in the user's voice; the question is not a node", lt)
		}
		replies := 0
		for _, n := range nodes {
			if n.Role == livedoc.RoleOutput {
				replies++
			}
		}
		if replies != 1 {
			t.Fatalf("turn %d holds %d assistant nodes, want 1: %+v", lt, replies, nodes)
		}
	}
	require.Equal(t, 2, len(order))
	// The mantra metric legitimately echoes the first prompt.
	for _, fr := range turn2 {
		b, _ := json.Marshal(fr.Params)
		if strings.Contains(string(b), `"markdown":"first prompt"`) {
			t.Fatalf("turn 2 frame re-composed turn 1's prompt node: %s", string(b))
		}
	}
}

// The user's message arrives as one committed frame, never live-then-close.
func TestAgent_UserPromptCommitsWithoutLiveFrame(t *testing.T) {
	a := newTestAgent(t, "the reply")
	defer a.Kill()

	ch, unsub := subscribeChan(a)
	defer unsub()

	submitPrompt(a, "the question")

	deadline := time.After(5 * time.Second)
	var frames []aria.Page
loop:
	for {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodAriaFrame:
				frames = append(frames, n.Params.(aria.Page))
			case rpc.MethodTurnDone:
				break loop
			}
		case <-deadline:
			t.Fatal("timeout waiting for turn.done")
		}
	}

	sawInquiryCommitted := false
	for _, f := range frames {
		for _, part := range f.Parts {
			if part.Inquiry == "the question" && part.Live == nil {
				sawInquiryCommitted = true
			}
			for _, n := range part.Nodes {
				if n.Role == livedoc.RoleInput {
					t.Fatalf("the question is not a node; got %+v", n)
				}
			}
		}
	}
	require.True(t, sawInquiryCommitted, "expected the inquiry as committed turn text")

	for _, f := range frames {
		live := f.LiveTail()
		if live == nil {
			continue
		}
		for _, d := range live.Nodes {
			if d.Set["role"] == livedoc.RoleInput {
				t.Fatalf("the prompt must never stream as a Live delta; got %+v", d)
			}
		}
	}
}

// QueuedPrompts observes queued prompts in FIFO order without consuming them.
func TestAgent_QueuedPromptsRPC(t *testing.T) {
	release := make(chan struct{})
	prov := &blockedProvider{release: release}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Form:      cb,
	})
	defer func() { close(release); a.Kill() }()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"}) // drains into the running turn
	require.Eventually(t, func() bool { return prov.blocked() }, 2*time.Second, 10*time.Millisecond)

	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})
	a.SubmitPrompt(rpc.QuaRequest{Text: ""}) // carrier: must be omitted

	epoch, snap := a.QueuedPrompts(false)
	require.NotEmpty(t, epoch, "the queue view names the generation its ids belong to")
	require.Equal(t, []string{"one", "two"}, queuedTexts(snap))
	for _, p := range snap {
		require.NotZero(t, p.ID, "a queued message is addressable")
		require.Equal(t, rpc.QueueStateQueued, p.State)
	}

	epoch2, snap2 := a.QueuedPrompts(false)
	require.Equal(t, epoch, epoch2)
	require.Equal(t, snap, snap2)

	_, all := a.QueuedPrompts(true)
	require.Equal(t, []string{"one", "two", ""}, queuedTexts(all))
}

func queuedTexts(prompts []rpc.QueuedPrompt) []string {
	out := make([]string, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, p.Text)
	}
	return out
}

// blockedProvider parks in Send until release is closed.
type blockedProvider struct {
	release chan struct{}
	inSend  atomicBool
}

type atomicBool struct {
	mu sync.Mutex
	b  bool
}

func (a *atomicBool) set(v bool) { a.mu.Lock(); a.b = v; a.mu.Unlock() }
func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.b }

func (p *blockedProvider) Name() string        { return "blocked" }
func (p *blockedProvider) Fingerprint() string { return "blocked/v0" }
func (p *blockedProvider) SetModel(string)     {}
func (p *blockedProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *blockedProvider) blocked() bool { return p.inSend.get() }
func (p *blockedProvider) Send(ctx context.Context, _ provider.SendInput, _ provider.Bus) error {
	p.inSend.set(true)
	defer p.inSend.set(false)
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return ctx.Err()
}
