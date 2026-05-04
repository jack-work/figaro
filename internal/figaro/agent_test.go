package figaro_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// --- Mock provider ---

type mockProvider struct {
	response string
}

func (m *mockProvider) Name() string                                          { return "mock" }
func (m *mockProvider) Fingerprint() string                                   { return "mock/v0" }
func (m *mockProvider) SetModel(model string)                                 {}
func (m *mockProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (m *mockProvider) Decode(payload []json.RawMessage) ([]message.Message, error) {
	return mockDecode(payload)
}
func (m *mockProvider) Encode(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}
func (m *mockProvider) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	return mockAssemble(deltas)
}
func (m *mockProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	mockPushAssistant(bus, m.response)
	return nil
}

// mockNativeAssistant is the test envelope: one text block + stop_reason.
type mockNativeAssistant struct {
	Role       string              `json:"role"`
	Content    []mockNativeContent `json:"content"`
	StopReason string              `json:"stop_reason,omitempty"`
}

type mockNativeContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type mockDelta struct {
	Delta       string              `json:"delta"`
	ContentType message.ContentType `json:"content_type,omitempty"`
}

// mockPushAssistant pushes one delta event carrying the full text. The
// agent's synchronize will call mockAssemble to fold it into a final
// nativeMessage.
func mockPushAssistant(bus provider.Bus, text string) {
	if text == "" {
		return
	}
	delta, _ := json.Marshal(mockDelta{Delta: text, ContentType: message.ContentText})
	bus.Push(provider.Event{Payload: []json.RawMessage{delta}})
}

func mockAssemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	var text string
	for _, payload := range deltas {
		if len(payload) == 0 {
			continue
		}
		var d mockDelta
		if json.Unmarshal(payload[0], &d) == nil {
			text += d.Delta
		}
	}
	if text == "" {
		return nil, nil
	}
	nm := mockNativeAssistant{
		Role:       "assistant",
		Content:    []mockNativeContent{{Type: "text", Text: text}},
		StopReason: "end_turn",
	}
	raw, _ := json.Marshal(nm)
	return []json.RawMessage{raw}, nil
}

// mockDecode handles both shapes of payload uniformly. Live tail
// entries are mockDelta; durable entries are mockNativeAssistant.
func mockDecode(payload []json.RawMessage) ([]message.Message, error) {
	out := make([]message.Message, 0, len(payload))
	for _, r := range payload {
		if len(r) == 0 {
			continue
		}
		var d mockDelta
		if json.Unmarshal(r, &d) == nil && d.Delta != "" {
			ct := d.ContentType
			if ct == "" {
				ct = message.ContentText
			}
			out = append(out, message.Message{
				Role:    message.RoleAssistant,
				Content: []message.Content{{Type: ct, Text: d.Delta}},
			})
			continue
		}
		var nm mockNativeAssistant
		if err := json.Unmarshal(r, &nm); err != nil {
			return nil, err
		}
		if nm.Role == "" {
			continue
		}
		msg := message.Message{Role: message.Role(nm.Role)}
		for _, c := range nm.Content {
			if c.Type == "text" {
				msg.Content = append(msg.Content, message.TextContent(c.Text))
			}
		}
		switch nm.StopReason {
		case "end_turn", "stop":
			msg.StopReason = message.StopEnd
		case "tool_use":
			msg.StopReason = message.StopToolUse
		}
		out = append(out, msg)
	}
	return out, nil
}

// --- Tests ---

func newTestAgent(response string) *figaro.Agent {
	return figaro.NewAgent(figaro.Config{
		ID:         "test-001",
		SocketPath: "/tmp/test-figaro.sock",
		Provider:   &mockProvider{response: response},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
	})
}

func TestAgent_ID(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()
	assert.Equal(t, "test-001", a.ID())
}

func TestAgent_SocketPath(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()
	assert.Equal(t, "/tmp/test-figaro.sock", a.SocketPath())
}

func TestAgent_PromptAndSubscribe(t *testing.T) {
	a := newTestAgent("4")
	defer a.Kill()

	// Subscribe before prompting.
	ch := a.Subscribe()

	a.Prompt("What is 2+2?")

	// Collect notifications until done.
	var notifications []rpc.Notification
	timeout := time.After(5 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			notifications = append(notifications, n)
			if n.Method == rpc.MethodDone {
				break loop
			}
		case <-timeout:
			t.Fatal("timeout waiting for notifications")
		}
	}

	// Should have received at least a delta and a done.
	methods := make([]string, len(notifications))
	for i, n := range notifications {
		methods[i] = n.Method
	}
	assert.Contains(t, methods, rpc.MethodDelta)
	assert.Contains(t, methods, rpc.MethodDone)
}

func TestAgent_Context(t *testing.T) {
	a := newTestAgent("hello")
	defer a.Kill()

	ch := a.Subscribe()

	// Initially empty.
	assert.Empty(t, a.Context())

	a.Prompt("say hi")

	// Wait for done.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:

	// Should have user + assistant messages.
	msgs := a.Context()
	require.GreaterOrEqual(t, len(msgs), 2)
	assert.Equal(t, message.RoleUser, msgs[0].Role)
	assert.Equal(t, message.RoleAssistant, msgs[1].Role)
}

func TestAgent_FIFOOrdering(t *testing.T) {
	// Provider echoes the prompt text back.
	a := newTestAgent("")
	a.Kill() // kill the default one

	// Use a provider that echoes the prompt (via the messages).
	a = figaro.NewAgent(figaro.Config{
		ID:         "fifo-test",
		SocketPath: "/tmp/test-fifo.sock",
		Provider:   &mockProvider{response: "ok"},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
	})
	defer a.Kill()

	ch := a.Subscribe()

	// Enqueue two prompts rapidly.
	a.Prompt("first")
	a.Prompt("second")

	// Collect two done notifications.
	doneCount := 0
	timeout := time.After(5 * time.Second)
	for doneCount < 2 {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				doneCount++
			}
		case <-timeout:
			t.Fatalf("timeout: only got %d done notifications", doneCount)
		}
	}

	// Both prompts should be in context, in order.
	msgs := a.Context()
	require.GreaterOrEqual(t, len(msgs), 4) // user, assistant, user, assistant
	assert.Equal(t, message.RoleUser, msgs[0].Role)
	assert.Equal(t, message.RoleUser, msgs[2].Role)
}

func TestAgent_MultipleSubscribers(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()

	ch1 := a.Subscribe()
	ch2 := a.Subscribe()

	a.Prompt("hello")

	// Both should receive done.
	timeout := time.After(5 * time.Second)
	got1, got2 := false, false
	for !got1 || !got2 {
		select {
		case n := <-ch1:
			if n.Method == rpc.MethodDone {
				got1 = true
			}
		case n := <-ch2:
			if n.Method == rpc.MethodDone {
				got2 = true
			}
		case <-timeout:
			t.Fatalf("timeout: got1=%v got2=%v", got1, got2)
		}
	}
}

func TestAgent_Unsubscribe(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()

	ch := a.Subscribe()
	a.Unsubscribe(ch)

	// Channel should be closed.
	_, open := <-ch
	assert.False(t, open, "unsubscribed channel should be closed")
}

func TestAgent_Kill(t *testing.T) {
	a := newTestAgent("hi")
	ch := a.Subscribe()

	a.Kill()

	// Subscriber channel should be closed.
	_, open := <-ch
	assert.False(t, open, "subscriber channel should be closed after kill")
}

// --- Panicking provider ---

type panicProvider struct {
	panicCount int
	response   string
}

func (p *panicProvider) Name() string                                          { return "panic-mock" }
func (p *panicProvider) Fingerprint() string                                   { return "panic-mock/v0" }
func (p *panicProvider) SetModel(model string)                                 {}
func (p *panicProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *panicProvider) Decode(payload []json.RawMessage) ([]message.Message, error) {
	return mockDecode(payload)
}
func (p *panicProvider) Encode(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}
func (p *panicProvider) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	return mockAssemble(deltas)
}
func (p *panicProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	if p.panicCount > 0 {
		p.panicCount--
		panic("simulated crash")
	}
	mockPushAssistant(bus, p.response)
	return nil
}

func TestAgent_PanicRecovery(t *testing.T) {
	// Provider panics on the first call, succeeds on the second.
	prov := &panicProvider{panicCount: 1, response: "recovered"}

	a := figaro.NewAgent(figaro.Config{
		ID:         "panic-test",
		SocketPath: "/tmp/panic-test.sock",
		Provider:   prov,
		Model:      "mock-model",
		MaxTokens:  1024,
	})
	defer a.Kill()

	ch := a.Subscribe()

	// First prompt — will panic inside the agent. The new contract is
	// that every turn (success or failure) must terminate with Done so
	// the CLI never hangs. We expect Error followed eventually by Done.
	a.Prompt("trigger panic")

	timeout := time.After(5 * time.Second)
	gotError, gotDone := false, false
	for !(gotError && gotDone) {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodError {
				gotError = true
			}
			if n.Method == rpc.MethodDone {
				gotDone = true
			}
		case <-timeout:
			t.Fatalf("timeout: gotError=%v gotDone=%v", gotError, gotDone)
		}
	}

	// Second prompt — should work because the agent restarted.
	a.Prompt("after recovery")

	gotDone = false
	timeout = time.After(5 * time.Second)
	for !gotDone {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				gotDone = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for done after recovery")
		}
	}

	// Send-path panics are recovered in the spawned goroutine and
	// surface as an Error notification — the conversation history
	// is preserved (no full agent crash + reset). The second prompt's
	// assistant turn should be the most recent entry.
	msgs := a.Context()
	require.NotEmpty(t, msgs)
	assert.Equal(t, message.RoleAssistant, msgs[len(msgs)-1].Role)
}

func TestAgent_PanicRecovery_ContextReset(t *testing.T) {
	// Provider panics on first call.
	prov := &panicProvider{panicCount: 1, response: "ok"}

	a := figaro.NewAgent(figaro.Config{
		ID:         "panic-ctx-test",
		SocketPath: "/tmp/panic-ctx-test.sock",
		Provider:   prov,
		Model:      "mock-model",
		MaxTokens:  1024,
	})
	defer a.Kill()

	ch := a.Subscribe()

	// Trigger panic.
	a.Prompt("boom")

	// Wait for error.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodError {
				goto errorReceived
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
errorReceived:

	// Send-path panics no longer wipe the conversation — the user
	// prompt that triggered the panic stays in the figaro stream and
	// the agent emits an Error notification. Token counters stay at
	// zero because no assistant response landed.
	msgs := a.Context()
	require.NotEmpty(t, msgs, "user prompt is preserved across Send panics")
	assert.Equal(t, message.RoleUser, msgs[len(msgs)-1].Role)

	info := a.Info()
	assert.Equal(t, 0, info.TokensIn)
	assert.Equal(t, 0, info.TokensOut)
}

func TestAgent_SetModel(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()

	assert.Equal(t, "mock-model-v1", a.Info().Model)
	a.SetModel("mock-model-v2")
	assert.Equal(t, "mock-model-v2", a.Info().Model)
}

func TestAgent_Info(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()

	info := a.Info()
	assert.Equal(t, "test-001", info.ID)
	assert.Equal(t, "mock", info.Provider)
	assert.Equal(t, "mock-model-v1", info.Model)
	assert.False(t, info.CreatedAt.IsZero())
}

// --- Persistence tests ---

func TestAgent_PersistenceFlushesOnPrompt(t *testing.T) {
	storeDir := t.TempDir()
	backend, err := store.NewFileBackend(storeDir)
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:         "persist-001",
		SocketPath: "/tmp/persist-test.sock",
		Provider:   &mockProvider{response: "persisted reply"},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
		Backend:    backend,
	})
	defer a.Kill()

	ch := a.Subscribe()
	a.Prompt("save me")

	// Wait for done.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				goto firstDone
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
firstDone:

	// Send a second prompt — by the time it starts processing,
	// the first prompt's flush is guaranteed complete (FIFO drain loop).
	a.Prompt("second")
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				goto secondDone
			}
		case <-timeout:
			t.Fatal("timeout on second prompt")
		}
	}
secondDone:

	// The aria directory should exist on disk (flushed after first prompt).
	ariaDir := filepath.Join(storeDir, "persist-001")
	ariaPath := filepath.Join(ariaDir, "aria.jsonl")
	data, err := os.ReadFile(ariaPath)
	require.NoError(t, err, "aria.jsonl should exist after prompt")
	require.NotEmpty(t, data)

	// aria.jsonl is NDJSON; count non-empty lines.
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var msgCount int
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			msgCount++
		}
	}
	// At minimum, the first prompt's flush wrote user + assistant (2 messages).
	assert.GreaterOrEqual(t, msgCount, 2, "should have at least user + assistant on disk")

	// meta.json holds derived stats (counts, tokens). Configured
	// fields (provider, model) moved to chalkboard.json.
	metaPath := filepath.Join(ariaDir, "meta.json")
	mdata, err := os.ReadFile(metaPath)
	require.NoError(t, err, "meta.json should exist after prompt")
	var meta struct {
		MessageCount int `json:"message_count"`
		TurnCount    int `json:"turn_count"`
	}
	require.NoError(t, json.Unmarshal(mdata, &meta))
	assert.GreaterOrEqual(t, meta.MessageCount, 2)
	assert.GreaterOrEqual(t, meta.TurnCount, 1)
}

func TestAgent_PersistenceRestoresOnCreate(t *testing.T) {
	storeDir := t.TempDir()
	backend, err := store.NewFileBackend(storeDir)
	require.NoError(t, err)

	// First agent: prompt and kill (which flushes to disk).
	a1 := figaro.NewAgent(figaro.Config{
		ID:         "restore-001",
		SocketPath: "/tmp/restore-test.sock",
		Provider:   &mockProvider{response: "first reply"},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
		Backend:    backend,
	})

	ch := a1.Subscribe()
	a1.Prompt("hello")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				goto firstDone
			}
		case <-timeout:
			t.Fatal("timeout on first prompt")
		}
	}
firstDone:
	a1.Kill()

	// Second agent with the same ID and Backend — should seed from disk.
	a2 := figaro.NewAgent(figaro.Config{
		ID:         "restore-001",
		SocketPath: "/tmp/restore-test2.sock",
		Provider:   &mockProvider{response: "second reply"},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
		Backend:    backend,
	})
	defer a2.Kill()

	// Context should already have the first conversation.
	msgs := a2.Context()
	require.GreaterOrEqual(t, len(msgs), 2, "should restore messages from disk")
	assert.Equal(t, message.RoleUser, msgs[0].Role)
	assert.Equal(t, message.RoleAssistant, msgs[1].Role)
}

func TestAgent_PersistenceKillFlushes(t *testing.T) {
	storeDir := t.TempDir()
	backend, err := store.NewFileBackend(storeDir)
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:         "killflush-001",
		SocketPath: "/tmp/killflush-test.sock",
		Provider:   &mockProvider{response: "will be saved"},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
		Backend:    backend,
	})

	ch := a.Subscribe()
	a.Prompt("save on kill")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:

	// Kill the agent (should flush + close).
	a.Kill()

	// Verify data is on disk.
	ariaPath := filepath.Join(storeDir, "killflush-001", "aria.jsonl")
	_, statErr := os.Stat(ariaPath)
	assert.NoError(t, statErr, "aria.jsonl should exist after kill")
}

func TestAgent_EphemeralWhenNoBackend(t *testing.T) {
	// No Backend — should behave as before (no files written).
	tmpDir := t.TempDir()

	a := figaro.NewAgent(figaro.Config{
		ID:         "ephemeral-001",
		SocketPath: "/tmp/ephemeral-test.sock",
		Provider:   &mockProvider{response: "gone"},
		Model:      "mock-model-v1",
		MaxTokens:  1024,
		// Backend deliberately omitted.
	})

	ch := a.Subscribe()
	a.Prompt("vanish")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				goto done
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
done:
	a.Kill()

	// No files should appear in any random dir.
	entries, _ := os.ReadDir(tmpDir)
	assert.Empty(t, entries, "no files should be written without a Backend")
}

// --- Slow provider for interrupt testing ---

type slowProvider struct {
	started chan struct{} // closed once Send has been entered
}

func (s *slowProvider) Name() string                                          { return "slow" }
func (s *slowProvider) Fingerprint() string                                   { return "slow/v0" }
func (s *slowProvider) SetModel(model string)                                 {}
func (s *slowProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (s *slowProvider) Decode(payload []json.RawMessage) ([]message.Message, error) {
	return mockDecode(payload)
}
func (s *slowProvider) Encode(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}
func (s *slowProvider) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	return mockAssemble(deltas)
}

// Send blocks until ctx is cancelled, then returns the cancellation
// error — mirroring what a real HTTP SSE stream does when its request
// context is cancelled mid-flight.
func (s *slowProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	if s.started != nil {
		close(s.started)
		s.started = nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestAgent_Interrupt verifies that Interrupt cancels an in-flight
// turn, emits stream.error + stream.done, and leaves the agent idle
// and usable for a second prompt.
func TestAgent_Interrupt(t *testing.T) {
	started := make(chan struct{})
	a := figaro.NewAgent(figaro.Config{
		ID:         "interrupt-001",
		SocketPath: "/tmp/interrupt-test.sock",
		Provider:   &slowProvider{started: started},
		Model:      "slow-model",
		MaxTokens:  1024,
	})
	defer a.Kill()

	ch := a.Subscribe()
	a.Prompt("take forever please")

	// Wait for the provider to actually be streaming before interrupting,
	// so we're testing the mid-turn path and not a pre-start race.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never started")
	}

	a.Interrupt()

	// Collect notifications until Done. We should see one Error
	// carrying the interrupt message, followed by Done.
	var sawError bool
	var doneReason string
	timeout := time.After(3 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodError:
				if p, ok := n.Params.(rpc.ErrorParams); ok && p.Message == "interrupted" {
					sawError = true
				}
			case rpc.MethodDone:
				if p, ok := n.Params.(rpc.DoneParams); ok {
					doneReason = p.Reason
				}
				break loop
			}
		case <-timeout:
			t.Fatal("timeout waiting for interrupt to flow through")
		}
	}

	assert.True(t, sawError, "expected stream.error(\"interrupted\")")
	assert.Equal(t, "interrupted", doneReason)

	// Agent should be idle and reusable after the interrupt.
	// (Avoid reissuing a prompt with the slow provider; just assert the
	// loop didn't die.)
	info := a.Info()
	assert.Equal(t, "idle", info.State, "agent should be idle after interrupt")
}

// TestAgent_InterruptWhenIdle is a no-op: Interrupt on an idle agent
// must not emit spurious Done/Error notifications.
func TestAgent_InterruptWhenIdle(t *testing.T) {
	a := newTestAgent("hi")
	defer a.Kill()

	ch := a.Subscribe()
	a.Interrupt()

	select {
	case n := <-ch:
		t.Fatalf("unexpected notification from idle interrupt: %+v", n)
	case <-time.After(150 * time.Millisecond):
		// Silence is correct.
	}
}

// Ensure unused import elision doesn't remove json (kept for future tests).
var _ = json.RawMessage(nil)
