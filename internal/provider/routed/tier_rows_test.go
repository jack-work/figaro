package routed

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
	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func TestASeededTierIdIsHiddenSoItNeverListsAsItsOwnModel(t *testing.T) {
	p, _ := buildRecordedCodeAssist(t, "gca-seed", "")
	_, err := p.Models(context.Background())
	require.NoError(t, err)
	seeder := p.(provider.ModelSeeder)

	tier, ok := seeder.SeedModel("gemini-4-neon-low")
	require.True(t, ok, "an aria still on a tier id keeps its limits")
	assert.True(t, tier.Hidden)
	assert.Equal(t, []string{"low"}, tier.Efforts)

	base, ok := seeder.SeedModel("gemini-4-neon")
	require.True(t, ok)
	assert.False(t, base.Hidden)
}

func TestACodeAssistTierIdIsHiddenEvenBeforeTheFirstListing(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"gca-cold": "api = \"code-assist\"\nbase_url = \"http://127.0.0.1:1\"\n\n[code_assist]\nproject = \"p\"\n"})
	require.Contains(t, RegisterDeclared(loaded), "gca-cold")
	built, err := provider.Lookup("gca-cold").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	require.NoError(t, err)
	seeder := built.(provider.ModelSeeder)
	tier, ok := seeder.SeedModel("gemini-4-neon-high")
	require.True(t, ok)
	assert.True(t, tier.Hidden)
	base, ok := seeder.SeedModel("gemini-4-neon")
	require.True(t, ok)
	assert.False(t, base.Hidden)
}

func TestAGatewayFoldsPrefixedTiersFromProviderConfigAlone(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[
				{"id":"google/gemini-4-neon-low","name":"Gemini 4 Neon (Low)","context_length":1048576},
				{"id":"google/gemini-4-neon-medium","name":"Gemini 4 Neon (Medium)","context_length":1048576},
				{"id":"google/gemini-4-neon-high","name":"Gemini 4 Neon (High)","context_length":1048576},
				{"id":"explabs/glm-5.3","name":"GLM 5.3","context_length":1048576}
			]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			sent = append(sent, req.Model)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	loaded := declaredDir(t, map[string]string{"rou2tiers": `preset = "gateway"
api = "chat"
base_url = "` + srv.URL + `/v1"

[routing]
catalog = "/models"

[effort]
default_rung = "high"
map_model = "^(?P<model>google/gemini-[a-z0-9.-]+?)-(?P<effort>low|medium|high|xhigh)$"
model_template = "{{ .Model }}-{{ .Effort }}"

[effort.ladder]
low = 0
medium = 50
high = 75
xhigh = 100
`})
	require.Contains(t, RegisterDeclared(loaded), "rou2tiers")
	var p provider.Provider
	be, ariaID := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(ariaID)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("hello")},
	}})
	require.NoError(t, err)
	send := func(model, effort string) {
		vals := map[string]json.RawMessage{"system.model": mustJSONString(model)}
		if effort != "" {
			vals["system.thinking_effort"] = json.RawMessage(effort)
		}
		require.NoError(t, p.Send(context.Background(), provider.SendInput{
			AriaID: ariaID, FigLog: figLog, Snapshot: form.FromMap(vals), MaxTokens: 512,
		}, &nullBus{}))
	}
	p, err = provider.Lookup("rou2tiers").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t"), OpenTranslator: be.OpenTranslator})
	require.NoError(t, err)

	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	byID := map[string]provider.ModelInfo{}
	for _, m := range listed {
		byID[m.ID] = m
	}
	require.Len(t, byID, 2)
	assert.Equal(t, []string{"low", "medium", "high"}, byID["google/gemini-4-neon"].Efforts)
	assert.Equal(t, "Gemini 4 Neon", byID["google/gemini-4-neon"].Name)

	send("google/gemini-4-neon", `"low"`)
	send("google/gemini-4-neon", "")
	send("google/gemini-4-neon-medium", "")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"google/gemini-4-neon-low", "google/gemini-4-neon-high", "google/gemini-4-neon-medium"}, sent)
}

func TestTheCatalogFormKeepsNoVisibleTierRowForAWantedOrStaleTierId(t *testing.T) {
	p, _ := buildRecordedCodeAssist(t, "gca-form", "")
	c := modelcat.New(modelcat.Config{Build: func(string) (provider.Provider, error) { return p, nil }, Known: func(string) bool { return true }})
	c.Want("gca-form", "gemini-4-neon-high")
	require.NoError(t, c.Refresh(context.Background(), "gca-form"))
	c.Want("gca-form", "gemini-4-neon-high")
	provs, _ := apimodels.Providers(c.Snapshot())
	rows := provs["gca-form"].Flat()
	require.Contains(t, rows, "gemini-4-neon")
	assert.False(t, rows["gemini-4-neon"].Hidden)
	assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, rows["gemini-4-neon"].Efforts)
	for id, e := range rows {
		if id != "gemini-4-neon" && strings.HasPrefix(id, "gemini-4-neon-") {
			assert.True(t, e.Hidden, id)
		}
	}
}
