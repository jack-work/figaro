package figaro_test

import (
	"context"
	"encoding/json"
	"errors"
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

type nativeCommitProvider struct {
	namespace   string
	payload     []json.RawMessage
	fingerprint string
	calls       int
}

func (p *nativeCommitProvider) Name() string        { return p.namespace }
func (p *nativeCommitProvider) Fingerprint() string { return p.fingerprint }
func (p *nativeCommitProvider) SetModel(string)     {}
func (p *nativeCommitProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *nativeCommitProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	p.calls++
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

func commitCheckpointPayload(t *testing.T, assistantLT uint64, msg message.Message, namespace, fingerprint string, payload []json.RawMessage) []byte {
	t.Helper()
	out, err := json.Marshal(map[string]any{
		"version":           2,
		"turn_id":           "recovery-commit",
		"generation":        1,
		"target_next_ir_lt": assistantLT + 1,
		"phase":             "assistant",
		"partial_assistant": msg,
		"commit": map[string]any{
			"assistant_lt": assistantLT,
			"cache": map[string]any{
				"namespace": namespace, "payload": payload, "fingerprint": fingerprint,
			},
		},
		"timestamp_ms": time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	return out
}

func TestRestartCompletesAssistantIRAndNativeCacheIdempotently(t *testing.T) {
	payloads := []struct {
		name        string
		namespace   string
		fingerprint string
		payload     []json.RawMessage
	}{
		{
			name: "direct-anthropic-input-ready", namespace: "anthropic", fingerprint: "anthropic/tag/v1",
			payload: []json.RawMessage{json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"sealed"}]}`)},
		},
		{
			name: "sdk-signed-thinking", namespace: "anthropic", fingerprint: "anthropic-sdk/tag/v1",
			payload: []json.RawMessage{json.RawMessage(`{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"sig-abc"}]}`)},
		},
		{
			name: "copilot-encrypted-output", namespace: "copilot-responses", fingerprint: "copilot-responses/v2/gpt-test",
			payload: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","encrypted_content":"enc-opaque","id":"rs_1"}`),
				json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"sealed"}]}`),
			},
		},
	}
	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			for _, state := range []string{"journal-only", "ir-only", "ir-and-cache"} {
				t.Run(state, func(t *testing.T) {
					b, id := newBackedConversation(t)
					defer b.Close()
					ir, err := b.Open(id)
					require.NoError(t, err)
					assistantLT := uint64(1)
					if tail, ok := ir.PeekTail(); ok {
						assistantLT = tail.LT + 1
					}
					msg := message.Message{
						Role: message.RoleAssistant, Content: []message.Content{message.TextContent("sealed")},
						StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
					}
					if state != "journal-only" {
						entry, err := ir.Append(store.Entry[message.Message]{Payload: msg})
						require.NoError(t, err)
						require.Equal(t, assistantLT, entry.LT)
					}
					if state == "ir-and-cache" {
						cache, err := b.OpenTranslation(id, tt.namespace)
						require.NoError(t, err)
						_, err = cache.Append(store.Entry[[]json.RawMessage]{
							FigaroLT: assistantLT, Payload: tt.payload, Fingerprint: tt.fingerprint,
						})
						require.NoError(t, err)
					}
					journal, err := b.OpenTurnJournal(id)
					require.NoError(t, err)
					require.NoError(t, journal.Checkpoint(
						assistantLT+1,
						commitCheckpointPayload(t, assistantLT, msg, tt.namespace, tt.fingerprint, tt.payload),
					))
					require.NoError(t, journal.Sync())

					prov := &recoveryProvider{}
					a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
					a.Kill()
					assert.Zero(t, prov.callCount())
					assert.Equal(t, assistantLT, uint64(ir.Len()))
					cache, err := b.OpenTranslation(id, tt.namespace)
					require.NoError(t, err)
					got, ok := cache.Lookup(assistantLT)
					require.True(t, ok)
					assert.Equal(t, tt.fingerprint, got.Fingerprint)
					require.Len(t, got.Payload, len(tt.payload))
					for i := range tt.payload {
						assert.Equal(t, string(tt.payload[i]), string(got.Payload[i]))
					}

					repeated := &recoveryProvider{}
					a = figaro.NewAgent(figaro.Config{ID: id, Provider: repeated, Backend: b, Tools: tool.NewRegistry()})
					a.Kill()
					assert.Zero(t, repeated.callCount())
					assert.Equal(t, assistantLT, uint64(ir.Len()))
				})
			}
		})
	}
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

func TestCacheAppendFailureLeavesRecoverableCommit(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	payload := []json.RawMessage{json.RawMessage(`{"encrypted_content":"enc-opaque","type":"reasoning"}`)}
	prov := &nativeCommitProvider{
		namespace: "atomic-cache", payload: payload, fingerprint: "atomic-cache/v1",
	}
	failing := failingAssistantCacheBackend{Backend: real, namespace: "atomic-cache"}
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: prov,
		Backend: failing,
		Tools:   tool.NewRegistry(),
	})
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

	blocked := &recoveryProvider{}
	a = figaro.NewAgent(figaro.Config{ID: id, Provider: blocked, Backend: failing, Tools: tool.NewRegistry()})
	blockedCh, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "must stay blocked"})
	blockedReason := waitDoneReason(t, blockedCh)
	a.Kill()
	assert.Contains(t, blockedReason, "recover turn journal")
	assert.Zero(t, blocked.callCount())
	assert.Equal(t, tail.LT, uint64(ir.Len()), "failed recovery must not advance canonical IR")

	recovery := &recoveryProvider{}
	a = figaro.NewAgent(figaro.Config{ID: id, Provider: recovery, Backend: real, Tools: tool.NewRegistry()})
	a.Kill()
	assert.Zero(t, recovery.callCount())
	cached, ok := cache.Lookup(tail.LT)
	require.True(t, ok)
	assert.Equal(t, string(payload[0]), string(cached.Payload[0]))
}
