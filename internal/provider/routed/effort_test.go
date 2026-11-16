package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestTransportForEndpointsPrefersGeminiAndCodeAssist(t *testing.T) {
	assert.Equal(t, Gemini, transportForEndpoints([]string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/models/m:streamGenerateContent"}))
	assert.Equal(t, CodeAssist, transportForEndpoints([]string{"/models/m:streamGenerateContent", "/v1internal:streamGenerateContent"}))
}

func TestDeclaredEffortRouting(t *testing.T) {
	type chatReq struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	var (
		mu       sync.Mutex
		outbound []chatReq
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[
				{"id":"gemini-3-pro-low","name":"Gemini 3 Pro (low)","context_window":1048576},
				{"id":"gemini-3-pro-high","name":"Gemini 3 Pro (high)","context_window":1048576},
				{"id":"gemini-2.5-flash","name":"Gemini 2.5 Flash","context_window":1048576}
			]}`)
		case "/v1/chat/completions":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var req chatReq
			require.NoError(t, json.Unmarshal(body, &req))
			mu.Lock()
			outbound = append(outbound, req)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tomlBody := `api = "chat"
base_url = "` + srv.URL + `/v1"

[effort]
keep_cache = true
round = "nearest"
default_rung = "high"
map_model = "^(?P<model>.+)-(?P<effort>low|medium|high|xhigh)$"
outbound_model_template = "{{.Model}}-{{.Effort}}"

[effort.ladder]
low = 25
medium = 50
high = 75
`
	loaded := declaredDir(t, map[string]string{"gemgw": tomlBody})
	got := RegisterDeclared(loaded)
	require.Contains(t, got, "gemgw")

	be, ariaID := store.NewTestAria(t, "d", message.Patch{})
	built, err := provider.Lookup("gemgw").Build(provider.BuildContext{
		Loaded:         loaded,
		Resolver:       staticToken("tok"),
		OpenTranslator: be.OpenTranslator,
	})
	require.NoError(t, err)
	dp, ok := built.(*DeclaredProvider)
	require.True(t, ok)

	assert.True(t, dp.KeepsCacheOnChange("system.thinking_effort", form.Snapshot{}))
	assert.True(t, dp.KeepsCacheOnChange("system.thinking_budget", form.Snapshot{}))

	models, err := dp.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 2, "effort variants must collapse into one model entry")
	assert.Equal(t, "gemini-3-pro", models[0].ID)
	assert.Equal(t, "Gemini 3 Pro", models[0].Entry.Name)
	require.NotNil(t, models[0].Entry.Reasoning)
	assert.True(t, *models[0].Entry.Reasoning)
	assert.Equal(t, "gemini-2.5-flash", models[1].ID)

	// Numeric score 60 on gemini-3-pro (observed rungs: low=25, high=75) rounds nearest -> high -> outbound gemini-3-pro-high.
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("hello")},
	}})
	require.NoError(t, err)

	snap := form.FromMap(map[string]json.RawMessage{
		"system.model":           json.RawMessage(`"gemini-3-pro"`),
		"system.thinking_effort": json.RawMessage(`60`),
	})
	require.NoError(t, dp.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snap,
		MaxTokens: 512,
	}, &nullBus{}))

	// Non-matching model (gemini-2.5-flash) must NOT be rewritten by outbound_model_template.
	snapFlash := form.FromMap(map[string]json.RawMessage{
		"system.model": json.RawMessage(`"gemini-2.5-flash"`),
	})
	require.NoError(t, dp.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snapFlash,
		MaxTokens: 512,
	}, &nullBus{}))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, outbound, 2)
	assert.Equal(t, "gemini-3-pro-high", outbound[0].Model)
	assert.Equal(t, "gemini-2.5-flash", outbound[1].Model)
}

func TestPickRungRoundingModes(t *testing.T) {
	cands := []scoredRung{
		{name: "low", score: 25},
		{name: "medium", score: 50},
		{name: "high", score: 75},
	}
	assert.Equal(t, "medium", pickRung(40, cands, RoundNearest))
	assert.Equal(t, "high", pickRung(62.5, cands, RoundNearest), "ties break upward")
	assert.Equal(t, "low", pickRung(49, cands, RoundDown))
	assert.Equal(t, "high", pickRung(51, cands, RoundUp))
}
