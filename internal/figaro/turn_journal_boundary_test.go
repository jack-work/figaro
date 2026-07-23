package figaro_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

func TestTurnJournalRecoveryAcrossAssistantAppendBoundary(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	ir, err := b.Open(id)
	require.NoError(t, err)
	tail, ok := ir.PeekTail()
	require.True(t, ok)

	assistantTarget := tail.FigaroLT + 1
	toolTarget := assistantTarget + 1
	assistant := message.Message{
		Role:       message.RoleAssistant,
		StopReason: message.StopToolInvoke,
		Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait",
			Arguments: map[string]any{},
		}},
	}

	journal, err := b.OpenTurnJournal(id)
	require.NoError(t, err)
	require.NoError(t, journal.Checkpoint(toolTarget, checkpointJSON(
		t, toolTarget, "tools", assistant, []map[string]any{{
			"tool_call_id": "call-1", "tool_name": "wait", "status": "pending",
		}},
	)))
	require.NoError(t, journal.Sync())

	prov := &recoveryProvider{}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	history := a.Context()
	a.Kill()

	require.GreaterOrEqual(t, len(history), 2)
	recovered := history[len(history)-2:]
	assert.Equal(t, message.RoleAssistant, recovered[0].Role)
	assert.Equal(t, message.StopAborted, recovered[0].StopReason)
	assert.Equal(t, message.RoleUser, recovered[1].Role)
	assert.Equal(t, message.ContentToolResult, recovered[1].Content[0].Type)
	assert.Zero(t, prov.callCount())
}

type failRecoveredToolResultLog struct {
	store.Log[message.Message]
	mu     *sync.Mutex
	failed *bool
}

func (l failRecoveredToolResultLog) Append(entry store.Entry[message.Message]) (store.Entry[message.Message], error) {
	for _, content := range entry.Payload.Content {
		if content.Type != message.ContentToolResult {
			continue
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if !*l.failed {
			*l.failed = true
			return store.Entry[message.Message]{}, errors.New("crash after recovered assistant")
		}
	}
	return l.Log.Append(entry)
}

type failRecoveredToolResultBackend struct {
	store.Backend
	mu     sync.Mutex
	failed bool
}

func (b *failRecoveredToolResultBackend) Open(id string) (store.Log[message.Message], error) {
	log, err := b.Backend.Open(id)
	if err != nil {
		return nil, err
	}
	return failRecoveredToolResultLog{Log: log, mu: &b.mu, failed: &b.failed}, nil
}

func TestRecoveryHandoffSurvivesSecondCrash(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	ir, err := real.Open(id)
	require.NoError(t, err)
	tail, ok := ir.PeekTail()
	require.True(t, ok)
	target := tail.FigaroLT + 1
	assistant := message.Message{
		Role: message.RoleAssistant, StopReason: message.StopToolInvoke,
		Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "wait", Arguments: map[string]any{},
		}},
	}
	journal, err := real.OpenTurnJournal(id)
	require.NoError(t, err)
	require.NoError(t, journal.Checkpoint(target, checkpointJSON(
		t, target, "assistant", assistant, []map[string]any{{
			"tool_call_id": "call-1", "tool_name": "wait", "status": "ok", "output_tail": "completed output",
		}},
	)))
	require.NoError(t, journal.Sync())

	failing := &failRecoveredToolResultBackend{Backend: real}
	first := figaro.NewAgent(figaro.Config{ID: id, Provider: &recoveryProvider{}, Backend: failing, Tools: tool.NewRegistry()})
	first.Kill()
	history := first.Context()
	require.NotEmpty(t, history)
	assert.Equal(t, message.StopAborted, history[len(history)-1].StopReason)

	secondProv := &recoveryProvider{}
	second := figaro.NewAgent(figaro.Config{ID: id, Provider: secondProv, Backend: real, Tools: tool.NewRegistry()})
	history = second.Context()
	second.Kill()
	assert.Zero(t, secondProv.callCount())
	last := history[len(history)-1]
	require.Len(t, last.Content, 1)
	assert.Equal(t, message.ContentToolResult, last.Content[0].Type)
	assert.Equal(t, "completed output", last.Content[0].Text)
	assert.False(t, last.Content[0].IsError)
}
