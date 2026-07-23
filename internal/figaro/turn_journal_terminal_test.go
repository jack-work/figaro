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
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

func TestTurnJournalRecoveryPreservesCompletedTools(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	ir, err := b.Open(id)
	require.NoError(t, err)
	calls := []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: "ok", ToolName: "wait", Arguments: map[string]any{}},
		{Type: message.ContentToolInvoke, ToolCallID: "err", ToolName: "wait", Arguments: map[string]any{}},
		{Type: message.ContentToolInvoke, ToolCallID: "run", ToolName: "wait", Arguments: map[string]any{}},
	}
	assistant, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleAssistant, StopReason: message.StopToolInvoke, Content: calls,
	}})
	require.NoError(t, err)
	target := assistant.LT + 1
	journal, err := b.OpenTurnJournal(id)
	require.NoError(t, err)
	require.NoError(t, journal.Checkpoint(target, checkpointJSON(
		t, target, "tools", assistant.Payload, []map[string]any{
			{"tool_call_id": "ok", "tool_name": "wait", "status": "ok", "output_tail": "completed output"},
			{"tool_call_id": "err", "tool_name": "wait", "status": "error", "output_tail": "completed error", "is_error": true},
			{"tool_call_id": "run", "tool_name": "wait", "status": "running", "output_tail": "partial output"},
		},
	)))
	require.NoError(t, journal.Sync())

	prov := &recoveryProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	history := a.Context()
	a.Kill()
	results := history[len(history)-1].Content
	require.Len(t, results, 3)
	assert.Equal(t, "completed output", results[0].Text)
	assert.False(t, results[0].IsError)
	assert.NotContains(t, results[0].Text, "did not complete")
	assert.Equal(t, "completed error", results[1].Text)
	assert.True(t, results[1].IsError)
	assert.NotContains(t, results[1].Text, "did not complete")
	assert.Contains(t, results[2].Text, "partial output")
	assert.Contains(t, results[2].Text, "did not complete")
	assert.True(t, results[2].IsError)
	assert.Zero(t, prov.callCount())
}

type mixedToolProvider struct {
	done    <-chan struct{}
	running <-chan struct{}
}

func (*mixedToolProvider) Name() string        { return "mixed-tools" }
func (*mixedToolProvider) Fingerprint() string { return "mixed-tools/v1" }
func (*mixedToolProvider) SetModel(string)     {}
func (*mixedToolProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
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
	time.Sleep(50 * time.Millisecond)
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

type mixedCheckpointTool struct {
	done        chan struct{}
	running     chan struct{}
	doneOnce    sync.Once
	runningOnce sync.Once
}

func (*mixedCheckpointTool) Name() string        { return "mixed-tool" }
func (*mixedCheckpointTool) Description() string { return "mixed checkpoint tool" }
func (*mixedCheckpointTool) Parameters() any     { return map[string]any{"type": "object"} }
func (t *mixedCheckpointTool) Execute(ctx context.Context, args map[string]any, out tool.OnOutput) ([]message.Content, error) {
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
	impl := &mixedCheckpointTool{done: make(chan struct{}), running: make(chan struct{})}
	registry := tool.NewRegistry()
	registry.MustRegister(impl)
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: &mixedToolProvider{done: impl.done, running: impl.running}, Backend: b, Tools: registry,
	})
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
	ir, err := b.Open(id)
	require.NoError(t, err)
	journal, err := b.OpenTurnJournal(id)
	require.NoError(t, err)
	var observed string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tail, ok := ir.PeekTail()
		if !ok {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		payload, ok, err := journal.Latest(tail.LT + 1)
		if err != nil || !ok {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		var cp struct {
			Tools []struct {
				ToolCallID string `json:"tool_call_id"`
				Status     string `json:"status"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(payload, &cp); err != nil {
			observed = err.Error()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		observed = string(payload)
		if len(cp.Tools) == 2 &&
			cp.Tools[0].ToolCallID == "done" && cp.Tools[0].Status == "error" &&
			cp.Tools[1].ToolCallID == "running" && cp.Tools[1].Status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Less(t, time.Now(), deadline, "last checkpoint: %s", observed)
	a.Interrupt()
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

type terminalSyncJournal struct {
	mu             sync.Mutex
	latest         []byte
	terminalSynced bool
}

func (j *terminalSyncJournal) Checkpoint(_ uint64, payload []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.latest = append(j.latest[:0], payload...)
	return nil
}

func (j *terminalSyncJournal) Sync() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var cp struct {
		Tools []struct {
			Status string `json:"status"`
		} `json:"tools"`
	}
	if json.Unmarshal(j.latest, &cp) == nil && len(cp.Tools) > 0 {
		terminal := true
		for _, state := range cp.Tools {
			terminal = terminal && (state.Status == "ok" || state.Status == "error")
		}
		j.terminalSynced = terminal
	}
	return nil
}

func (j *terminalSyncJournal) Latest(uint64) ([]byte, bool, error) { return nil, false, nil }
func (j *terminalSyncJournal) Retire() error                       { return nil }

func (j *terminalSyncJournal) isTerminalSynced() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.terminalSynced
}

type terminalSyncLog struct {
	store.Log[message.Message]
	journal *terminalSyncJournal
}

func (l terminalSyncLog) Append(entry store.Entry[message.Message]) (store.Entry[message.Message], error) {
	for _, content := range entry.Payload.Content {
		if content.Type == message.ContentToolResult && !l.journal.isTerminalSynced() {
			return store.Entry[message.Message]{}, errors.New("tool results appended before terminal checkpoint sync")
		}
	}
	return l.Log.Append(entry)
}

type terminalSyncBackend struct {
	store.Backend
	journal *terminalSyncJournal
}

func (b terminalSyncBackend) Open(id string) (store.Log[message.Message], error) {
	log, err := b.Backend.Open(id)
	if err != nil {
		return nil, err
	}
	return terminalSyncLog{Log: log, journal: b.journal}, nil
}

func (b terminalSyncBackend) OpenTurnJournal(string) (store.TurnJournal, error) {
	return b.journal, nil
}

type singleToolProvider struct {
	calls atomic.Int32
}

func (*singleToolProvider) Name() string        { return "single-tool" }
func (*singleToolProvider) Fingerprint() string { return "single-tool/v1" }
func (*singleToolProvider) SetModel(string)     {}
func (*singleToolProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *singleToolProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.calls.Add(1) > 1 {
		msg := message.Message{
			Role: message.RoleAssistant, StopReason: message.StopEnd,
			Content: []message.Content{message.TextContent("finished")}, Timestamp: time.Now().UnixMilli(),
		}
		if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
			return err
		}
		bus.PushFigaro(msg)
		return nil
	}
	call := message.Content{Type: message.ContentToolInvoke, ToolCallID: "call", ToolName: "instant", Arguments: map[string]any{}}
	bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
	bus.PushToolReady(call)
	msg := message.Message{Role: message.RoleAssistant, StopReason: message.StopToolInvoke, Content: []message.Content{call}, Timestamp: time.Now().UnixMilli()}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

type instantTool struct{}

func (instantTool) Name() string        { return "instant" }
func (instantTool) Description() string { return "returns immediately" }
func (instantTool) Parameters() any     { return map[string]any{"type": "object"} }
func (instantTool) Execute(context.Context, map[string]any, tool.OnOutput) ([]message.Content, error) {
	return []message.Content{message.TextContent("done")}, nil
}

func TestTerminalToolCheckpointSyncPrecedesCanonicalResults(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	journal := &terminalSyncJournal{}
	backend := terminalSyncBackend{Backend: real, journal: journal}
	registry := tool.NewRegistry()
	registry.MustRegister(instantTool{})
	provider := &singleToolProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: provider, Backend: backend, Tools: registry})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.NotContains(t, reason, "tool results appended before terminal checkpoint sync")
	assert.True(t, journal.isTerminalSynced())
	history := a.Context()
	require.NotEmpty(t, history)
	sawToolResult := false
	for _, msg := range history {
		for _, content := range msg.Content {
			sawToolResult = sawToolResult || content.Type == message.ContentToolResult
		}
	}
	assert.True(t, sawToolResult)
}
