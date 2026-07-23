package figaro_test

import (
	"context"
	"encoding/json"
	"sync"
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

type cacheAfterCommitProvider struct {
	mu            sync.Mutex
	cacheAdvanced bool
}

func (p *cacheAfterCommitProvider) Name() string        { return "cache-after-commit" }
func (p *cacheAfterCommitProvider) Fingerprint() string { return "cache-after-commit/v1" }
func (p *cacheAfterCommitProvider) SetModel(string)     {}
func (p *cacheAfterCommitProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *cacheAfterCommitProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("success")},
		StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg, provider.AssistantCache{
		Namespace:   "cache-after-commit",
		Payload:     []json.RawMessage{json.RawMessage(`{"native":"success"}`)},
		Fingerprint: p.Fingerprint(),
	})
	p.mu.Lock()
	p.cacheAdvanced = true
	p.mu.Unlock()
	return nil
}

func (p *cacheAfterCommitProvider) advanced() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cacheAdvanced
}

func TestJournalFailureDoesNotAdvanceProviderCache(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	prov := &cacheAfterCommitProvider{}
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: prov,
		Backend: journalBackend{Backend: real, journal: &failingJournal{failAt: 2}},
		Tools:   tool.NewRegistry(), Chalkboard: mustChalkboard(t),
	})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "checkpoint failed")
	assert.False(t, prov.advanced(), "provider cache must wait for actor commit acknowledgement")
	history := a.Context()
	require.NotEmpty(t, history)
	assert.Equal(t, message.StopAborted, history[len(history)-1].StopReason)
}
