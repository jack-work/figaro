package figaro_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	ariaLog "github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

func newBackedConversation(t *testing.T) (*store.XwalBackend, string) {
	t.Helper()
	b, err := store.NewXwalBackend(t.TempDir())
	require.NoError(t, err)
	loadout, err := b.CreateLoadout("test", message.Patch{})
	require.NoError(t, err)
	id, err := b.CreateConversation(loadout)
	require.NoError(t, err)
	return b, id
}

func waitDone(t *testing.T, ch <-chan rpc.Notification) {
	t.Helper()
	waitDoneReason(t, ch)
}

func waitDoneReason(t *testing.T, ch <-chan rpc.Notification) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("turn did not finish")
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				return n.Params.(rpc.DoneEntry).Reason
			}
		}
	}
}

type idleProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *idleProvider) Name() string                                         { return "idle-test" }
func (p *idleProvider) Fingerprint() string                                  { return "idle-test/v1" }
func (p *idleProvider) SetModel(string)                                      {}
func (p *idleProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *idleProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil
}

func (p *idleProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestOpenAfterCrashBeforeAssistantSeal(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	ir, err := b.Open(id)
	require.NoError(t, err)
	_, err = ir.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("crashed mid-stream")},
	}})
	require.NoError(t, err)
	before := ir.Len()

	prov := &idleProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	got := a.Context()
	a.Kill()

	assert.Equal(t, before, ir.Len())
	assert.Equal(t, message.RoleUser, got[len(got)-1].Role)
	assert.Zero(t, prov.callCount())
}

func TestOpenAfterCrashWithUnresolvedTools(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	ir, err := b.Open(id)
	require.NoError(t, err)
	_, err = ir.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:       message.RoleAssistant,
		StopReason: message.StopToolInvoke,
		Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait",
			Arguments: map[string]any{},
		}},
	}})
	require.NoError(t, err)

	prov := &idleProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	first := a.Context()
	a.Kill()
	last := first[len(first)-1]
	require.Equal(t, message.RoleUser, last.Role)
	require.Len(t, last.Content, 1)
	assert.Equal(t, message.ContentToolResult, last.Content[0].Type)
	assert.Equal(t, "call-1", last.Content[0].ToolCallID)
	assert.True(t, last.Content[0].IsError)
	assert.Contains(t, last.Content[0].Text, "process died mid-turn")

	a = figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	second := a.Context()
	a.Kill()
	assert.Len(t, second, len(first), "repeated open must not double-repair")
	assert.Zero(t, prov.callCount())
}

type interruptProvider struct {
	mode    string
	started chan struct{}
}

func (p *interruptProvider) Name() string                                         { return "interrupt-test" }
func (p *interruptProvider) Fingerprint() string                                  { return "interrupt-test/v1" }
func (p *interruptProvider) SetModel(string)                                      {}
func (p *interruptProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *interruptProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	switch p.mode {
	case "prose":
		bus.PushDelta(message.TextContent("partial prose"))
	case "ready":
		bus.PushToolInvokeStart("call-1", "wait")
		bus.PushToolReady(message.Content{
			Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait", Arguments: map[string]any{},
		})
	case "tool":
		call := message.Content{
			Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait", Arguments: map[string]any{},
		}
		bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
		bus.PushToolReady(call)
		msg := message.Message{
			Role: message.RoleAssistant, StopReason: message.StopToolInvoke,
			Content: []message.Content{call}, Timestamp: time.Now().UnixMilli(),
		}
		if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
			return err
		}
		bus.PushFigaro(msg)
		return nil
	}
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}

type waitTool struct {
	started chan struct{}
}

func (t *waitTool) Name() string        { return "wait" }
func (t *waitTool) Description() string { return "waits for cancellation" }
func (t *waitTool) Parameters() any     { return map[string]any{"type": "object"} }
func (t *waitTool) Execute(ctx context.Context, _ map[string]any, out tool.OnOutput) ([]message.Content, error) {
	out([]byte("durable tool tail"))
	close(t.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestInterruptSealsPartialTurn(t *testing.T) {
	for _, mode := range []string{"empty", "prose", "ready", "tool"} {
		t.Run(mode, func(t *testing.T) {
			b, id := newBackedConversation(t)
			defer b.Close()
			prov := &interruptProvider{mode: mode, started: make(chan struct{})}
			registry := tool.NewRegistry()
			var toolStarted chan struct{}
			if mode == "tool" {
				toolStarted = make(chan struct{})
				registry.MustRegister(&waitTool{started: toolStarted})
			}
			a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: registry})
			defer a.Kill()
			ch, _ := subscribeChan(a)
			a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
			if mode == "tool" {
				select {
				case <-toolStarted:
				case <-time.After(5 * time.Second):
					t.Fatal("tool did not start")
				}
			} else {
				select {
				case <-prov.started:
				case <-time.After(5 * time.Second):
					t.Fatal("provider did not start")
				}
			}
			time.Sleep(100 * time.Millisecond)
			a.Interrupt()
			waitDone(t, ch)

			got := a.Context()
			require.NotEmpty(t, got)
			switch mode {
			case "empty":
				last := got[len(got)-1]
				assert.Equal(t, message.RoleUser, last.Role, "empty partial leaves the turn absent")
				assert.Equal(t, "go", last.Content[0].Text)
			case "prose":
				last := got[len(got)-1]
				assert.Equal(t, message.StopAborted, last.StopReason)
				assert.Equal(t, "partial prose", last.Content[0].Text)
			case "ready":
				require.GreaterOrEqual(t, len(got), 2)
				assert.Equal(t, message.StopAborted, got[len(got)-2].StopReason)
				assert.Equal(t, message.ContentToolResult, got[len(got)-1].Content[0].Type)
			case "tool":
				last := got[len(got)-1]
				assert.Equal(t, message.ContentToolResult, last.Content[0].Type)
				assert.Contains(t, last.Content[0].Text, "durable tool tail")
			}
		})
	}
}

type mixedToolProvider struct {
	done    <-chan struct{}
	running <-chan struct{}
	release chan struct{}
}

func (*mixedToolProvider) Name() string                                         { return "mixed-tools" }
func (*mixedToolProvider) Fingerprint() string                                  { return "mixed-tools/v1" }
func (*mixedToolProvider) SetModel(string)                                      {}
func (*mixedToolProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *mixedToolProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	calls := []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: "done", ToolName: "mixed-tool", Arguments: map[string]any{"mode": "done"}},
		{Type: message.ContentToolInvoke, ToolCallID: "running", ToolName: "mixed-tool", Arguments: map[string]any{"mode": "running"}},
	}
	for _, call := range calls {
		bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
		bus.PushToolReady(call)
	}
	<-p.done
	<-p.running
	<-p.release
	msg := message.Message{
		Role: message.RoleAssistant, StopReason: message.StopToolInvoke,
		Content: calls, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

type mixedTool struct {
	done        chan struct{}
	running     chan struct{}
	doneOnce    sync.Once
	runningOnce sync.Once
}

func (*mixedTool) Name() string        { return "mixed-tool" }
func (*mixedTool) Description() string { return "mixed outcome tool" }
func (*mixedTool) Parameters() any     { return map[string]any{"type": "object"} }
func (t *mixedTool) Execute(ctx context.Context, args map[string]any, out tool.OnOutput) ([]message.Content, error) {
	if args["mode"] == "done" {
		out([]byte("completed progress"))
		t.doneOnce.Do(func() { close(t.done) })
		return nil, errors.New("terminal failure")
	}
	out([]byte("running progress"))
	t.runningOnce.Do(func() { close(t.running) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestInterruptPreservesCompletedAndRunningToolStates(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	impl := &mixedTool{done: make(chan struct{}), running: make(chan struct{})}
	registry := tool.NewRegistry()
	registry.MustRegister(impl)
	prov := &mixedToolProvider{done: impl.done, running: impl.running, release: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: registry})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	select {
	case <-impl.done:
	case <-time.After(5 * time.Second):
		t.Fatal("completed tool did not run")
	}
	select {
	case <-impl.running:
	case <-time.After(5 * time.Second):
		t.Fatal("running tool did not stream")
	}
	time.Sleep(200 * time.Millisecond)
	a.Interrupt()
	close(prov.release)
	waitDone(t, ch)

	history := a.Context()
	results := history[len(history)-1].Content
	require.Len(t, results, 2)
	assert.Contains(t, results[0].Text, "completed progress")
	assert.Contains(t, results[0].Text, "terminal failure")
	assert.NotContains(t, results[0].Text, "did not complete")
	assert.True(t, results[0].IsError)
	assert.Contains(t, results[1].Text, "running progress")
	assert.Contains(t, results[1].Text, "did not complete")
	assert.True(t, results[1].IsError)
}

type delayedDeltaProvider struct {
	calls int
}

func (p *delayedDeltaProvider) Name() string                                         { return "delayed" }
func (p *delayedDeltaProvider) Fingerprint() string                                  { return "delayed/v1" }
func (p *delayedDeltaProvider) SetModel(string)                                      {}
func (p *delayedDeltaProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *delayedDeltaProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	p.calls++
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	bus.PushDelta(message.TextContent("streamed before panic"))
	<-ctx.Done()
	return ctx.Err()
}

type panicOnceNotifier struct {
	mu       sync.Mutex
	panicked bool
}

func (n *panicOnceNotifier) Notify(method string, params any) error {
	if method != rpc.MethodAriaFrame {
		return nil
	}
	read, ok := params.(ariaLog.AriaRead)
	if !ok || read.Live == nil || read.Live.Role != "assistant" {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.panicked {
		n.panicked = true
		panic("panic after live frame")
	}
	return nil
}

func TestPanicRecoverySealsInMemoryPartial(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	prov := &delayedDeltaProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	defer a.Kill()
	a.Subscribe(&panicOnceNotifier{})
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "crashed and was restarted")

	got := a.Context()
	require.NotEmpty(t, got)
	last := got[len(got)-1]
	assert.Equal(t, message.RoleAssistant, last.Role)
	assert.Equal(t, message.StopAborted, last.StopReason)
	assert.Equal(t, "streamed before panic", last.Content[0].Text)
}

type canonicalThenFrameProvider struct{}

func (canonicalThenFrameProvider) Name() string        { return "canonical-frame" }
func (canonicalThenFrameProvider) Fingerprint() string { return "canonical-frame/v1" }
func (canonicalThenFrameProvider) SetModel(string)     {}
func (canonicalThenFrameProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (canonicalThenFrameProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("canonical assistant")},
		StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

func TestPanicAfterIRBeforeLiveCommitReconcilesCanonicalAssistant(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: canonicalThenFrameProvider{}, Backend: b, Tools: tool.NewRegistry(),
	})
	defer a.Kill()
	a.Subscribe(&panicOnceNotifier{})
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "crashed and was restarted")

	read := a.Read(0)
	require.Nil(t, read.Live)
	require.NotEmpty(t, read.Committed)
	last := read.Committed[len(read.Committed)-1]
	assert.Equal(t, "assistant", last.Role)
	require.NotEmpty(t, last.Nodes)
	assert.Contains(t, last.Nodes[0].Markdown, "canonical assistant")
}

type panicQueueProvider struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (*panicQueueProvider) Name() string                                         { return "panic-queue" }
func (*panicQueueProvider) Fingerprint() string                                  { return "panic-queue/v1" }
func (*panicQueueProvider) SetModel(string)                                      {}
func (*panicQueueProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *panicQueueProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.calls.Add(1) == 1 {
		close(p.started)
		<-p.release
		bus.PushDelta(message.TextContent("panic frame"))
		<-ctx.Done()
		return ctx.Err()
	}
	msg := message.Message{
		Role: message.RoleAssistant, StopReason: message.StopEnd,
		Content: []message.Content{message.TextContent("queued prompt completed")}, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

type queuePanicNotifier struct {
	once     sync.Once
	panicked chan struct{}
}

func (n *queuePanicNotifier) Notify(method string, params any) error {
	if method != rpc.MethodAriaFrame {
		return nil
	}
	read, ok := params.(ariaLog.AriaRead)
	if !ok || read.Live == nil || read.Live.Role != "assistant" {
		return nil
	}
	n.once.Do(func() {
		close(n.panicked)
		panic("queued-event recovery panic")
	})
	return nil
}

func TestPanicRecoveryPreservesQueuedPromptAndFork(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	prov := &panicQueueProvider{started: make(chan struct{}), release: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	defer a.Kill()
	notifier := &queuePanicNotifier{panicked: make(chan struct{})}
	a.Subscribe(notifier)
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "first"})
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first provider round did not start")
	}

	a.SubmitPrompt(rpc.QuaRequest{Text: "second"})
	forkRan := make(chan struct{})
	forkDone := make(chan error, 1)
	go func() {
		forkDone <- a.CoordinateFork(func() error {
			close(forkRan)
			return nil
		})
	}()
	close(prov.release)
	select {
	case <-notifier.panicked:
	case <-time.After(5 * time.Second):
		t.Fatal("notifier did not panic")
	}
	select {
	case <-forkRan:
	case <-time.After(5 * time.Second):
		t.Fatal("queued fork was not serviced after panic")
	}
	require.NoError(t, <-forkDone)

	deadline := time.After(5 * time.Second)
	done := 0
	for done < 2 {
		select {
		case <-deadline:
			t.Fatalf("received %d turn completions, want panic and queued prompt", done)
		case notification := <-ch:
			if notification.Method == rpc.MethodTurnDone {
				done++
			}
		}
	}
	history := a.Context()
	var sawSecond, sawCompletion bool
	for _, msg := range history {
		for _, content := range msg.Content {
			sawSecond = sawSecond || content.Text == "second"
			sawCompletion = sawCompletion || content.Text == "queued prompt completed"
		}
	}
	assert.True(t, sawSecond)
	assert.True(t, sawCompletion)
}

type mismatchLog struct {
	store.Log[message.Message]
}

func (l mismatchLog) Append(entry store.Entry[message.Message]) (store.Entry[message.Message], error) {
	stamped, err := l.Log.Append(entry)
	stamped.LT++
	stamped.FigaroLT++
	return stamped, err
}

type mismatchBackend struct {
	store.Backend
	log store.Log[message.Message]
}

func (b mismatchBackend) Open(string) (store.Log[message.Message], error) {
	return b.log, nil
}

func TestAssistantSealRejectsPredictedLTMismatch(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	base, err := real.Open(id)
	require.NoError(t, err)
	backend := mismatchBackend{Backend: real, log: mismatchLog{Log: base}}
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: canonicalThenFrameProvider{}, Backend: backend, Tools: tool.NewRegistry(),
	})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "assistant seal LT mismatch")
	history := a.Context()
	require.NotEmpty(t, history)
	assert.Equal(t, message.RoleAssistant, history[len(history)-1].Role)
}

type nativeCommitProvider struct {
	namespace   string
	payload     []json.RawMessage
	fingerprint string
}

func (p *nativeCommitProvider) Name() string                                         { return p.namespace }
func (p *nativeCommitProvider) Fingerprint() string                                  { return p.fingerprint }
func (p *nativeCommitProvider) SetModel(string)                                      {}
func (p *nativeCommitProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *nativeCommitProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("sealed")},
		StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg, provider.AssistantCache{
		Namespace: p.namespace, Payload: p.payload, Fingerprint: p.fingerprint,
	})
	return nil
}

type failingAssistantCacheLog struct {
	store.Log[[]json.RawMessage]
}

func (l failingAssistantCacheLog) Append(store.Entry[[]json.RawMessage]) (store.Entry[[]json.RawMessage], error) {
	return store.Entry[[]json.RawMessage]{}, errors.New("native cache unavailable")
}

type failingAssistantCacheBackend struct {
	store.Backend
	namespace string
}

func (b failingAssistantCacheBackend) OpenTranslation(ariaID, namespace string) (store.Log[[]json.RawMessage], error) {
	log, err := b.Backend.OpenTranslation(ariaID, namespace)
	if err != nil || namespace != b.namespace {
		return log, err
	}
	return failingAssistantCacheLog{Log: log}, nil
}

func TestCacheAppendFailureEndsTurnKeepsAssistant(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	payload := []json.RawMessage{json.RawMessage(`{"encrypted_content":"enc-opaque","type":"reasoning"}`)}
	prov := &nativeCommitProvider{namespace: "atomic-cache", payload: payload, fingerprint: "atomic-cache/v1"}
	failing := failingAssistantCacheBackend{Backend: real, namespace: "atomic-cache"}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: failing, Tools: tool.NewRegistry()})
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	a.Kill()
	assert.Contains(t, reason, "native cache unavailable")

	ir, err := real.Open(id)
	require.NoError(t, err)
	tail, ok := ir.PeekTail()
	require.True(t, ok)
	assert.Equal(t, message.RoleAssistant, tail.Payload.Role)
	cache, err := real.OpenTranslation(id, "atomic-cache")
	require.NoError(t, err)
	_, ok = cache.Lookup(tail.LT)
	assert.False(t, ok)

	prov2 := &idleProvider{}
	a = figaro.NewAgent(figaro.Config{ID: id, Provider: prov2, Backend: real, Tools: tool.NewRegistry()})
	got := a.Context()
	a.Kill()
	assert.Equal(t, message.RoleAssistant, got[len(got)-1].Role, "next open sees the canonical assistant, unblocked")
	assert.Zero(t, prov2.callCount())
}

type sealBarrierProvider struct {
	afterAck chan struct{}
	release  chan struct{}
}

func (p *sealBarrierProvider) Name() string                                         { return "seal-barrier" }
func (p *sealBarrierProvider) Fingerprint() string                                  { return "seal-barrier/v1" }
func (p *sealBarrierProvider) SetModel(string)                                      {}
func (p *sealBarrierProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *sealBarrierProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("sealed")},
		StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg, provider.AssistantCache{
		Namespace:   "seal-barrier",
		Payload:     []json.RawMessage{json.RawMessage(`{"native":"sealed"}`)},
		Fingerprint: p.Fingerprint(),
	})
	close(p.afterAck)
	<-p.release
	return nil
}

func TestQueuedForkWaitsForProviderCacheSeal(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	prov := &sealBarrierProvider{afterAck: make(chan struct{}), release: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	select {
	case <-prov.afterAck:
	case <-time.After(5 * time.Second):
		t.Fatal("assistant IR was not acknowledged")
	}

	var cont, alt string
	forkDone := make(chan error, 1)
	go func() {
		forkDone <- a.CoordinateFork(func() error {
			var err error
			cont, alt, err = b.Fork(id)
			return err
		})
	}()
	select {
	case err := <-forkDone:
		t.Fatalf("fork crossed provider cache barrier: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(prov.release)
	require.NoError(t, <-forkDone)
	waitDone(t, ch)

	for _, branch := range []string{cont, alt} {
		ir, err := b.Open(branch)
		require.NoError(t, err)
		tail, ok := ir.PeekTail()
		require.True(t, ok)
		assert.Equal(t, message.RoleAssistant, tail.Payload.Role)
		cache, err := b.OpenTranslation(branch, "seal-barrier")
		require.NoError(t, err)
		cached, ok := cache.Lookup(tail.LT)
		require.True(t, ok, "cache missing on branch %s", branch)
		assert.JSONEq(t, `{"native":"sealed"}`, string(cached.Payload[0]))
	}
}
