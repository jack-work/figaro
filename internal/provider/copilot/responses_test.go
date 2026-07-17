package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

type staticResponseTokenSource struct {
	token       string
	invalidated int
}

type failingResponseCache struct {
	*store.MemLog[[]json.RawMessage]
}

func (f *failingResponseCache) Append(store.Entry[[]json.RawMessage]) (store.Entry[[]json.RawMessage], error) {
	return store.Entry[[]json.RawMessage]{}, errors.New("cache unavailable")
}

func (s *staticResponseTokenSource) Resolve() (string, error) { return s.token, nil }

func (s *staticResponseTokenSource) Invalidate(string) error {
	s.invalidated++
	return nil
}

type responseTestBus struct {
	deltas      []message.Content
	toolStarts  []message.Content
	toolDeltas  []string
	toolReady   []message.Content
	messages    []message.Message
	messageEnds []string
}

type responseAgentProvider struct {
	*responsesProvider
}

func (*responseAgentProvider) Name() string { return providerName }

func (*responseAgentProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "gpt-5.6-terra", Provider: providerName}}, nil
}

type responseIntegrationTool struct {
	calls atomic.Int32
}

func (*responseIntegrationTool) Name() string        { return "echo" }
func (*responseIntegrationTool) Description() string { return "Returns its value." }
func (*responseIntegrationTool) Parameters() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []string{"value"},
	}
}

func (t *responseIntegrationTool) Execute(_ context.Context, args map[string]any, onOutput tool.OnOutput) ([]message.Content, error) {
	t.calls.Add(1)
	value, _ := args["value"].(string)
	onOutput([]byte("stream:" + value))
	return []message.Content{message.TextContent("tool:" + value)}, nil
}

type responseDoneNotifier struct {
	once sync.Once
	done chan struct{}
}

func (n *responseDoneNotifier) Notify(method string, _ any) error {
	if method == rpc.MethodTurnDone {
		n.once.Do(func() { close(n.done) })
	}
	return nil
}

func (b *responseTestBus) PushDelta(content message.Content) { b.deltas = append(b.deltas, content) }

func (b *responseTestBus) PushFigaro(msg message.Message) { b.messages = append(b.messages, msg) }

func (b *responseTestBus) PushToolInvokeStart(id, name string) {
	b.toolStarts = append(b.toolStarts, message.Content{
		Type:       message.ContentToolInvoke,
		ToolCallID: id,
		ToolName:   name,
	})
}

func (b *responseTestBus) PushToolInvokeDelta(_ string, partial string) {
	b.toolDeltas = append(b.toolDeltas, partial)
}

func (b *responseTestBus) PushToolReady(call message.Content) {
	b.toolReady = append(b.toolReady, call)
}

func (b *responseTestBus) PushMessageEnd(reason string) {
	b.messageEnds = append(b.messageEnds, reason)
}

func newResponsesTestProvider(
	server *httptest.Server,
	cache store.Log[[]json.RawMessage],
) *responsesProvider {
	tokenSource := &staticResponseTokenSource{token: "test-token"}
	p := newResponsesProvider(provider.Knobs{
		Model:     "gpt-5.6-terra",
		MaxTokens: 1024,
	}, tokenSource, "", func(string) (store.Log[[]json.RawMessage], error) {
		return cache, nil
	})
	p.baseURL = func(string) string { return server.URL }
	return p
}

func newResponseServer(t *testing.T, handler func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(websocket.Handler(handler))
	t.Cleanup(server.Close)
	return server
}

func newResponsesInputLog(t *testing.T) store.Log[message.Message] {
	t.Helper()
	log := store.NewMemLog[message.Message]()
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent("ciao")},
	}})
	require.NoError(t, err)
	return log
}

func TestResponsesProviderStreamsAndCachesAssistant(t *testing.T) {
	requests := make(chan responseCreateRequest, 1)
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		requests <- request
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type":  "response.output_text.delta",
			"delta": "salve",
		}))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"usage": map[string]any{
					"input_tokens":  11,
					"output_tokens": 3,
					"input_tokens_details": map[string]any{
						"cached_tokens":      5,
						"cache_write_tokens": 2,
					},
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{
							map[string]any{"type": "output_text", "text": "salve"},
						},
					},
				},
			},
		}))
	})

	cache := store.NewMemLog[[]json.RawMessage]()
	p := newResponsesTestProvider(server, cache)
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}

	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID:    "aria-1",
		FigLog:    log,
		Snapshot:  chalkboard.Snapshot{"system.credo": json.RawMessage(`"be concise"`)},
		MaxTokens: 77,
	}, bus))

	request := <-requests
	assert.Equal(t, "response.create", request.Type)
	assert.Equal(t, "gpt-5.6-terra", request.Model)
	assert.Equal(t, 77, request.MaxOutputTokens)
	assert.Equal(t, "be concise", request.Instructions)
	require.Len(t, request.Input, 1)
	assert.Contains(t, string(request.Input[0]), `"ciao"`)
	assert.Equal(t, []message.Content{{Type: message.ContentProse, Text: "salve"}}, bus.deltas)
	require.Len(t, bus.messages, 1)
	assert.Equal(t, "salve", bus.messages[0].Content[0].Text)
	assert.Equal(t, message.StopEnd, bus.messages[0].StopReason)
	assert.Equal(t, 11, bus.messages[0].Usage.InputTokens)
	assert.Equal(t, 5, bus.messages[0].Usage.CacheReadTokens)
	assert.Equal(t, 2, bus.messages[0].Usage.CacheWriteTokens)

	entries := cache.Read()
	require.Len(t, entries, 2)
	assert.Equal(t, p.Fingerprint(), entries[0].Fingerprint)
	assert.Equal(t, p.Fingerprint(), entries[1].Fingerprint)
	assert.Contains(t, string(entries[1].Payload[0]), `"output_text"`)
}

func TestResponsesProviderMapsFunctionCalls(t *testing.T) {
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.output_item.added",
			"item": map[string]any{
				"id":      "item-1",
				"type":    "function_call",
				"call_id": "call-1",
				"name":    "bash",
			},
		}))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type":    "response.function_call_arguments.delta",
			"item_id": "item-1",
			"delta":   `{"command":"echo `,
		}))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type":      "response.function_call_arguments.done",
			"item_id":   "item-1",
			"arguments": `{"command":"echo ciao"}`,
		}))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{
						"type":      "function_call",
						"call_id":   "call-1",
						"name":      "bash",
						"arguments": `{"command":"echo ciao"}`,
					},
				},
			},
		}))
	})

	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-1",
		FigLog: log,
		Tools: []provider.Tool{{
			Name:        "bash",
			Description: "Runs a command",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, bus))

	require.Len(t, bus.toolStarts, 1)
	assert.Equal(t, "call-1", bus.toolStarts[0].ToolCallID)
	assert.Equal(t, "bash", bus.toolStarts[0].ToolName)
	assert.Equal(t, []string{`{"command":"echo `}, bus.toolDeltas)
	require.Len(t, bus.toolReady, 1)
	assert.Equal(t, "echo ciao", bus.toolReady[0].Arguments["command"])
	require.Len(t, bus.messages, 1)
	assert.Equal(t, message.StopToolInvoke, bus.messages[0].StopReason)
	require.Len(t, bus.messages[0].Content, 1)
	assert.Equal(t, message.ContentToolInvoke, bus.messages[0].Content[0].Type)
}

func TestResponsesProviderAppliesChalkboardParameters(t *testing.T) {
	requests := make(chan responseCreateRequest, 1)
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		requests <- request
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{
							map[string]any{"type": "output_text", "text": "configured"},
						},
					},
				},
			},
		}))
	})

	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	log := newResponsesInputLog(t)
	temperature := 0.4
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-1",
		FigLog: log,
		Snapshot: chalkboard.Snapshot{
			"system.context_tier":        json.RawMessage(`"long_context"`),
			"system.thinking_effort":     json.RawMessage(`"high"`),
			"system.reasoning_context":   json.RawMessage(`"all_turns"`),
			"system.reasoning_summary":   json.RawMessage(`"auto"`),
			"system.verbosity":           json.RawMessage(`"low"`),
			"system.parallel_tool_calls": json.RawMessage(`false`),
			"system.temperature":         json.RawMessage(`0.4`),
		},
	}, &responseTestBus{}))

	request := <-requests
	require.NotNil(t, request.Reasoning)
	assert.Equal(t, "all_turns", request.Reasoning.Context)
	assert.Equal(t, "high", request.Reasoning.Effort)
	assert.Equal(t, "auto", request.Reasoning.Summary)
	require.NotNil(t, request.Text)
	assert.Equal(t, "low", request.Text.Verbosity)
	assert.False(t, request.ParallelToolCalls)
	require.NotNil(t, request.Temperature)
	assert.Equal(t, temperature, *request.Temperature)
	assert.Nil(t, request.TopP)
}

func TestResponseOptionsRejectInvalidChalkboardParameters(t *testing.T) {
	tests := []struct {
		name string
		snap chalkboard.Snapshot
	}{
		{
			name: "unknown context tier",
			snap: chalkboard.Snapshot{
				"system.context_tier": json.RawMessage(`"wide"`),
			},
		},
		{
			name: "unknown reasoning context",
			snap: chalkboard.Snapshot{
				"system.reasoning_context": json.RawMessage(`"forever"`),
			},
		},
		{
			name: "unknown reasoning summary",
			snap: chalkboard.Snapshot{
				"system.reasoning_summary": json.RawMessage(`"full"`),
			},
		},
		{
			name: "temperature out of range",
			snap: chalkboard.Snapshot{
				"system.temperature": json.RawMessage(`2.1`),
			},
		},
		{
			name: "top p out of range",
			snap: chalkboard.Snapshot{
				"system.top_p": json.RawMessage(`0`),
			},
		},
		{
			name: "temperature and top p",
			snap: chalkboard.Snapshot{
				"system.temperature": json.RawMessage(`0.2`),
				"system.top_p":       json.RawMessage(`0.8`),
			},
		},
		{
			name: "parallel tools wrong type",
			snap: chalkboard.Snapshot{
				"system.parallel_tool_calls": json.RawMessage(`"yes"`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := responseOptionsFor(test.snap)
			require.Error(t, err)
		})
	}
}

func TestResponsesProviderContextTierBudget(t *testing.T) {
	p := newResponsesTestProvider(newResponseServer(t, func(conn *websocket.Conn) { conn.Close() }), store.NewMemLog[[]json.RawMessage]())
	p.SetContextLimits("gpt-5.6-terra", responseContextLimits{Default: 20, Long: 200})
	log := store.NewMemLog[message.Message]()
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent(strings.Repeat("x", 100))},
	}})
	require.NoError(t, err)
	in := provider.SendInput{FigLog: log}

	err = p.validateContext(in, "gpt-5.6-terra", responseRequestOptions{contextTier: "default"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default-context")

	require.NoError(t, p.validateContext(in, "gpt-5.6-terra", responseRequestOptions{contextTier: "long_context"}))

	in.Snapshot = chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`21`),
	}
	err = p.validateContext(in, "gpt-5.6-terra", responseRequestOptions{contextTier: "default"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the default-context limit")
}

func TestCatalogContextLimits(t *testing.T) {
	var model catalogModel
	model.Billing.TokenPrices.Default.MaxPromptTokens = 128000
	model.Billing.TokenPrices.LongContext.MaxPromptTokens = 1000000
	model.Capabilities.Limits.MaxContextWindowTokens = 1000000
	assert.Equal(t, responseContextLimits{Default: 128000, Long: 1000000}, catalogContextLimits(model))
}

func TestCopilotContextLimitUsesCachedCatalog(t *testing.T) {
	var model catalogModel
	model.Billing.TokenPrices.Default.MaxPromptTokens = 128000
	model.Billing.TokenPrices.LongContext.MaxPromptTokens = 1000000
	c := &Copilot{catalog: map[string]catalogModel{"gpt-5.6-terra": model}}

	assert.Equal(t, 128000, c.ContextLimit("gpt-5.6-terra", chalkboard.Snapshot{}))
	assert.Equal(t, 1000000, c.ContextLimit("gpt-5.6-terra", chalkboard.Snapshot{
		"system.context_tier": json.RawMessage(`"long_context"`),
	}))
	assert.Equal(t, 120000, c.ContextLimit("gpt-5.6-terra", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`120000`),
	}))
	assert.Equal(t, 128000, c.ContextLimit("gpt-5.6-terra", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`2000000`),
	}))
}

func TestResponsesProviderInvalidatesCacheOnModelSwitch(t *testing.T) {
	cache := store.NewMemLog[[]json.RawMessage]()
	server := newResponseServer(t, func(conn *websocket.Conn) { conn.Close() })
	p := newResponsesTestProvider(server, cache)
	initialFingerprint := p.Fingerprint()
	_, err := cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    1,
		Payload:     []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)},
		Fingerprint: initialFingerprint,
	})
	require.NoError(t, err)

	p.SetModel("gpt-5.6-luna")
	assert.NotEqual(t, initialFingerprint, p.Fingerprint())
	require.NotNil(t, p.cacheFor("aria-1"))
	assert.Empty(t, cache.Read())
}

func TestResponsesProviderDrivesFigaroToolRoundTrip(t *testing.T) {
	var requestCount atomic.Int32
	firstRequest := make(chan responseCreateRequest, 1)
	secondRequest := make(chan responseCreateRequest, 1)
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))

		switch requestCount.Add(1) {
		case 1:
			firstRequest <- request
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type":  "response.reasoning.delta",
				"delta": "checking ",
			}))
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"id":      "item-1",
					"type":    "function_call",
					"call_id": "call-1",
					"name":    "echo",
				},
			}))
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type":    "response.function_call_arguments.delta",
				"item_id": "item-1",
				"delta":   `{"value":"ciao`,
			}))
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type":      "response.function_call_arguments.done",
				"item_id":   "item-1",
				"arguments": `{"value":"ciao"}`,
			}))
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"status": "completed",
					"output": []any{
						map[string]any{
							"type":              "reasoning",
							"encrypted_content": "opaque-reasoning",
							"summary": []any{
								map[string]any{"type": "summary_text", "text": "checking tool"},
							},
						},
						map[string]any{
							"type":      "function_call",
							"call_id":   "call-1",
							"name":      "echo",
							"arguments": `{"value":"ciao"}`,
						},
					},
				},
			}))
		case 2:
			secondRequest <- request
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type":  "response.output_text.delta",
				"delta": "finished",
			}))
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"status": "completed",
					"output": []any{
						map[string]any{
							"type": "message",
							"role": "assistant",
							"content": []any{
								map[string]any{"type": "output_text", "text": "finished"},
							},
						},
					},
				},
			}))
		default:
			t.Errorf("unexpected request %d", requestCount.Load())
		}
	})

	cache := store.NewMemLog[[]json.RawMessage]()
	p := newResponsesTestProvider(server, cache)
	registry := tool.NewRegistry()
	echo := &responseIntegrationTool{}
	require.NoError(t, registry.Register(echo))
	cb, err := chalkboard.Open("")
	require.NoError(t, err)
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model": json.RawMessage(`"gpt-5.6-terra"`),
	}})
	agent := figaro.NewAgent(figaro.Config{
		ID:         "responses-round-trip",
		SocketPath: t.TempDir() + "/figaro.sock",
		Provider:   &responseAgentProvider{responsesProvider: p},
		Tools:      registry,
		Chalkboard: cb,
	})
	defer agent.Kill()

	notifier := &responseDoneNotifier{done: make(chan struct{})}
	unsubscribe := agent.Subscribe(notifier)
	defer unsubscribe()
	agent.Prompt("use the echo tool")

	select {
	case <-notifier.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Responses tool round trip")
	}

	require.Equal(t, int32(2), requestCount.Load())
	require.Equal(t, int32(1), echo.calls.Load())
	first := <-firstRequest
	second := <-secondRequest
	assert.Equal(t, first.Headers["X-Client-Session-Id"], second.Headers["X-Client-Session-Id"])
	replayed := make([]string, len(second.Input))
	for i, input := range second.Input {
		replayed[i] = string(input)
	}
	joined := strings.Join(replayed, "\n")
	assert.Contains(t, joined, `"encrypted_content":"opaque-reasoning"`)
	assert.Contains(t, joined, `"type":"function_call_output"`)
	assert.Contains(t, joined, `"output":"tool:ciao"`)

	context := agent.Context()
	require.Len(t, context, 4)
	assert.Equal(t, message.RoleUser, context[0].Role)
	assert.Equal(t, message.StopToolInvoke, context[1].StopReason)
	assert.Equal(t, message.ContentThinking, context[1].Content[0].Type)
	assert.Equal(t, "checking tool", context[1].Content[0].Text)
	assert.Equal(t, message.ContentToolInvoke, context[1].Content[1].Type)
	assert.Equal(t, "call-1", context[1].Content[1].ToolCallID)
	assert.Equal(t, message.RoleUser, context[2].Role)
	assert.Equal(t, message.ContentToolResult, context[2].Content[0].Type)
	assert.Equal(t, "tool:ciao", context[2].Content[0].Text)
	assert.Equal(t, message.StopEnd, context[3].StopReason)
	assert.Equal(t, "finished", context[3].Content[0].Text)
}

func TestResponsesProviderCancellationDoesNotAppend(t *testing.T) {
	received := make(chan struct{})
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		close(received)
		var ignored any
		_ = websocket.JSON.Receive(conn, &ignored)
	})

	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- p.Send(ctx, provider.SendInput{AriaID: "aria-1", FigLog: log}, bus)
	}()
	<-received
	cancel()

	require.ErrorIs(t, <-errs, context.Canceled)
	assert.Len(t, log.Read(), 1)
	assert.Empty(t, bus.messages)
}

func TestResponsesProviderDoesNotFailAfterDerivedCacheWriteFailure(t *testing.T) {
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{
							map[string]any{"type": "output_text", "text": "salve"},
						},
					},
				},
			},
		}))
	})
	cache := &failingResponseCache{MemLog: store.NewMemLog[[]json.RawMessage]()}
	p := newResponsesTestProvider(server, cache)
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}

	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-1",
		FigLog: log,
	}, bus))
	require.Len(t, bus.messages, 1)
	assert.Equal(t, "salve", bus.messages[0].Content[0].Text)
	assert.Len(t, log.Read(), 2)
}

func TestResponsesProviderRejectsMalformedFunctionArguments(t *testing.T) {
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.output_item.added",
			"item": map[string]any{
				"id":      "item-1",
				"type":    "function_call",
				"call_id": "call-1",
				"name":    "bash",
			},
		}))
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type":      "response.function_call_arguments.done",
			"item_id":   "item-1",
			"arguments": `{"command":`,
		}))
	})

	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}
	err := p.Send(context.Background(), provider.SendInput{AriaID: "aria-1", FigLog: log}, bus)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `function "bash" arguments`)
	assert.Len(t, log.Read(), 1)
	assert.Empty(t, bus.messages)
}

func TestResponsesInputPreservesCachedAssistantOutput(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	first, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent("question")},
	}})
	require.NoError(t, err)
	second, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleAssistant,
		Content: []message.Content{message.TextContent("answer")},
	}})
	require.NoError(t, err)

	cache := store.NewMemLog[[]json.RawMessage]()
	_, err = cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    first.LT,
		Payload:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}`)},
		Fingerprint: responseFingerprint("gpt-5.6-terra"),
	})
	require.NoError(t, err)
	rawAssistant := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}`)
	_, err = cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    second.LT,
		Payload:     []json.RawMessage{rawAssistant},
		Fingerprint: responseFingerprint("gpt-5.6-terra"),
	})
	require.NoError(t, err)

	server := newResponseServer(t, func(conn *websocket.Conn) { conn.Close() })
	p := newResponsesTestProvider(server, cache)
	input, err := p.inputFor(provider.SendInput{AriaID: "aria-1", FigLog: log})
	require.NoError(t, err)
	require.Len(t, input, 2)
	assert.Equal(t, string(rawAssistant), string(input[1]))
}

func TestResponsesInputPlacesToolOutputBeforeSteeringText(t *testing.T) {
	msg := message.Message{
		Role: message.RoleUser,
		Content: []message.Content{
			message.ToolResultContent("call-1", "bash", "done", false),
			message.TextContent("continue with the result"),
		},
	}
	input, err := encodeResponseMessage(msg, nil, chalkboard.Snapshot{}, nil)
	require.NoError(t, err)
	require.Len(t, input, 2)
	assert.Contains(t, string(input[0]), `"type":"function_call_output"`)
	assert.Contains(t, string(input[1]), `"role":"user"`)
	assert.Contains(t, string(input[1]), "continue with the result")
}

func TestResponsesProviderClearsIncompatibleCache(t *testing.T) {
	cache := store.NewMemLog[[]json.RawMessage]()
	_, err := cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    1,
		Payload:     []json.RawMessage{json.RawMessage(`{"legacy":true}`)},
		Fingerprint: "anthropic-sdk/tag/v1",
	})
	require.NoError(t, err)

	server := newResponseServer(t, func(conn *websocket.Conn) { conn.Close() })
	p := newResponsesTestProvider(server, cache)
	require.NotNil(t, p.cacheFor("aria-1"))
	assert.Empty(t, cache.Read())
}

func TestDecodeResponseAssistantPreservesReasoningSummary(t *testing.T) {
	out, err := decodeResponseAssistant(responseObject{
		Status: "completed",
		Output: []json.RawMessage{json.RawMessage(`{
			"type":"reasoning",
			"summary":[{"type":"summary_text","text":"considered the constraints"}]
		}`)},
	})
	require.NoError(t, err)
	require.Len(t, out.Content, 1)
	assert.Equal(t, message.ContentThinking, out.Content[0].Type)
	assert.Equal(t, "considered the constraints", out.Content[0].Text)
}

func TestDecodeResponseAssistantRejectsNonObjectFunctionArguments(t *testing.T) {
	_, err := decodeResponseAssistant(responseObject{
		Status: "completed",
		Output: []json.RawMessage{json.RawMessage(`{
			"type":"function_call",
			"call_id":"call-1",
			"name":"bash",
			"arguments":"[]"
		}`)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `function "bash" arguments`)
}

func TestCopilotSeparatesMessagesAndResponsesCaches(t *testing.T) {
	messages := store.NewMemLog[[]json.RawMessage]()
	responses := store.NewMemLog[[]json.RawMessage]()
	p, err := New(
		provider.Knobs{Model: "gpt-5.6-terra"},
		&staticResponseTokenSource{token: "test-token"},
		"",
		func(string) (store.Log[[]json.RawMessage], error) { return messages, nil },
		func(string) (store.Log[[]json.RawMessage], error) { return responses, nil },
	)
	require.NoError(t, err)

	messageCache, err := p.inner.CacheOpen("aria-1")
	require.NoError(t, err)
	responseCache, err := p.responses.cacheOpen("aria-1")
	require.NoError(t, err)
	_, err = messageCache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    1,
		Payload:     []json.RawMessage{json.RawMessage(`{"type":"message"}`)},
		Fingerprint: p.inner.Fingerprint(),
	})
	require.NoError(t, err)
	assert.Empty(t, responseCache.Read())
}

func TestEncodeResponseMessageIsDeterministic(t *testing.T) {
	msg := message.Message{
		Role: message.RoleAssistant,
		Content: []message.Content{
			message.TextContent("answer"),
			{
				Type:       message.ContentToolInvoke,
				ToolCallID: "call-1",
				ToolName:   "bash",
				Arguments:  map[string]interface{}{"command": "echo ciao"},
			},
		},
	}
	first, err := encodeResponseMessage(msg, nil, chalkboard.Snapshot{}, nil)
	require.NoError(t, err)
	second, err := encodeResponseMessage(msg, nil, chalkboard.Snapshot{}, nil)
	require.NoError(t, err)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(firstJSON, secondJSON))
}

func TestRouteForCatalogModel(t *testing.T) {
	assert.Equal(t, modelRouteResponses, routeForCatalogModel(catalogModel{
		ID:                 "gpt-5.6-terra",
		SupportedEndpoints: []string{"/responses"},
	}))
	assert.Equal(t, modelRouteMessages, routeForCatalogModel(catalogModel{
		ID:                 "claude-sonnet-4.5",
		SupportedEndpoints: []string{"/v1/messages"},
	}))
	assert.Equal(t, modelRouteUnknown, routeForCatalogModel(catalogModel{
		ID:                 "unknown",
		SupportedEndpoints: []string{"/chat/completions"},
	}))
}

func TestResponsesEndpoint(t *testing.T) {
	assert.Equal(t, "wss://api.enterprise.githubcopilot.com/responses",
		responsesEndpoint("https://api.enterprise.githubcopilot.com"))
	assert.Equal(t, "ws://127.0.0.1:8080/responses",
		responsesEndpoint("http://127.0.0.1:8080"))
}

func TestResponseHeadersMatchResponsesHandshakeShape(t *testing.T) {
	headers := responseHeaders("opaque-token", "task-1", "session-1", "interaction-1", "machine-1")
	assert.Equal(t, "Bearer opaque-token", headers.Get("Authorization"))
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "session-1", headers.Get("X-Client-Session-Id"))
	assert.Equal(t, "github.com", headers.Get("X-Github-Repository-Host"))
	assert.Contains(t, headers.Get("Copilot-Integration-Id"), "vscode")
}

func TestResponseFunctionArgumentsAcceptJSONStringOrObject(t *testing.T) {
	assert.JSONEq(t, `{"command":"echo ciao"}`,
		string(responseArgumentBytes(json.RawMessage(`"{\"command\":\"echo ciao\"}"`))))
	assert.JSONEq(t, `{"command":"echo ciao"}`,
		string(responseArgumentBytes(json.RawMessage(`{"command":"echo ciao"}`))))
}

func TestResponsesProviderHonorsContextDeadline(t *testing.T) {
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		time.Sleep(100 * time.Millisecond)
	})
	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	log := newResponsesInputLog(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := p.Send(ctx, provider.SendInput{AriaID: "aria-1", FigLog: log}, &responseTestBus{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
