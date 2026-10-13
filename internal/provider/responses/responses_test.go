package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
	"github.com/jack-work/figaro/internal/tool"
)

type staticResponseTokenSource struct {
	token       string
	invalidated int
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
	cache       []provider.AssistantCache
	messageEnds []string
}

type responseAgentProvider struct {
	*Provider
}

func (*responseAgentProvider) Name() string { return "test" }

func (*responseAgentProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "gpt-5.6-terra", Provider: "test"}}, nil
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

func (b *responseTestBus) PushFigaro(msg message.Message, cache ...provider.AssistantCache) {
	b.messages = append(b.messages, msg)
	b.cache = append(b.cache, cache...)
}

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
) *Provider {
	tokenSource := &staticResponseTokenSource{token: "test-token"}
	p := New(provider.Knobs{
		Model:     "gpt-5.6-terra",
		MaxTokens: 1024,
	}, tokenSource, testConfig, func(string) (store.Log[[]json.RawMessage], error) {
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
		Role:    message.RoleInput,
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
		Snapshot:  form.FromMap(map[string]json.RawMessage{"system.credo": json.RawMessage(`"be concise"`)}),
		MaxTokens: 77,
	}, bus))

	request := <-requests
	assert.Equal(t, "response.create", request.Type)
	assert.Equal(t, "gpt-5.6-terra", request.Model)
	assert.Equal(t, 77, request.MaxOutputTokens)
	assert.Equal(t, "be concise", request.Instructions)
	require.Len(t, request.Input, 1)
	assert.Contains(t, string(request.Input[0]), `"ciao"`)
	assert.Equal(t, provider.SessionKey("aria-1"), request.PromptCacheKey)
	assert.NotEmpty(t, request.PromptCacheKey)
	assert.NotContains(t, request.PromptCacheKey, "aria-1", "the key must not leak the aria id")
	// A single user turn has no completed exchange to mark.
	assert.NotContains(t, string(request.Input[0]), "prompt_cache_breakpoint")
	assert.Equal(t, []message.Content{{Type: message.ContentProse, Text: "salve"}}, bus.deltas)
	require.Len(t, bus.messages, 1)
	assert.Equal(t, "salve", bus.messages[0].Content[0].Text)
	assert.Equal(t, message.StopEnd, bus.messages[0].StopReason)
	// input_tokens (11) includes the cached (5) and written (2) tokens.
	assert.Equal(t, 4, bus.messages[0].Usage.InputTokens)
	assert.Equal(t, 5, bus.messages[0].Usage.CacheReadTokens)
	assert.Equal(t, 2, bus.messages[0].Usage.CacheWriteTokens)
	assert.Equal(t, 3, bus.messages[0].Usage.OutputTokens)
	assert.Equal(t, 11+3, tokens.ContextFromUsage(bus.messages[0].Usage))

	entries := cache.Read()
	require.Len(t, entries, 1)
	assert.Equal(t, p.Fingerprint(), entries[0].Fingerprint)
	require.Len(t, bus.cache, 1)
	assert.Equal(t, testConfig.Namespace, bus.cache[0].Namespace)
	assert.Equal(t, p.Fingerprint(), bus.cache[0].Fingerprint)
	assert.Contains(t, string(bus.cache[0].Payload[0]), `"output_text"`)
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

func TestResponsesProviderAppliesFormParameters(t *testing.T) {
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
		Snapshot: form.FromMap(map[string]json.RawMessage{
			"system.context_tier":        json.RawMessage(`"long_context"`),
			"system.thinking_effort":     json.RawMessage(`"high"`),
			"system.reasoning_context":   json.RawMessage(`"all_turns"`),
			"system.reasoning_summary":   json.RawMessage(`"auto"`),
			"system.verbosity":           json.RawMessage(`"low"`),
			"system.parallel_tool_calls": json.RawMessage(`false`),
			"system.temperature":         json.RawMessage(`0.4`),
		}),
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

func TestResponseOptionsRejectInvalidFormParameters(t *testing.T) {
	tests := []struct {
		name string
		snap form.Snapshot
	}{
		{
			name: "unknown context tier",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.context_tier": json.RawMessage(`"wide"`),
			}),
		},
		{
			name: "unknown reasoning context",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.reasoning_context": json.RawMessage(`"forever"`),
			}),
		},
		{
			name: "unknown reasoning summary",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.reasoning_summary": json.RawMessage(`"full"`),
			}),
		},
		{
			name: "temperature out of range",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.temperature": json.RawMessage(`2.1`),
			}),
		},
		{
			name: "top p out of range",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.top_p": json.RawMessage(`0`),
			}),
		},
		{
			name: "temperature and top p",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.temperature": json.RawMessage(`0.2`),
				"system.top_p":       json.RawMessage(`0.8`),
			}),
		},
		{
			name: "parallel tools wrong type",
			snap: form.FromMap(map[string]json.RawMessage{
				"system.parallel_tool_calls": json.RawMessage(`"yes"`),
			}),
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
	p.SetContextLimits("gpt-5.6-terra", ContextLimits{Default: 20, Long: 200})
	log := store.NewMemLog[message.Message]()
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent(strings.Repeat("x", 100))},
	}})
	require.NoError(t, err)
	in := provider.SendInput{FigLog: log}

	err = p.validateContext(in, "gpt-5.6-terra", responseRequestOptions{contextTier: "default"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default-context")

	require.NoError(t, p.validateContext(in, "gpt-5.6-terra", responseRequestOptions{contextTier: "long_context"}))

	in.Snapshot = form.FromMap(map[string]json.RawMessage{
		"system.max_context_tokens": json.RawMessage(`21`),
	})
	err = p.validateContext(in, "gpt-5.6-terra", responseRequestOptions{contextTier: "default"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the default-context limit")
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
	opened, err := p.cacheFor("aria-1")
	require.NoError(t, err)
	require.NotNil(t, opened)
	assert.Empty(t, cache.Read())
}

func TestResponsesProviderDrivesFigaroToolRoundTrip(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "valid"
		if malformed {
			name = "quarantined"
		}
		t.Run(name, func(t *testing.T) { runResponsesToolRoundTrip(t, malformed) })
	}
}

func runResponsesToolRoundTrip(t *testing.T, malformed bool) {
	arguments := `{"value":"ciao"}`
	if malformed {
		arguments = `{"value":"unfinished`
	}
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
				"arguments": arguments,
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
							"arguments": arguments,
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

	registry := tool.NewRegistry()
	echo := &responseIntegrationTool{}
	require.NoError(t, registry.Register(echo))
	cb, err := form.Open("")
	require.NoError(t, err)
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model": json.RawMessage(`"gpt-5.6-terra"`),
	}, nil))
	be, ariaID := store.NewTestAria(t, "d", message.Patch{})
	// The provider must read the same translator log the agent commits to.
	cache, err := be.OpenTranslator(ariaID, testConfig.Namespace)
	require.NoError(t, err)
	p := newResponsesTestProvider(server, cache)
	agent := figaro.NewAgent(figaro.Config{
		ID:         ariaID,
		SocketPath: t.TempDir() + "/figaro.sock",
		Provider:   &responseAgentProvider{Provider: p},
		Tools:      registry,
		Backend:    be,
		Form:       cb,
	})
	defer agent.Kill()

	notifier := &responseDoneNotifier{done: make(chan struct{})}
	unsubscribe := agent.Subscribe(notifier)
	defer unsubscribe()
	agent.SubmitPrompt(rpc.QuaRequest{Text: "use the echo tool"})

	select {
	case <-notifier.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Responses tool round trip")
	}

	require.Equal(t, int32(2), requestCount.Load())
	if malformed {
		require.Zero(t, echo.calls.Load(), "quarantined input must never execute")
	} else {
		require.Equal(t, int32(1), echo.calls.Load())
	}
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
	if malformed {
		assert.Contains(t, joined, "INVALID_JSON")
	} else {
		assert.Contains(t, joined, `"output":"tool:ciao"`)
	}

	var context []message.Message
	for _, m := range agent.Context() {
		if !message.IsCeremonial(m) {
			context = append(context, m)
		}
	}
	require.Len(t, context, 4)
	assert.Equal(t, message.RoleInput, context[0].Role)
	assert.Equal(t, message.StopToolInvoke, context[1].StopReason)
	assert.Equal(t, message.ContentThinking, context[1].Content[0].Type)
	assert.Equal(t, "checking tool", context[1].Content[0].Text)
	assert.Equal(t, message.ContentToolInvoke, context[1].Content[1].Type)
	assert.Equal(t, "call-1", context[1].Content[1].ToolCallID)
	assert.Equal(t, message.RoleInput, context[2].Role)
	assert.Equal(t, message.ContentToolResult, context[2].Content[0].Type)
	if malformed {
		assert.True(t, context[2].Content[0].IsError)
		assert.Equal(t, message.InvalidJSONResult(arguments), context[2].Content[0].Text)
	} else {
		assert.Equal(t, "tool:ciao", context[2].Content[0].Text)
	}
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

func TestResponsesProviderSuppliesCacheWithoutAppendingIt(t *testing.T) {
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
	cache := store.NewMemLog[[]json.RawMessage]()
	p := newResponsesTestProvider(server, cache)
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}

	err := p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-1",
		FigLog: log,
	}, bus)
	require.NoError(t, err)
	require.Len(t, bus.messages, 1)
	require.Len(t, bus.cache, 1)
	assert.Equal(t, "salve", bus.messages[0].Content[0].Text)
	assert.Len(t, log.Read(), 1)
}

func TestResponsesProviderQuarantinesMalformedFunctionArguments(t *testing.T) {
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
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.completed", "response": map[string]any{
				"status": "completed", "output": []any{map[string]any{
					"type": "function_call", "call_id": "call-1", "name": "bash", "arguments": `{"command":`,
				}},
			},
		}))
	})

	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	log := newResponsesInputLog(t)
	bus := &responseTestBus{}
	err := p.Send(context.Background(), provider.SendInput{AriaID: "aria-1", FigLog: log}, bus)
	require.NoError(t, err)
	assert.Len(t, log.Read(), 1)
	require.Len(t, bus.messages, 1)
	require.Len(t, bus.toolReady, 1)
	raw, bad := message.MalformedArgsOf(bus.messages[0].Content[0])
	require.True(t, bad)
	assert.Equal(t, `{"command":`, raw)
	assert.Equal(t, bus.messages[0].Content[0].Arguments, bus.toolReady[0].Arguments)
	require.Len(t, bus.cache, 1)
	var native responseOutputItem
	require.NoError(t, json.Unmarshal(bus.cache[0].Payload[0], &native))
	_, err = decodeResponseArguments(string(responseArgumentBytes(native.Arguments)))
	require.NoError(t, err, "quarantine must also make the cached native input replayable")
}

func TestResponsesInputPreservesCachedAssistantOutput(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	first, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("question")},
	}})
	require.NoError(t, err)
	second, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleOutput,
		Content: []message.Content{message.TextContent("answer")},
	}})
	require.NoError(t, err)

	cache := store.NewMemLog[[]json.RawMessage]()
	_, err = cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    first.LT,
		Payload:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}`)},
		Fingerprint: testFingerprint("gpt-5.6-terra"),
	})
	require.NoError(t, err)
	rawAssistant := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}`)
	_, err = cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    second.LT,
		Payload:     []json.RawMessage{rawAssistant},
		Fingerprint: testFingerprint("gpt-5.6-terra"),
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
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call-1", "bash", "done", false),
			message.TextContent("continue with the result"),
		},
	}
	input, err := encodeResponseMessage(msg, nil, form.Snapshot{}, nil)
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
	opened, err := p.cacheFor("aria-1")
	require.NoError(t, err)
	require.NotNil(t, opened)
	assert.Empty(t, cache.Read())
}

func TestResponsesInputWarmPreservesPrefixBytes(t *testing.T) {
	cache := store.NewMemLog[[]json.RawMessage]()
	server := newResponseServer(t, func(conn *websocket.Conn) { conn.Close() })
	p := newResponsesTestProvider(server, cache)
	log := newResponsesInputLog(t)
	in := provider.SendInput{AriaID: "aria-1", FigLog: log}

	first, err := p.inputFor(in)
	require.NoError(t, err)
	prefix := append([]byte(nil), first[0]...)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("next")},
	}})
	require.NoError(t, err)
	second, err := p.inputFor(in)
	require.NoError(t, err)

	assert.Greater(t, len(second), len(first))
	assert.Equal(t, prefix, []byte(second[0]))
}

func TestContextSizeForLogMatchesCanonicalEstimator(t *testing.T) {
	for _, withUsage := range []bool{false, true} {
		log := store.NewMemLog[message.Message]()
		var messages []message.Message
		for i := 0; i < 100; i++ {
			msg := message.Message{
				Role:    message.RoleInput,
				Content: []message.Content{message.TextContent(fmt.Sprintf("message-%d", i))},
			}
			if withUsage && i == 75 {
				msg.Usage = &message.Usage{InputTokens: 700, OutputTokens: 30}
			}
			messages = append(messages, msg)
			_, err := log.Append(store.Entry[message.Message]{Payload: msg})
			require.NoError(t, err)
		}

		wantTokens, wantExact := tokens.ContextSize(messages)
		gotTokens, gotExact := contextSizeForLog(log)
		assert.Equal(t, wantTokens, gotTokens)
		assert.Equal(t, wantExact, gotExact)
	}
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

func TestDecodeResponseAssistantQuarantinesNonObjectFunctionArguments(t *testing.T) {
	out, err := decodeResponseAssistant(responseObject{
		Status: "completed",
		Output: []json.RawMessage{json.RawMessage(`{
			"type":"function_call",
			"call_id":"call-1",
			"name":"bash",
			"arguments":"[]"
		}`)},
	})
	require.NoError(t, err)
	require.Len(t, out.Content, 1)
	raw, bad := message.MalformedArgsOf(out.Content[0])
	require.True(t, bad)
	assert.Equal(t, "[]", raw)
}

func TestEncodeResponseMessageIsDeterministic(t *testing.T) {
	msg := message.Message{
		Role: message.RoleOutput,
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
	first, err := encodeResponseMessage(msg, nil, form.Snapshot{}, nil)
	require.NoError(t, err)
	second, err := encodeResponseMessage(msg, nil, form.Snapshot{}, nil)
	require.NoError(t, err)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(firstJSON, secondJSON))
}

func TestResponsesEndpoint(t *testing.T) {
	assert.Equal(t, "wss://api.enterprise.githubcopilot.com/responses",
		responsesEndpoint("https://api.enterprise.githubcopilot.com"))
	assert.Equal(t, "ws://127.0.0.1:8080/responses",
		responsesEndpoint("http://127.0.0.1:8080"))
}

func TestResponseFunctionArgumentsAcceptJSONStringOrObject(t *testing.T) {
	assert.Equal(t, "null", string(responseArgumentBytes(json.RawMessage(`null`))))
	assert.Equal(t, "[]", string(responseArgumentBytes(json.RawMessage(`[]`))))
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

func assistantItem(t *testing.T, text string) json.RawMessage {
	t.Helper()
	raw, err := marshalResponseItem(responseMessage("assistant", []responseContent{
		{Type: "output_text", Text: text},
	}))
	require.NoError(t, err)
	return raw
}

func userItem(t *testing.T, text string) json.RawMessage {
	t.Helper()
	raw, err := marshalResponseItem(responseMessage("user", []responseContent{
		{Type: "input_text", Text: text},
	}))
	require.NoError(t, err)
	return raw
}

func breakpointsIn(t *testing.T, input []json.RawMessage) []int {
	t.Helper()
	var out []int
	for i, raw := range input {
		var item responseInputItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		for _, c := range item.Content {
			if c.PromptCacheBreakpoint != nil {
				out = append(out, i)
			}
		}
	}
	return out
}

// The mark goes behind the newest turn: GPT-5.6 does not fall back to the
// longest matching unmarked prefix.
func TestPromptCacheBreakpointLandsOnTheLastAssistantItem(t *testing.T) {
	input := []json.RawMessage{
		userItem(t, "first"),
		assistantItem(t, "first reply"),
		userItem(t, "second"),
	}
	marked, n := markPromptCacheBreakpoint(input)
	assert.Equal(t, 1, n)
	assert.Equal(t, []int{1}, breakpointsIn(t, marked),
		"the mark belongs on the last assistant item, not on the new user turn")
}

// inputFor returns the live projection slice; stamping it in place would
// bake a moving marker into the per-LT cache.
func TestPromptCacheBreakpointDoesNotMutateTheProjection(t *testing.T) {
	input := []json.RawMessage{
		userItem(t, "first"),
		assistantItem(t, "first reply"),
		userItem(t, "second"),
	}
	before := make([]string, len(input))
	for i, raw := range input {
		before[i] = string(raw)
	}

	marked, n := markPromptCacheBreakpoint(input)
	require.Equal(t, 1, n)

	for i, raw := range input {
		assert.Equal(t, before[i], string(raw),
			"item %d of the caller's slice was mutated; that slice is the retained projection", i)
	}
	assert.NotEqual(t, string(input[1]), string(marked[1]), "the returned copy must carry the mark")
	assert.Empty(t, breakpointsIn(t, input), "the projection must stay unmarked")
}

func TestPromptCacheBreakpointIsIdempotentAcrossTurns(t *testing.T) {
	input := []json.RawMessage{userItem(t, "first"), assistantItem(t, "reply"), userItem(t, "second")}
	once, _ := markPromptCacheBreakpoint(input)
	twice, _ := markPromptCacheBreakpoint(input)
	assert.Equal(t, breakpointsIn(t, once), breakpointsIn(t, twice))
	assert.Len(t, breakpointsIn(t, twice), 1, "one mark per request, never accumulating")
}

func TestPromptCacheBreakpointSkippedOnTheFirstTurn(t *testing.T) {
	input := []json.RawMessage{userItem(t, "only turn")}
	marked, n := markPromptCacheBreakpoint(input)
	assert.Equal(t, 0, n)
	assert.Empty(t, breakpointsIn(t, marked))
}

// No prompt_cache_options.mode is sent, so automatic breakpoints stay in play.
func TestNoExplicitModeIsSent(t *testing.T) {
	body, err := json.Marshal(responseCreateRequest{Type: "response.create", responseBody: responseBody{Model: "gpt-5.6-sol"}})
	require.NoError(t, err)
	assert.NotContains(t, string(body), "prompt_cache_options",
		"mode=explicit disables OpenAI-managed breakpoints; ship the additive half first")
}

// The key must match openaichat's derivation and never be the raw aria id.
func TestPromptCacheKeyMatchesTheSharedDerivation(t *testing.T) {
	const aria = "4cf7d08f"
	key := provider.SessionKey(aria)
	assert.NotEmpty(t, key)
	assert.NotContains(t, key, aria, "the key must not leak the aria id")
	assert.Equal(t, key, provider.SessionKey(aria), "stable across turns and restarts")
	assert.NotEqual(t, key, provider.SessionKey("30bc6333"))

	body, err := json.Marshal(responseCreateRequest{
		Type: "response.create", responseBody: responseBody{Model: "gpt-5.6-sol", PromptCacheKey: key},
	})
	require.NoError(t, err)
	assert.Contains(t, string(body), `"prompt_cache_key":"`+key+`"`)
}

func TestPromptCacheBreakpointOnTheWireAcrossTurns(t *testing.T) {
	requests := make(chan responseCreateRequest, 3)
	reply := func(conn *websocket.Conn, text string) {
		require.NoError(t, websocket.JSON.Send(conn, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"usage": map[string]any{
					"input_tokens":  2000,
					"output_tokens": 2,
					"input_tokens_details": map[string]any{
						"cached_tokens":      1920,
						"cache_write_tokens": 0,
					},
				},
				"output": []any{
					map[string]any{
						"type": "message", "role": "assistant",
						"content": []any{map[string]any{"type": "output_text", "text": text}},
					},
				},
			},
		}))
	}
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var request responseCreateRequest
		require.NoError(t, websocket.JSON.Receive(conn, &request))
		requests <- request
		reply(conn, "ok")
	})

	cache := store.NewMemLog[[]json.RawMessage]()
	p := newResponsesTestProvider(server, cache)
	log := newResponsesInputLog(t)
	snap := form.FromMap(map[string]json.RawMessage{"system.credo": json.RawMessage(`"be concise"`)})

	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-gc", FigLog: log, Snapshot: snap, MaxTokens: 64,
	}, &responseTestBus{}))
	first := <-requests

	// This test plays the drain loop's part and appends the assistant.
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleOutput,
		Content: []message.Content{message.TextContent("first reply")},
	}})
	require.NoError(t, err)

	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("again")},
	}})
	require.NoError(t, err)
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-gc", FigLog: log, Snapshot: snap, MaxTokens: 64,
	}, &responseTestBus{}))
	second := <-requests

	countBreakpoints := func(items []json.RawMessage) int {
		n := 0
		for _, raw := range items {
			var item responseInputItem
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			for _, c := range item.Content {
				if c.PromptCacheBreakpoint != nil {
					n++
				}
			}
		}
		return n
	}

	assert.Equal(t, 0, countBreakpoints(first.Input),
		"turn 1 has no completed exchange behind it; the implicit breakpoint stands alone")
	assert.Equal(t, 1, countBreakpoints(second.Input),
		"turn 2 must carry exactly one explicit breakpoint")

	// Only a third turn can show a marker accumulated by in-place stamping.
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("third")},
	}})
	require.NoError(t, err)
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-gc", FigLog: log, Snapshot: snap, MaxTokens: 64,
	}, &responseTestBus{}))
	third := <-requests
	assert.Equal(t, 1, countBreakpoints(third.Input),
		"exactly one marker per request; markers must not accumulate across turns")

	var marked responseInputItem
	for _, raw := range second.Input {
		var item responseInputItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		for _, c := range item.Content {
			if c.PromptCacheBreakpoint != nil {
				marked = item
			}
		}
	}
	assert.Equal(t, "assistant", marked.Role,
		"the mark belongs behind the newest turn, on a boundary that existed last turn")

	assert.Equal(t, provider.SessionKey("aria-gc"), first.PromptCacheKey)
	assert.Equal(t, first.PromptCacheKey, second.PromptCacheKey,
		"a key that changes between turns is a key that pins nothing")

	// The per-LT cache must never carry the moving marker.
	for _, entry := range cache.Read() {
		for _, raw := range entry.Payload {
			assert.NotContains(t, string(raw), "prompt_cache_breakpoint",
				"a marker baked into the translation cache could never move forward")
		}
	}
}

// The Copilot proxy re-encrypts item_id on every delta; output_index is the
// stable handle.
func TestResponseCallForReencryptedItemID(t *testing.T) {
	calls := map[string]*responseCall{}
	items := map[string]*responseCall{}
	byIndex := map[int]*responseCall{}

	added := responseStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: responseIndex(1),
		Item: responseOutputItem{
			Type:   "function_call",
			ID:     "ENCRYPTED-BLOB-AT-ADD",
			CallID: "call_abc123",
			Name:   "write",
		},
	}
	call := ensureResponseCall(calls, added.Item.CallID, added.Item.Name)
	items[added.Item.ID] = call
	byIndex[*added.OutputIndex] = call

	delta := responseStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: responseIndex(1),
		ItemID:      "A-DIFFERENT-ENCRYPTION-OF-THE-SAME-ITEM",
		Delta:       `{"pa`,
	}
	if got := responseCallFor(calls, items, byIndex, delta); got != call {
		t.Fatalf("delta with a re-encrypted item_id did not resolve to its call: got %v, want %p", got, call)
	}

	byItem := responseStreamEvent{ItemID: "ENCRYPTED-BLOB-AT-ADD", OutputIndex: responseIndex(99)}
	if got := responseCallFor(calls, items, byIndex, byItem); got != call {
		t.Fatalf("item_id lookup regressed: got %v, want %p", got, call)
	}
	byCall := responseStreamEvent{CallID: "call_abc123"}
	if got := responseCallFor(calls, items, byIndex, byCall); got != call {
		t.Fatalf("call_id lookup regressed: got %v, want %p", got, call)
	}

	stray := responseStreamEvent{ItemID: "UNKNOWN", OutputIndex: responseIndex(7)}
	if got := responseCallFor(calls, items, byIndex, stray); got != nil {
		t.Fatalf("unknown output_index resolved to %v, want nil", got)
	}
}

var testConfig = Config{
	Namespace: "test-responses",
	BaseURL:   func(string) string { return "http://127.0.0.1:1" },
}

func testFingerprint(model string) string {
	return (&Provider{namespace: testConfig.Namespace}).fingerprintFor(model)
}
