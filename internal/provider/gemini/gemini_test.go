package gemini

import (
	"context"
	"encoding/json"
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
	deltas []message.Content
	starts []string
	ready  []message.Content
	final  *message.Message
	caches []provider.AssistantCache
}

func (b *captureBus) PushDelta(c message.Content)         { b.deltas = append(b.deltas, c) }
func (b *captureBus) PushToolInvokeStart(id, name string) { b.starts = append(b.starts, id+":"+name) }
func (b *captureBus) PushToolInvokeDelta(string, string)  {}
func (b *captureBus) PushToolReady(c message.Content)     { b.ready = append(b.ready, c) }
func (b *captureBus) PushMessageEnd(string)               {}
func (b *captureBus) PushFigaro(m message.Message, c ...provider.AssistantCache) {
	cp := m
	b.final = &cp
	b.caches = append(b.caches, c...)
}

func TestCodeAssistXWALRoundTripPreservesCacheSignaturesAndReminders(t *testing.T) {
	var pIface any = &Provider{}
	_, isEnc := pIface.(provider.EntryEncoder)
	require.True(t, isEnc, "gemini provider must implement EntryEncoder for XWAL translations-v2")

	var (
		mu        sync.Mutex
		envelopes []CodeAssistRequest
		inners    []GenerateRequest
		headers   []http.Header
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ya29.fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1internal:fetchAvailableModels":
			require.Equal(t, http.MethodPost, r.Method)
			io.WriteString(w, `{"models":{
				"gemini-4-carbon":{"displayName":"Gemini 4 Carbon","supportsImages":true,"thinkingLevel":2,"maxTokens":65536,"maxInputTokens":1048576},
				"old-disabled":{"displayName":"Old"}
			}}`)
		case "/v1internal:streamGenerateContent":
			require.Equal(t, "sse", r.URL.Query().Get("alt"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var env CodeAssistRequest
			require.NoError(t, json.Unmarshal(body, &env))
			var inner GenerateRequest
			require.NoError(t, json.Unmarshal(env.Request, &inner))

			mu.Lock()
			envelopes = append(envelopes, env)
			inners = append(inners, inner)
			headers = append(headers, r.Header.Clone())
			turn := len(envelopes)
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			if turn == 1 {
				chunks := []string{
					`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"checking repo"}]}}]}}`,
					`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Reading file."},{"functionCall":{"id":"fc_1","name":"read","args":{"path":"go.mod"}},"thoughtSignature":"sig-fc-1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":800,"candidatesTokenCount":25,"thoughtsTokenCount":15,"totalTokenCount":1040}}}`,
				}
				io.WriteString(w, strings.Join(chunks, "\n\n")+"\n\n")
				return
			}
			chunks := []string{
				`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Module is figaro."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1100,"cachedContentTokenCount":1020,"candidatesTokenCount":10,"totalTokenCount":1110}}}`,
			}
			io.WriteString(w, strings.Join(chunks, "\n\n")+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	initMap := map[string]json.RawMessage{
		"mantra":       json.RawMessage(`"native gemini"`),
		"system.credo": json.RawMessage(`"You are Figaro."`),
	}
	initPatch := form.Build(form.Snapshot{}, initMap, nil)
	be, ariaID := store.NewTestAria(t, "d", initPatch)
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)

	cred := &rotatingCred{tokens: []string{"ya29.stale", "ya29.fresh"}}
	route := provider.Route{
		Name:    "gemini",
		BaseURL: srv.URL,
		Dialect: provider.DialectCodeAssist,
		Auth:    provider.AuthBearer,
		Caps:    provider.CacheCaps{SessionKey: true},
		Headers: map[string]string{"User-Agent": "custom-ua/1.0"},
	}
	p, err := New(provider.Knobs{Model: "gemini-4-carbon", MaxTokens: 32768}, cred, route, func(a string) (arialog.Log[[]json.RawMessage], error) {
		return be.OpenTranslator(a, "gemini-code-assist")
	})
	require.NoError(t, err)
	p.CodeAssist = CodeAssistConfig{Project: "proj-1", UserAgent: "client-1", RequestType: "agent"}
	p.HTTPClient = srv.Client()

	models, err := p.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "gemini-4-carbon", models[0].ID)
	assert.Equal(t, []string{"ya29.stale"}, cred.invalidated)

	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Patches: []message.Patch{initPatch},
		Content: []message.Content{message.TextContent("read go.mod")},
	}})
	require.NoError(t, err)

	snap := form.FromMap(initMap)
	schemaWithDefs := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs":   map[string]any{"Path": map[string]any{"type": []any{"string", "null"}}},
		"type":    "object",
		"properties": map[string]any{
			"path": map[string]any{"$ref": "#/$defs/Path"},
		},
		"required": []any{"path"},
	}

	bus1 := &captureBus{}
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snap,
		MaxTokens: 4096,
		Tools:     []provider.Tool{{Name: "read", Description: "Read file", Parameters: schemaWithDefs}},
	}, bus1))

	require.NotNil(t, bus1.final)
	require.Len(t, bus1.caches, 1, "must hand native AssistantCache row to XWAL")
	assert.Equal(t, "gemini-code-assist", bus1.caches[0].Namespace)
	require.NotNil(t, bus1.final.Usage)
	assert.Equal(t, 200, bus1.final.Usage.InputTokens)
	assert.Equal(t, 800, bus1.final.Usage.CacheReadTokens)
	assert.Equal(t, 40, bus1.final.Usage.OutputTokens)

	// Persist assistant + tool_result with patch into XWAL (mirroring Agent).
	asstEntry, err := figLog.Append(arialog.Entry[message.Message]{Payload: *bus1.final})
	require.NoError(t, err)
	trLog, err := be.OpenTranslator(ariaID, "gemini-code-assist")
	require.NoError(t, err)
	_, err = trLog.Append(arialog.Entry[[]json.RawMessage]{
		FigaroLT:    asstEntry.LT,
		Fingerprint: bus1.caches[0].Fingerprint,
		Payload:     bus1.caches[0].Payload,
	})
	require.NoError(t, err)

	notePatch := form.Build(snap, map[string]json.RawMessage{"status": json.RawMessage(`"read"`)} , nil)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleToolResult,
		Patches: []message.Patch{notePatch},
		Content: []message.Content{message.ToolResultContent("fc_1", "", "module github.com/jack-work/figaro", false)},
	}})
	require.NoError(t, err)

	bus2 := &captureBus{}
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snap.Apply(notePatch),
		MaxTokens: 4096,
	}, bus2))
	require.NotNil(t, bus2.final)
	assert.Equal(t, "Module is figaro.", bus2.final.Content[0].Text)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, envelopes, 2)
	assert.Equal(t, "proj-1", envelopes[0].Project)
	assert.Equal(t, "client-1", envelopes[0].UserAgent)
	assert.Equal(t, "agent", envelopes[0].RequestType)
	assert.Equal(t, "custom-ua/1.0", headers[0].Get("User-Agent"))
	assert.NotEmpty(t, headers[0].Get("x-session-id"))

	// Prefix stability across turns.
	assert.Equal(t, string(inners[0].Contents[0]), string(inners[1].Contents[0]))

	// Schema sanitization removed $schema and $defs/$ref.
	rawTools, _ := json.Marshal(inners[0].Tools)
	assert.NotContains(t, string(rawTools), "$schema")
	assert.NotContains(t, string(rawTools), "$ref")
	assert.Contains(t, string(rawTools), `"nullable":true`)

	// Replayed model turn retains thoughtSignature and omits raw thinking text.
	var modelTurn Content
	require.NoError(t, json.Unmarshal(inners[1].Contents[1], &modelTurn))
	assert.Equal(t, "model", modelTurn.Role)
	require.Len(t, modelTurn.Parts, 2)
	assert.Equal(t, "Reading file.", modelTurn.Parts[0].Text)
	assert.Equal(t, "sig-fc-1", modelTurn.Parts[1].ThoughtSignature)

	// Tool result turn backfilled missing functionResponse.name="read" and folded reminder into output.
	var toolTurn Content
	require.NoError(t, json.Unmarshal(inners[1].Contents[2], &toolTurn))
	assert.Equal(t, "user", toolTurn.Role)
	require.Len(t, toolTurn.Parts, 1)
	assert.Equal(t, "read", toolTurn.Parts[0].FunctionResponse.Name)
	assert.Contains(t, toolTurn.Parts[0].FunctionResponse.Response["output"], `<system-reminder name="status">`)
}

func TestGenerativeLanguageDialectUsesGoogleKeyAndPaginatesModels(t *testing.T) {
	var seenAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAPIKey = r.Header.Get("x-goog-api-key")
		switch r.URL.Path {
		case "/v1beta/models":
			if r.URL.Query().Get("pageToken") == "p2" {
				io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent"]}]}`)
				return
			}
			io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","inputTokenLimit":1048576,"outputTokenLimit":65536,"thinking":true,"supportedGenerationMethods":["generateContent"]},{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"p2"}`)
		case "/v1beta/models/gemini-2.5-pro:streamGenerateContent":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"direct ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":20,\"candidatesTokenCount\":4,\"totalTokenCount\":24}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	be, ariaID := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("ping")},
	}})
	require.NoError(t, err)

	route := provider.Route{
		Name:    "gemini",
		BaseURL: srv.URL + "/v1beta",
		Dialect: provider.DialectGemini,
		Auth:    provider.AuthGoogleKey,
	}
	p, err := New(provider.Knobs{Model: "gemini-2.5-pro", MaxTokens: 2048}, &rotatingCred{tokens: []string{"AIza-test-key"}}, route, func(a string) (arialog.Log[[]json.RawMessage], error) {
		return be.OpenTranslator(a, "gemini-direct")
	})
	require.NoError(t, err)
	p.HTTPClient = srv.Client()

	models, err := p.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 2, "embedContent-only model must be filtered and both pages collected")
	assert.Equal(t, "AIza-test-key", seenAPIKey)
	assert.Equal(t, "gemini-2.5-pro", models[0].ID)
	assert.Equal(t, "gemini-2.5-flash", models[1].ID)

	bus := &captureBus{}
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  form.FromMap(map[string]json.RawMessage{"system.thinking_budget": json.RawMessage(`4096`)}),
		MaxTokens: 1024,
	}, bus))
	require.NotNil(t, bus.final)
	assert.Equal(t, "direct ok", bus.final.Content[0].Text)
}

func TestPairingClosesDanglingCallsAndCoalescesAdjacentUsers(t *testing.T) {
	r1, _ := json.Marshal(Content{Role: "model", Parts: []Part{{FunctionCall: &FuncCall{ID: "c1", Name: "bash", Args: map[string]any{"cmd": "ls"}}}}})
	r2, _ := json.Marshal(Content{Role: "user", Parts: []Part{{Text: "actually do pwd"}}})
	r3, _ := json.Marshal(Content{Role: "user", Parts: []Part{{Text: "now"}}})
	seq := provider.SliceRows([]json.RawMessage{r1, r2, r3}, []uint64{1, 2, 3})

	paired, _ := pairAndCollectSystem(seq, "")
	rows, _ := provider.CollectRows(paired)
	// Leading model gets synthetic "." user turn, then model(c1 with skip_thought_signature_validator), then coalesced user(synthetic error for c1 + "actually do pwd" + "now").
	require.Len(t, rows, 3)

	var mTurn Content
	require.NoError(t, json.Unmarshal(rows[1], &mTurn))
	assert.Equal(t, skipThoughtSigValidator, mTurn.Parts[0].ThoughtSignature)

	var uTurn Content
	require.NoError(t, json.Unmarshal(rows[2], &uTurn))
	require.Len(t, uTurn.Parts, 3)
	require.NotNil(t, uTurn.Parts[0].FunctionResponse)
	assert.Equal(t, "c1", uTurn.Parts[0].FunctionResponse.ID)
	assert.Equal(t, "actually do pwd", uTurn.Parts[1].Text)
	assert.Equal(t, "now", uTurn.Parts[2].Text)
}

func TestDrainSSEReturnsStreamCutWhenTruncated(t *testing.T) {
	stream := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"half\"}]}}]}\n\n"
	_, err := drainSSE(context.Background(), strings.NewReader(stream), &captureBus{})
	var cut *provider.StreamCut
	require.ErrorAs(t, err, &cut)
	assert.Equal(t, "finishReason", cut.Awaited)
}
