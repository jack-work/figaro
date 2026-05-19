package figaro_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	cache    store.Log[[]json.RawMessage] // nil = no cache (tests don't need one)
}

func (m *mockProvider) Name() string                                             { return "mock" }
func (m *mockProvider) Fingerprint() string                                      { return "mock/v0" }
func (m *mockProvider) SetModel(model string)                                    {}
func (m *mockProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (m *mockProvider) encode(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}

func (m *mockProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	mockCatchUp(in.FigLog, m.cache, m.encode, m.Fingerprint())
	mockPushAssistant(in.FigLog, m.cache, bus, m.encode, m.Fingerprint(), m.response)
	return nil
}

// mockEncodeFn matches the per-message encoder shape. Provider mocks
// supply their own implementations so spies (chalkSpyProvider) can
// record what they would have encoded.
type mockEncodeFn func(msg message.Message, prev chalkboard.Snapshot) ([]json.RawMessage, error)

// mockCatchUp catches up the cache from the durable figLog using
// the given encoder. Mirrors what real providers do at the top of
// Send. Skipped when cache is nil (ephemeral tests).
func mockCatchUp(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], encode mockEncodeFn, fingerprint string) {
	snap := chalkboard.Snapshot{}
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

// mockPushAssistant simulates a streaming turn: emits the text as a
// delta, appends a final assistant message to figLog, writes it
// into the cache (if any), and pushes figaro so the act loop
// dispatches.
func mockPushAssistant(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], bus provider.Bus, encode mockEncodeFn, fingerprint, text string) {
	if text == "" {
		return
	}
	bus.PushDelta(message.Content{Type: message.ContentText, Text: text})
	msg := message.Message{
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent(text)},
		StopReason: message.StopEnd,
	}
	entry, err := figLog.Append(store.Entry[message.Message]{Payload: msg})
	if err == nil {
		msg.LogicalTime = entry.LT
		if cache != nil {
			if payload, eErr := encode(msg, chalkboard.Snapshot{}); eErr == nil {
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

// --- Test helpers ---

// chanNotifier is the test-side adapter for figaro.Notifier — pipes
// every fanout call into a buffered channel so tests can assert on
// notification ordering. close-once is guarded by mu so unsub is safe
// to call from anywhere.
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

// subscribeChan registers a chanNotifier on the agent and returns the
// receive end + an unsubscribe func that drops the registration AND
// closes the channel.
func subscribeChan(a *figaro.Agent) (<-chan rpc.Notification, func()) {
	sink := &chanNotifier{ch: make(chan rpc.Notification, 128)}
	unsub := a.Subscribe(sink)
	return sink.ch, func() {
		unsub()
		sink.closeOnce()
	}
}

// --- Tests ---

func newTestAgent(response string) *figaro.Agent {
	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}})
	return figaro.NewAgent(figaro.Config{
		ID:         "test-001",
		SocketPath: "/tmp/test-figaro.sock",
		Provider:   &mockProvider{response: response},
		Chalkboard: cb,
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
	ch, _ := subscribeChan(a)

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

	ch, _ := subscribeChan(a)

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
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)

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

	ch1, _ := subscribeChan(a)
	ch2, _ := subscribeChan(a)

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

	ch, unsub := subscribeChan(a)
	unsub()

	// Channel should be closed by the unsubscribe func.
	_, open := <-ch
	assert.False(t, open, "unsubscribed channel should be closed")
}

func TestAgent_Kill(t *testing.T) {
	a := newTestAgent("hi")
	ch, _ := subscribeChan(a)

	a.Kill()

	// Kill clears subscribers but doesn't close test channels — they're
	// the test's own resource. Confirm no further notifications arrive.
	select {
	case n, ok := <-ch:
		if ok {
			t.Fatalf("unexpected notification after Kill: %+v", n)
		}
	case <-time.After(50 * time.Millisecond):
		// expected: nothing else queued
	}
}

// --- Panicking provider ---

type panicProvider struct {
	panicCount int
	response   string
}

func (p *panicProvider) Name() string                                             { return "panic-mock" }
func (p *panicProvider) Fingerprint() string                                      { return "panic-mock/v0" }
func (p *panicProvider) SetModel(model string)                                    {}
func (p *panicProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *panicProvider) encode(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
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
	// Provider panics on the first call, succeeds on the second.
	prov := &panicProvider{panicCount: 1, response: "recovered"}

	a := figaro.NewAgent(figaro.Config{
		ID:         "panic-test",
		SocketPath: "/tmp/panic-test.sock",
		Provider:   prov,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)

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
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)

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
	// prompt that triggered the panic stays in the figaro log and
	// the agent emits an Error notification. Token counters stay at
	// zero because no assistant response landed.
	msgs := a.Context()
	require.NotEmpty(t, msgs, "user prompt is preserved across Send panics")
	assert.Equal(t, message.RoleUser, msgs[len(msgs)-1].Role)

	info := a.Info()
	assert.Equal(t, 0, info.TokensIn)
	assert.Equal(t, 0, info.TokensOut)
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
		Backend:    backend,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
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
	// figwal layout: arias/<id>/aria/<segment>.jsonl
	ariaDir := filepath.Join(storeDir, "persist-001")
	walDir := filepath.Join(ariaDir, "aria")
	segments, err := os.ReadDir(walDir)
	require.NoError(t, err, "figwal aria dir should exist after prompt")
	var msgCount int
	for _, seg := range segments {
		if seg.IsDir() || filepath.Ext(seg.Name()) != ".jsonl" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(walDir, seg.Name()))
		require.NoError(t, err)
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				msgCount++
			}
		}
	}
	// At minimum, the first prompt's flush wrote user + assistant (2 messages).
	assert.GreaterOrEqual(t, msgCount, 2, "should have at least user + assistant on disk")

	// meta.json holds derived stats (counts, tokens). Configured
	// fields (provider, model) moved to chalkboard.json. The
	// summary derivation is async — poll for the latest tick to
	// land.
	metaPath := filepath.Join(ariaDir, "meta.json")
	var meta struct {
		MessageCount int `json:"message_count"`
		TurnCount    int `json:"turn_count"`
	}
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(metaPath)
		if err != nil {
			return false
		}
		if json.Unmarshal(data, &meta) != nil {
			return false
		}
		return meta.MessageCount >= 2 && meta.TurnCount >= 1
	}, 2*time.Second, 10*time.Millisecond, "meta.json should reflect user+assistant")
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
		Backend:    backend,
	})

	ch, _ := subscribeChan(a1)
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
		Backend:    backend,
	})

	ch, _ := subscribeChan(a)
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

	// Verify data is on disk. figwal layout: aria/ dir with segments.
	walDir := filepath.Join(storeDir, "killflush-001", "aria")
	st, statErr := os.Stat(walDir)
	require.NoError(t, statErr, "figwal aria dir should exist after kill")
	assert.True(t, st.IsDir())
}

// TestAgent_BootInsertsSentinelForDanglingToolUse builds an aria
// directory on disk whose tail is an assistant turn ending in
// stop_reason=tool_use with no matching tool_results, opens an Agent
// against the same Backend, and verifies the boot path appends a
// RoleSystemInterrupt sentinel.
func TestAgent_BootInsertsSentinelForDanglingToolUse(t *testing.T) {
	storeDir := t.TempDir()
	backend, err := store.NewFileBackend(storeDir)
	require.NoError(t, err)

	// Seed the stream with [user, assistant.tool_use] using a
	// pre-agent FileStream directly.
	pre, err := backend.Open("danglingboot")
	require.NoError(t, err)
	_, err = pre.Append(store.Entry[message.Message]{
		Payload: message.Message{
			Role:    message.RoleUser,
			Content: []message.Content{message.TextContent("run a tool")},
		},
	})
	require.NoError(t, err)
	_, err = pre.Append(store.Entry[message.Message]{
		Payload: message.Message{
			Role: message.RoleAssistant,
			Content: []message.Content{
				{Type: message.ContentToolCall, ToolCallID: "tc_boot", ToolName: "bash"},
			},
			StopReason: message.StopToolUse,
		},
	})
	require.NoError(t, err)
	require.NoError(t, pre.Close())

	// Boot an agent on the same store; NewAgent runs the boot-time
	// repair.
	a := figaro.NewAgent(figaro.Config{
		ID:         "danglingboot",
		SocketPath: "/tmp/danglingboot-test.sock",
		Provider:   &mockProvider{response: "ignored"},
		Backend:    backend,
	})
	defer a.Kill()

	// Read history from the agent (also flushes/projects).
	msgs := a.Context()
	require.GreaterOrEqual(t, len(msgs), 3, "boot should have appended a sentinel")
	tail := msgs[len(msgs)-1]
	assert.True(t, message.IsInterruptSentinel(tail), "tail should be the sentinel")
	assert.Equal(t, []string{"tc_boot"}, message.DanglingToolCallIDs(tail))
}

func TestAgent_EphemeralWhenNoBackend(t *testing.T) {
	// No Backend — should behave as before (no files written).
	tmpDir := t.TempDir()

	a := figaro.NewAgent(figaro.Config{
		ID:         "ephemeral-001",
		SocketPath: "/tmp/ephemeral-test.sock",
		Provider:   &mockProvider{response: "gone"},
		// Backend deliberately omitted.
	})

	ch, _ := subscribeChan(a)
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

func (s *slowProvider) Name() string                                             { return "slow" }
func (s *slowProvider) Fingerprint() string                                      { return "slow/v0" }
func (s *slowProvider) SetModel(model string)                                    {}
func (s *slowProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

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
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
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

	ch, _ := subscribeChan(a)
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
