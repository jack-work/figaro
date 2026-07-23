package figaro_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	ariaLog "github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

type recoveryProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *recoveryProvider) Name() string        { return "recovery-test" }
func (p *recoveryProvider) Fingerprint() string { return "recovery-test/v1" }
func (p *recoveryProvider) SetModel(string)     {}
func (p *recoveryProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *recoveryProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil
}

func (p *recoveryProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

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

func checkpointJSON(t *testing.T, target uint64, phase string, assistant message.Message, tools []map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"version":           2,
		"turn_id":           "turn-test",
		"generation":        1,
		"target_next_ir_lt": target,
		"phase":             phase,
		"partial_assistant": assistant,
		"tools":             tools,
		"timestamp_ms":      time.Now().UnixMilli(),
	}
	out, err := json.Marshal(payload)
	require.NoError(t, err)
	return out
}

func TestTurnJournalRestartRecovery(t *testing.T) {
	tests := []struct {
		name      string
		assistant message.Message
		tools     []map[string]any
		wantRoles []message.Role
	}{
		{
			name:      "before first token",
			assistant: message.Message{Role: message.RoleAssistant},
			wantRoles: []message.Role{message.RoleAssistant},
		},
		{
			name: "mid prose",
			assistant: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.Content{message.TextContent("durable partial")},
			},
			wantRoles: []message.Role{message.RoleAssistant},
		},
		{
			name: "ready tool invoke",
			assistant: message.Message{
				Role: message.RoleAssistant,
				Content: []message.Content{{
					Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait",
					Arguments: map[string]any{"x": "y"},
				}},
			},
			tools: []map[string]any{{
				"tool_call_id": "call-1", "tool_name": "wait", "status": "running",
			}},
			wantRoles: []message.Role{message.RoleAssistant, message.RoleUser},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, id := newBackedConversation(t)
			defer b.Close()
			ir, err := b.Open(id)
			require.NoError(t, err)
			target := ir.Read()[len(ir.Read())-1].FigaroLT + 1
			journal, err := b.OpenTurnJournal(id)
			require.NoError(t, err)
			require.NoError(t, journal.Checkpoint(target, checkpointJSON(t, target, "assistant", tt.assistant, tt.tools)))
			require.NoError(t, journal.Sync())

			prov := &recoveryProvider{}
			a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
			got := a.Context()
			a.Kill()

			require.GreaterOrEqual(t, len(got), len(tt.wantRoles))
			got = got[len(got)-len(tt.wantRoles):]
			for i, role := range tt.wantRoles {
				assert.Equal(t, role, got[i].Role)
			}
			assert.Equal(t, message.StopAborted, got[0].StopReason)
			if tt.name == "mid prose" {
				assert.Equal(t, "durable partial", got[0].Content[0].Text)
			}
			if len(tt.tools) > 0 {
				result := got[len(got)-1].Content[0]
				assert.True(t, result.IsError)
				assert.Contains(t, result.Text, "did not complete")
			}
			assert.Zero(t, prov.callCount(), "recovery must not re-call the provider")
		})
	}
}

func TestTurnJournalRestartAfterAssistantAppendAndRepeatedRecovery(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	ir, err := b.Open(id)
	require.NoError(t, err)
	assistant := message.Message{
		Role:       message.RoleAssistant,
		StopReason: message.StopToolInvoke,
		Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait",
			Arguments: map[string]any{},
		}},
	}
	sealed, err := ir.Append(store.Entry[message.Message]{Payload: assistant})
	require.NoError(t, err)
	target := sealed.FigaroLT + 1
	journal, err := b.OpenTurnJournal(id)
	require.NoError(t, err)
	require.NoError(t, journal.Checkpoint(target, checkpointJSON(t, target, "tools", assistant, []map[string]any{{
		"tool_call_id": "call-1", "tool_name": "wait", "status": "running", "output_tail": "last output",
	}})))
	require.NoError(t, journal.Sync())

	prov := &recoveryProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	first := a.Context()
	a.Kill()
	require.Equal(t, message.RoleUser, first[len(first)-1].Role)
	assert.Contains(t, first[len(first)-1].Content[0].Text, "last output")

	a = figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	second := a.Context()
	a.Kill()
	assert.Len(t, second, len(first), "repeated recovery must be idempotent")
	assert.Zero(t, prov.callCount())
}

type interruptProvider struct {
	mode    string
	started chan struct{}
}

func (p *interruptProvider) Name() string        { return "interrupt-test" }
func (p *interruptProvider) Fingerprint() string { return "interrupt-test/v1" }
func (p *interruptProvider) SetModel(string)     {}
func (p *interruptProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
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
		_, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
		if err != nil {
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

func TestInterruptSealsDurablePartialTurn(t *testing.T) {
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
				assert.Equal(t, message.StopAborted, got[len(got)-1].StopReason)
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

func waitDone(t *testing.T, ch <-chan rpc.Notification) {
	t.Helper()
	select {
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
	case n := <-ch:
		for n.Method != rpc.MethodTurnDone {
			select {
			case <-time.After(5 * time.Second):
				t.Fatal("turn did not finish")
			case n = <-ch:
			}
		}
	}
}

type failingJournal struct {
	failCheckpoint bool
	failAt         int
	failAfter      int
	checkpoints    int
	failSync       bool
}

func (j *failingJournal) Checkpoint(uint64, []byte) error {
	j.checkpoints++
	if j.failCheckpoint || j.failAt == j.checkpoints || (j.failAfter > 0 && j.checkpoints >= j.failAfter) {
		return errors.New("checkpoint failed")
	}
	return nil
}
func (j *failingJournal) Sync() error {
	if j.failSync {
		return errors.New("sync failed")
	}
	return nil
}
func (j *failingJournal) Latest(uint64) ([]byte, bool, error) { return nil, false, nil }
func (j *failingJournal) Retire() error                       { return nil }

type journalBackend struct {
	store.Backend
	journal store.TurnJournal
}

func (b journalBackend) OpenTurnJournal(string) (store.TurnJournal, error) {
	return b.journal, nil
}

type delayedDeltaProvider struct {
	calls int
}

func (p *delayedDeltaProvider) Name() string        { return "delayed" }
func (p *delayedDeltaProvider) Fingerprint() string { return "delayed/v1" }
func (p *delayedDeltaProvider) SetModel(string)     {}
func (p *delayedDeltaProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *delayedDeltaProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	p.calls++
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1100 * time.Millisecond):
	}
	bus.PushDelta(message.TextContent("must be suppressed"))
	<-ctx.Done()
	return ctx.Err()
}

func TestJournalFailureSuppressesLiveOutputAndEndsError(t *testing.T) {
	tests := []struct {
		name       string
		journal    *failingJournal
		wantCalls  int
		wantReason string
	}{
		{"append", &failingJournal{failCheckpoint: true}, 0, "checkpoint failed"},
		{"sync", &failingJournal{failSync: true}, 1, "sync failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			real, id := newBackedConversation(t)
			defer real.Close()
			prov := &delayedDeltaProvider{}
			a := figaro.NewAgent(figaro.Config{
				ID: id, Provider: prov, Backend: journalBackend{Backend: real, journal: tt.journal},
				Tools: tool.NewRegistry(), Chalkboard: mustChalkboard(t),
			})
			defer a.Kill()
			ch, _ := subscribeChan(a)
			a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
			reason := waitDoneReason(t, ch)
			assert.Contains(t, reason, tt.wantReason)
			assert.Equal(t, tt.wantCalls, prov.calls)
			for _, msg := range a.Context() {
				for _, content := range msg.Content {
					assert.NotContains(t, content.Text, "must be suppressed")
				}
			}

		})
	}
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
		panic("panic after durable live frame")
	}
	return nil
}

func TestPanicRecoverySealsLatestDurableCheckpoint(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	prov := &delayedDeltaProvider{}
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: prov, Backend: real, Tools: tool.NewRegistry(), Chalkboard: mustChalkboard(t),
	})
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
	assert.Equal(t, "must be suppressed", last.Content[0].Text)
}

func mustChalkboard(t *testing.T) *chalkboard.State {
	t.Helper()
	cb, err := chalkboard.Open("")
	require.NoError(t, err)
	return cb
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
