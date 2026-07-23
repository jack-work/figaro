package angelus_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

type hangingProvider struct {
	started chan struct{}
}

func (p *hangingProvider) Name() string                                         { return "hanging" }
func (p *hangingProvider) Fingerprint() string                                  { return "hanging/v1" }
func (p *hangingProvider) SetModel(string)                                      {}
func (p *hangingProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *hangingProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.TextContent("partial prose"))
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestShutdownDrainSealsPartialTurn(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir())
	require.NoError(t, err)
	defer backend.Close()
	loadout, err := backend.CreateLoadout("test", message.Patch{})
	require.NoError(t, err)
	conv, err := backend.CreateConversation(loadout)
	require.NoError(t, err)

	prov := &hangingProvider{started: make(chan struct{})}
	agent := figaro.NewAgent(figaro.Config{
		ID: conv, Provider: prov, Backend: backend, Tools: tool.NewRegistry(),
	})

	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, t.TempDir())})
	require.NoError(t, a.Registry.Register(agent))

	agent.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	a.Shutdown(2 * time.Second)
	assert.Less(t, time.Since(start), 5*time.Second)

	ir, err := backend.Open(conv)
	require.NoError(t, err)
	tail, ok := ir.PeekTail()
	require.True(t, ok)
	assert.Equal(t, message.RoleAssistant, tail.Payload.Role)
	assert.Equal(t, message.StopAborted, tail.Payload.StopReason)
	require.NotEmpty(t, tail.Payload.Content)
	assert.Equal(t, "partial prose", tail.Payload.Content[0].Text)
}

func TestConcurrentRestoreRepairsOnce(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, err := store.NewXwalBackend(dir + "/arias")
	require.NoError(t, err)
	defer backend.Close()
	loadout, err := backend.CreateLoadout("mock", message.Patch{})
	require.NoError(t, err)
	conv, err := backend.CreateConversation(loadout)
	require.NoError(t, err)
	ir, err := backend.Open(conv)
	require.NoError(t, err)
	_, err = ir.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("run it")},
	}})
	require.NoError(t, err)
	_, err = ir.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:       message.RoleAssistant,
		StopReason: message.StopToolInvoke,
		Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "tc_race", ToolName: "bash",
			Arguments: map[string]any{},
		}},
	}})
	require.NoError(t, err)
	before := ir.Len()

	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})
	require.NoError(t, os.MkdirAll(a.FigaroSocketDir(), 0700))
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handlers := angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a,
		Config:  loaded,
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) {
			return &mockProviderForIntegration{}, nil
		},
		Ctx:                 ctx,
		ChalkboardTemplates: template.New("t"),
	})

	const n = 8
	agents := make([]figaro.Figaro, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			agents[i], errs[i] = handlers.Restore(ctx, conv)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.NotNil(t, agents[i])
		assert.Same(t, agents[0], agents[i])
	}
	defer a.Registry.Kill(conv)

	assert.Equal(t, before+1, ir.Len(), "exactly one repair tic appended")
	tail, ok := ir.PeekTail()
	require.True(t, ok)
	require.Equal(t, message.RoleUser, tail.Payload.Role)
	require.Len(t, tail.Payload.Content, 1)
	assert.Equal(t, message.ContentToolResult, tail.Payload.Content[0].Type)
	assert.Equal(t, "tc_race", tail.Payload.Content[0].ToolCallID)
	assert.True(t, tail.Payload.Content[0].IsError)
	assert.Contains(t, tail.Payload.Content[0].Text, "process died mid-turn")
}
