package figaro_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
)

// --- Mock provider ---

type mockProvider struct {
	response string
}

func (m *mockProvider) Name() string         { return "mock" }
func (m *mockProvider) SetModel(model string) {}

func (m *mockProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) Send(ctx context.Context, block *message.Block, tools []provider.Tool, maxTokens int) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		msg := message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent(m.response)},
			StopReason: message.StopEnd,
			Provider:   "mock",
			Timestamp:  time.Now().UnixMilli(),
		}
		ch <- provider.StreamEvent{
			Delta:       m.response,
			ContentType: message.ContentText,
			Message:     &msg,
		}
		ch <- provider.StreamEvent{
			Done:    true,
			Message: &msg,
		}
	}()
	return ch, nil
}

// --- Tests ---

func newTestAgent(response string) *figaro.Agent {
	return figaro.NewAgent(figaro.Config{
		ID:           "test-001",
		SocketPath:   "/tmp/test-figaro.sock",
		Provider:     &mockProvider{response: response},
		Model:        "mock-model-v1",
		SystemPrompt: "You are a test agent.",
		MaxTokens:    1024,
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
		ID:           "fifo-test",
		SocketPath:   "/tmp/test-fifo.sock",
		Provider:     &mockProvider{response: "ok"},
		Model:        "mock-model-v1",
		SystemPrompt: "",
		MaxTokens:    1024,
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
