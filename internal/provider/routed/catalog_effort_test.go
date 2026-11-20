package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

type codeAssistFixture struct {
	srv  *httptest.Server
	mu   sync.Mutex
	sent []string
}

func (f *codeAssistFixture) models() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func newRecordedCodeAssist(t *testing.T) *codeAssistFixture {
	t.Helper()
	catalog, err := os.ReadFile("../gemini/testdata/code-assist-models-2026-11-20.json")
	require.NoError(t, err)
	f := &codeAssistFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:fetchAvailableModels":
			w.Write(catalog)
		case "/v1internal:streamGenerateContent":
			var req gemini.CodeAssistRequest
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			f.mu.Lock()
			f.sent = append(f.sent, req.Model)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ack\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":1,\"totalTokenCount\":4}}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func buildRecordedCodeAssist(t *testing.T, name, effort string) (provider.Provider, *codeAssistFixture) {
	t.Helper()
	f := newRecordedCodeAssist(t)
	body := "api = \"code-assist\"\nbase_url = \"" + f.srv.URL + "\"\n\n[code_assist]\nproject = \"p\"\n" + effort
	loaded := declaredDir(t, map[string]string{name: body})
	require.Contains(t, RegisterDeclared(loaded), name)
	built, err := provider.Lookup(name).Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("ya29.tok")})
	require.NoError(t, err)
	return built, f
}

func sendModel(t *testing.T, p provider.Provider, model string, effort string) {
	t.Helper()
	be, ariaID := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("hello")},
	}})
	require.NoError(t, err)
	vals := map[string]json.RawMessage{"system.model": mustJSONString(model)}
	if effort != "" {
		vals["system.thinking_effort"] = json.RawMessage(effort)
	}
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: ariaID, FigLog: figLog, Snapshot: form.FromMap(vals), MaxTokens: 1024,
	}, &nullBus{}))
}

func TestCodeAssistEffortTiersComeFromTheCatalogWithoutAMapModel(t *testing.T) {
	p, _ := buildRecordedCodeAssist(t, "gca-tiers", "[effort]\ndefault_rung = \"high\"\n")
	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	byID := map[string]provider.ModelInfo{}
	for _, m := range listed {
		byID[m.ID] = m
	}
	for _, member := range []string{"gemini-4-neon-high", "claude-opus-5-5-low", "gemini-4-hydrogen-tiered-xhigh", "gemini-3.8-flash-medium"} {
		_, listed := byID[member]
		assert.False(t, listed, "%s folds into its base", member)
	}
	want := map[string][]string{
		"gemini-4-neon":            {"low", "medium", "high", "xhigh"},
		"gemini-4-hydrogen":        {"low", "medium", "high", "xhigh"},
		"gemini-4-hydrogen-tiered": {"high", "xhigh"},
		"gemini-3.8-flash":         {"low", "medium", "high"},
		"claude-opus-5-5":          {"low", "medium", "high"},
		"claude-sonnet-5-5":        {"low", "medium", "high"},
	}
	for id, efforts := range want {
		_, listed := byID[id]
		require.True(t, listed, id)
		assert.Equal(t, efforts, byID[id].Efforts, id)
		require.NotNil(t, byID[id].Reasoning, id)
		assert.True(t, *byID[id].Reasoning, id)
	}
	assert.Equal(t, "Gemini 4 Neon", byID["gemini-4-neon"].Name)
	assert.Equal(t, "Claude Opus 5.5", byID["claude-opus-5-5"].Name)
	assert.Empty(t, byID["gemini-2.5-pro"].Efforts)
}

func TestCodeAssistNamesNoLongerShareOneWrongDisplayName(t *testing.T) {
	p, _ := buildRecordedCodeAssist(t, "gca-names", "[effort]\ndefault_rung = \"high\"\n")
	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	names := map[string]string{}
	for _, m := range listed {
		names[m.ID] = m.Name
	}
	assert.Equal(t, "Gemini 2.5 Flash", names["gemini-2.5-flash"])
	assert.Equal(t, "Gemini 2.5 Flash Lite", names["gemini-2.5-flash-lite"])
	assert.Equal(t, "Gemini 2.5 Flash Thinking", names["gemini-2.5-flash-thinking"])
	assert.Equal(t, "Gemini 3.5 Flash Lite", names["gemini-3.5-flash-lite"])
	assert.Equal(t, "Gemini 2.5 Pro", names["gemini-2.5-pro"])
	assert.NotEqual(t, names["gemini-4-hydrogen"], names["gemini-4-hydrogen-tiered"])
}

func TestCodeAssistSendPicksTheCatalogRungOutbound(t *testing.T) {
	p, f := buildRecordedCodeAssist(t, "gca-send", "[effort]\ndefault_rung = \"high\"\n")
	sendModel(t, p, "gemini-4-neon", `95`)
	sendModel(t, p, "gemini-4-neon", `"low"`)
	sendModel(t, p, "claude-opus-5-5", `"xhigh"`)
	sendModel(t, p, "gemini-4-hydrogen-tiered", `20`)
	sendModel(t, p, "gemini-4-argon-low", "")
	sendModel(t, p, "gemini-4-argon-low", `"medium"`)
	sendModel(t, p, "gemini-2.5-pro", `"high"`)
	assert.Equal(t, []string{
		"gemini-4-neon-xhigh",
		"gemini-4-neon-low",
		"claude-opus-5-5-high",
		"gemini-4-hydrogen-tiered-high",
		"gemini-4-argon-low",
		"gemini-4-argon-medium",
		"gemini-2.5-pro",
	}, f.models())
}

func TestCodeAssistWithoutAnEffortSectionStillFoldsTiers(t *testing.T) {
	p, f := buildRecordedCodeAssist(t, "gca-bare", "")
	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, m := range listed {
		ids[m.ID] = true
	}
	assert.True(t, ids["gemini-4-neon"])
	assert.False(t, ids["gemini-4-neon-high"])
	sendModel(t, p, "gemini-4-neon", `"medium"`)
	sendModel(t, p, "gemini-4-carbon-low", "")
	assert.Equal(t, []string{"gemini-4-neon-medium", "gemini-4-carbon-low"}, f.models())
}

func TestMapModelStillOverridesTheCatalog(t *testing.T) {
	p, f := buildRecordedCodeAssist(t, "gca-override", `[effort]
map_model      = "^(?P<model>gemini-3\\.8-flash)-(?P<effort>tiered)$"
model_template = "{{ .Model }}-{{ .Effort }}"
`)
	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	byID := map[string]provider.ModelInfo{}
	for _, m := range listed {
		byID[m.ID] = m
	}
	_, tieredListed := byID["gemini-3.8-flash-tiered"]
	assert.False(t, tieredListed)
	assert.Contains(t, byID["gemini-3.8-flash"].Efforts, "tiered")
	sendModel(t, p, "gemini-3.8-flash", `"tiered"`)
	sendModel(t, p, "gemini-4-neon", `"xhigh"`)
	assert.Equal(t, []string{"gemini-3.8-flash-tiered", "gemini-4-neon-xhigh"}, f.models())
}
