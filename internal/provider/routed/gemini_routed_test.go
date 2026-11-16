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
	"github.com/jack-work/figaro/internal/provider/gemini"
	"github.com/jack-work/figaro/internal/store"
)

func TestDeclaredCodeAssistProviderWithXWALAndEffortRouting(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []gemini.CodeAssistRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:fetchAvailableModels":
			io.WriteString(w, `{"models":{
				"gemini-4-carbon-low":{"displayName":"Gemini 4 Carbon (low)","supportsImages":true,"thinkingLevel":1,"maxTokens":65536,"maxInputTokens":1048576},
				"gemini-4-carbon-high":{"displayName":"Gemini 4 Carbon (high)","supportsImages":true,"thinkingLevel":3,"maxTokens":65536,"maxInputTokens":1048576}
			}}`)
		case "/v1internal:streamGenerateContent":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var req gemini.CodeAssistRequest
			require.NoError(t, json.Unmarshal(body, &req))
			mu.Lock()
			seen = append(seen, req)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ack\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":30,\"cachedContentTokenCount\":20,\"candidatesTokenCount\":3,\"totalTokenCount\":33}}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tomlBody := `api = "code-assist"
base_url = "` + srv.URL + `"
send_figaro_id = true

[code_assist]
project      = "aicode-consumers"
user_agent   = "antigravity"
request_type = "agent"

[effort]
keep_cache              = true
round                   = "nearest"
default_rung            = "high"
map_model               = "^(?P<model>.+)-(?P<effort>low|medium|high|xhigh)$"
outbound_model_template = "{{.Model}}-{{.Effort}}"

[effort.ladder]
low    = 25
medium = 50
high   = 75
`
	loaded := declaredDir(t, map[string]string{"gemca": tomlBody})
	require.Contains(t, RegisterDeclared(loaded), "gemca")

	be, ariaID := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("hello")},
	}})
	require.NoError(t, err)

	built, err := provider.Lookup("gemca").Build(provider.BuildContext{
		Loaded:         loaded,
		Resolver:       staticToken("ya29.tok"),
		OpenTranslator: func(a, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(a, ns) },
	})
	require.NoError(t, err)

	dp, ok := built.(*DeclaredProvider)
	require.True(t, ok)
	assert.Equal(t, "gemca-code-assist", dp.TranslatorChannel(), "static code-assist provider must enable on-append XWAL translation")
	assert.True(t, dp.KeepsCacheOnChange("system.thinking_effort", form.Snapshot{}))

	models, err := dp.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "gemini-4-carbon", models[0].ID)

	snap := form.FromMap(map[string]json.RawMessage{
		"system.model":           json.RawMessage(`"gemini-4-carbon"`),
		"system.thinking_effort": json.RawMessage(`80`),
	})
	require.NoError(t, dp.Send(context.Background(), provider.SendInput{
		AriaID:    ariaID,
		FigLog:    figLog,
		Snapshot:  snap,
		MaxTokens: 4096,
	}, &nullBus{}))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 1)
	assert.Equal(t, "gemini-4-carbon-high", seen[0].Model)
	assert.Equal(t, "aicode-consumers", seen[0].Project)
	assert.Equal(t, "antigravity", seen[0].UserAgent)
	assert.Equal(t, "agent", seen[0].RequestType)
}
