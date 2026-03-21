package angelus_test

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/transport"
)

// mockProviderForIntegration echoes a fixed response.
type mockProviderForIntegration struct{}

func (m *mockProviderForIntegration) Name() string         { return "mock" }
func (m *mockProviderForIntegration) SetModel(model string) {}
func (m *mockProviderForIntegration) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (m *mockProviderForIntegration) Send(ctx context.Context, block *message.Block, tools []provider.Tool, maxTokens int) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		msg := message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent("42")},
			StopReason: message.StopEnd,
			Provider:   "mock",
			Timestamp:  time.Now().UnixMilli(),
		}
		ch <- provider.StreamEvent{Delta: "42", ContentType: message.ContentText, Message: &msg}
		ch <- provider.StreamEvent{Done: true, Message: &msg}
	}()
	return ch, nil
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

	a.Handlers = angelus.NewHandlerMap(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		ProviderFactory: factory,
		Ctx:             ctx,
	})

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

	fcli, err := figaro.DialClient(figaroEP)
	require.NoError(t, err)
	defer fcli.Close()

	err = fcli.Prompt(ctx, "what is the answer?")
	require.NoError(t, err)

	// Give the agent a moment to process.
	time.Sleep(200 * time.Millisecond)

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
