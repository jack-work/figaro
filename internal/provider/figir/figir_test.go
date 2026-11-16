package figir

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type rotatingCred struct {
	mu          sync.Mutex
	tokens      []string
	invalidated []string
}

func (r *rotatingCred) Resolve() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[0], nil
}

func (r *rotatingCred) Invalidate(tok string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, tok)
	if len(r.tokens) > 1 && r.tokens[0] == tok {
		r.tokens = r.tokens[1:]
	}
	return nil
}

type captureBus struct {
	deltas   []message.Content
	starts   []string
	argParts []string
	ready    []message.Content
	endStop  string
	final    *message.Message
	caches   int
}

func (b *captureBus) PushDelta(c message.Content)              { b.deltas = append(b.deltas, c) }
func (b *captureBus) PushToolInvokeStart(id, name string)      { b.starts = append(b.starts, id+":"+name) }
func (b *captureBus) PushToolInvokeDelta(id, part string)      { b.argParts = append(b.argParts, id+":"+part) }
func (b *captureBus) PushToolReady(c message.Content)          { b.ready = append(b.ready, c) }
func (b *captureBus) PushMessageEnd(stop string)               { b.endStop = stop }
func (b *captureBus) PushFigaro(m message.Message, c ...provider.AssistantCache) {
	cp := m
	b.final = &cp
	b.caches += len(c)
}

func TestProviderDoesNotImplementEntryEncoder(t *testing.T) {
	var p any = &Provider{}
	_, ok := p.(provider.EntryEncoder)
	assert.False(t, ok, "figir encodes directly from FigLog and must never write translations-v2")
}

func TestLoopbackRoundTripPreservesSignatureMetaRemindersAndRenewsOn401(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []Request
		headers  []http.Header
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer fresh-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"data":[{"id":"gemini-3-pro","context_window":1048576}]}`)
		case "/v1/fig-ir":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			if r.Header.Get("Authorization") != "Bearer fresh-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var req Request
			require.NoError(t, json.Unmarshal(body, &req))
			mu.Lock()
			requests = append(requests, req)
			headers = append(headers, r.Header.Clone())
			turn := len(requests)
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			if turn == 1 {
				events := []string{
					`data: {"type":"thinking_delta","text":"pondering ","meta":{"gemini_part":{"thought":true}}}`,
					`data: {"type":"thinking_delta","text":"deeply","signature":"sig-thought-1"}`,
					`data: {"type":"prose_delta","text":"Let me check.","signature":"sig-prose-1"}`,
					`data: {"type":"tool_invoke_start","tool_call_id":"call_1","tool_name":"read","signature":"sig-call-1","meta":{"thought_signature":"ts-1"}}`,
					`data: {"type":"tool_invoke_delta","tool_call_id":"call_1","partial_json":"{\"path\":"}`,
					`data: {"type":"tool_invoke_end","tool_call_id":"call_1","partial_json":"\"README.md\"}"}`,
					`data: {"type":"message_end","stop_reason":"tool_invoke","wire_stop":"STOP","usage":{"input_tokens":120,"output_tokens":35,"cache_read_tokens":100}}`,
					`data: [DONE]`,
				}
				io.WriteString(w, strings.Join(events, "\n\n")+"\n\n")
				return
			}
			events := []string{
				`data: {"type":"prose_delta","text":"All clear."}`,
				`data: {"type":"message_end","stop_reason":"stop","wire_stop":"STOP","usage":{"input_tokens":150,"output_tokens":10}}`,
				`data: [DONE]`,
			}
			io.WriteString(w, strings.Join(events, "\n\n")+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cred := &rotatingCred{tokens: []string{"stale-token", "fresh-token"}}
	route := provider.Route{
		Name:    "gemini-gw",
		BaseURL: srv.URL + "/v1",
		Dialect: provider.DialectFigIR,
		Auth:    provider.AuthBearer,
		Headers: map[string]string{"X-Gateway": "local"},
	}
	p, err := New(provider.Knobs{Model: "gemini-3-pro", MaxTokens: 4096}, cred, route)
	require.NoError(t, err)
	p.HTTPClient = srv.Client()

	// 401 on first attempt triggers Invalidate("stale-token") and succeeds with "fresh-token".
	models, err := p.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "gemini-3-pro", models[0].ID)
	assert.Equal(t, []string{"stale-token"}, cred.invalidated)

	initMap := map[string]json.RawMessage{
		"mantra":                 json.RawMessage(`"ship fig-ir"`),
		"system.credo":           json.RawMessage(`"You are Figaro."`),
		"system.thinking_effort": json.RawMessage(`"high"`),
		"system.thinking_budget": json.RawMessage(`8192`),
	}
	initPatch := form.Build(form.Snapshot{}, initMap, nil)
	be, ariaID := store.NewTestAria(t, "d", initPatch)
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)

	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Patches: []message.Patch{initPatch},
		Content: []message.Content{message.TextContent("inspect README")},
	}})
	require.NoError(t, err)

	snap := form.FromMap(initMap)
	bus1 := &captureBus{}
	err = p.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snap,
		MaxTokens: 2048,
		Tools:     []provider.Tool{{Name: "read", Description: "Read file"}},
	}, bus1)
	require.NoError(t, err)
	require.NotNil(t, bus1.final)
	assert.Equal(t, 0, bus1.caches, "figir pushes no native translator cache rows")
	assert.Equal(t, message.StopToolInvoke, bus1.final.StopReason)
	require.Len(t, bus1.final.Content, 3)
	assert.Equal(t, "pondering deeply", bus1.final.Content[0].Text)
	assert.Equal(t, "sig-thought-1", bus1.final.Content[0].Signature)
	assert.Contains(t, string(bus1.final.Content[0].Meta["gemini_part"]), `"thought":true`)
	assert.Equal(t, "sig-prose-1", bus1.final.Content[1].Signature)
	assert.Equal(t, "call_1", bus1.final.Content[2].ToolCallID)
	assert.Equal(t, "README.md", bus1.final.Content[2].Arguments["path"])
	assert.Equal(t, "sig-call-1", bus1.final.Content[2].Signature)
	assert.Equal(t, `"ts-1"`, string(bus1.final.Content[2].Meta["thought_signature"]))

	// Append assistant turn + tool result with a form patch, then send turn 2.
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: *bus1.final})
	require.NoError(t, err)
	notePatch := form.Build(snap, map[string]json.RawMessage{"note": json.RawMessage(`"checked"`)}, nil)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleToolResult,
		Patches: []message.Patch{notePatch},
		Content: []message.Content{message.ToolResultContent("call_1", "read", "# Figaro", false)},
	}})
	require.NoError(t, err)

	bus2 := &captureBus{}
	err = p.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snap.Apply(notePatch),
		MaxTokens: 2048,
	}, bus2)
	require.NoError(t, err)
	require.NotNil(t, bus2.final)
	assert.Equal(t, message.StopEnd, bus2.final.StopReason)
	assert.Equal(t, "All clear.", bus2.final.Content[0].Text)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	assert.Equal(t, "local", headers[0].Get("X-Gateway"))
	assert.NotEmpty(t, headers[0].Get("x-session-id"))
	assert.Equal(t, "You are Figaro.", requests[0].System)
	assert.Equal(t, "high", requests[0].ThinkingEffort)
	assert.Equal(t, 8192, requests[0].ThinkingBudget)

	// Prefix stability: message 0 in turn 1 and turn 2 must be byte-identical.
	assert.Equal(t, string(requests[0].Messages[0]), string(requests[1].Messages[0]))
	var firstMsg WireMessage
	require.NoError(t, json.Unmarshal(requests[0].Messages[0], &firstMsg))
	require.Len(t, firstMsg.Content, 2)
	assert.Contains(t, firstMsg.Content[1].Text, `<system-reminder name="mantra">`)

	// Turn 2's replayed assistant message must carry Signature and Meta intact.
	var replayed WireMessage
	require.NoError(t, json.Unmarshal(requests[1].Messages[1], &replayed))
	assert.Equal(t, "output", replayed.Role)
	require.Len(t, replayed.Content, 3)
	assert.Equal(t, "sig-thought-1", replayed.Content[0].Signature)
	assert.Equal(t, "sig-call-1", replayed.Content[2].Signature)
	assert.Equal(t, `"ts-1"`, string(replayed.Content[2].Meta["thought_signature"]))

	// Turn 2's tool_result must fold the patch reminder directly into the tool_result text.
	var toolMsg WireMessage
	require.NoError(t, json.Unmarshal(requests[1].Messages[2], &toolMsg))
	require.Len(t, toolMsg.Content, 1)
	assert.Equal(t, "tool_result", toolMsg.Content[0].Type)
	assert.Contains(t, toolMsg.Content[0].Text, "# Figaro")
	assert.Contains(t, toolMsg.Content[0].Text, `<system-reminder name="note">`)
}

func TestDrainSSEReturnsStreamCutWhenTruncatedBeforeMessageEnd(t *testing.T) {
	stream := "data: {\"type\":\"prose_delta\",\"text\":\"half\"}\n\n"
	_, err := drainSSE(context.Background(), strings.NewReader(stream), &captureBus{})
	var cut *provider.StreamCut
	require.ErrorAs(t, err, &cut)
	assert.Equal(t, "message_end", cut.Awaited)
}

func TestRefusalOfClassifiesImageErrors(t *testing.T) {
	base := errors.New("bad request")
	noImg := refusalOf(http.StatusBadRequest, []byte(`{"error":{"code":"unsupported_capability","message":"request carries an image"}}`), base)
	var allRefused *provider.ImagesRefused
	require.ErrorAs(t, noImg, &allRefused)

	tooBig := refusalOf(http.StatusBadRequest, []byte(`{"error":{"message":"image exceeds 5.0 MB maximum: 6000000 bytes > 5242880 bytes"}}`), base)
	var oneRefused *provider.ImageRefused
	require.ErrorAs(t, tooBig, &oneRefused)
	assert.Equal(t, 5242880, oneRefused.MaxBase64)
}
