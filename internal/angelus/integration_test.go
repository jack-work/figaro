package angelus_test

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/transport"
)

// mockProviderForIntegration echoes a fixed response.
type mockProviderForIntegration struct{}

func (m *mockProviderForIntegration) Name() string                                          { return "mock" }
func (m *mockProviderForIntegration) Fingerprint() string                                   { return "mock/v0" }
func (m *mockProviderForIntegration) SetModel(model string)                                 {}
func (m *mockProviderForIntegration) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

type mockIntegNative struct {
	Role       string                   `json:"role"`
	Content    []map[string]interface{} `json:"content"`
	StopReason string                   `json:"stop_reason,omitempty"`
}

func (m *mockProviderForIntegration) Decode(raw []json.RawMessage) ([]message.Message, error) {
	out := make([]message.Message, 0, len(raw))
	for _, r := range raw {
		var nm mockIntegNative
		if err := json.Unmarshal(r, &nm); err != nil {
			return nil, err
		}
		msg := message.Message{Role: message.Role(nm.Role)}
		for _, c := range nm.Content {
			if t, _ := c["type"].(string); t == "text" {
				if txt, _ := c["text"].(string); txt != "" {
					msg.Content = append(msg.Content, message.TextContent(txt))
				}
			}
		}
		if nm.StopReason == "end_turn" {
			msg.StopReason = message.StopEnd
		}
		out = append(out, msg)
	}
	return out, nil
}

func (m *mockProviderForIntegration) Encode(_ context.Context, _ []message.Message, _ chalkboard.Snapshot, _ causal.Slice[message.ProviderTranslation], _ []provider.Tool, _ int) ([]byte, provider.ProjectionSummary, error) {
	return nil, provider.ProjectionSummary{Fingerprint: m.Fingerprint()}, nil
}
func (m *mockProviderForIntegration) Send(_ context.Context, _ []byte, _ provider.Bus) ([]json.RawMessage, error) {
	nm := mockIntegNative{
		Role:       "assistant",
		Content:    []map[string]interface{}{{"type": "text", "text": "42"}},
		StopReason: "end_turn",
	}
	raw, _ := json.Marshal(nm)
	return []json.RawMessage{raw}, nil
}

func TestIntegration_CreateAndPrompt(t *testing.T) {
	dir := t.TempDir()
	logger := log.New(os.Stderr, "integration: ", log.LstdFlags)

	// Create and start angelus.
	a := angelus.New(angelus.Config{RuntimeDir: dir, Logger: logger})

	// Wire the provider factory.
	factory := func(providerName, model string) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	}
	loaded, err := config.Load(dir) // empty config dir, uses defaults
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		ProviderFactory: factory,
		Ctx:             ctx,
	}).Map

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

	// Wait for socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(a.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// --- Angelus client: create a figaro ---
	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	createResp, err := acli.Create(ctx, "mock", "mock-model")
	require.NoError(t, err)
	assert.NotEmpty(t, createResp.FigaroID)
	assert.Equal(t, "unix", createResp.Endpoint.Scheme)

	// --- Angelus client: bind a pid ---
	err = acli.Bind(ctx, 99999, createResp.FigaroID)
	require.NoError(t, err)

	// --- Angelus client: resolve ---
	resolveResp, err := acli.Resolve(ctx, 99999)
	require.NoError(t, err)
	assert.True(t, resolveResp.Found)
	assert.Equal(t, createResp.FigaroID, resolveResp.FigaroID)

	// --- Angelus client: list ---
	listResp, err := acli.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listResp.Figaros, 1)
	assert.Equal(t, createResp.FigaroID, listResp.Figaros[0].ID)

	// --- Figaro client: connect and prompt ---
	// Wait for figaro socket to appear.
	figaroEP := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(figaroEP.Address); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Connect with notification handler to wait for stream.done.
	// Notifications are delivered in wire order — no envelopes, no reordering.
	doneCh := make(chan struct{}, 1)
	fcli, err := figaro.DialClient(figaroEP, func(method string, params json.RawMessage) {
		if method == "stream.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()

	err = fcli.Prompt(ctx, "what is the answer?")
	require.NoError(t, err)

	// Wait for done notification.
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream.done")
	}

	// Verify via info that the message was processed.
	info, err := fcli.Info(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, info.MessageCount, 2) // user + assistant

	// --- Angelus client: kill ---
	err = acli.Kill(ctx, createResp.FigaroID)
	require.NoError(t, err)

	listResp, err = acli.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listResp.Figaros)
}
