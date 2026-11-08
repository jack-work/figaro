package routed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
)

// testdata/rou2-models.json is seven entries of rou2's live /models
// (2026-11-08, pricing and data_policy stripped): explabs' own shape,
// Anthropic's shape under anthropic/, and bare OpenAI rows.
func rou2Gateway(t *testing.T, name, file string) *Provider {
	t.Helper()
	body, err := os.ReadFile("testdata/rou2-models.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return gatewayAt(t, name, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n"+file)
}

func listedByID(t *testing.T, p *Provider) map[string]provider.ModelInfo {
	t.Helper()
	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	out := map[string]provider.ModelInfo{}
	for _, m := range listed {
		out[m.ID] = m
	}
	return out
}

// A gateway preset with no [catalog] still reads windows: the built-in
// generic mapping knows the OpenAI-, Anthropic- and OpenRouter-shaped names.
// rou2 needs no catalog config at all.
func TestAGatewayWithNoCatalogTableReadsRou2sWindows(t *testing.T) {
	got := listedByID(t, rou2Gateway(t, "rou2gen", ""))
	for id, want := range map[string][2]int{
		"explabs/glm-5.3":            {1_048_576, 131_072},
		"explabs/qwen3.8-max":        {1_000_000, 131_072},
		"explabs/deepseek-v4-flash":  {1_048_576, 384_000},
		"anthropic/claude-haiku-5-5": {1_000_000, 128_000},
		"anthropic/claude-opus-5-5":  {1_000_000, 128_000},
	} {
		m, ok := got[id]
		require.True(t, ok, id)
		require.Equal(t, want[0], m.Window(""), id)
		require.Equal(t, want[1], m.MaxOutput, id)
		require.Equal(t, models.SourceAPI, m.Source("context"), id)
	}
	require.Equal(t, "GLM 5.3", got["explabs/glm-5.3"].Name)
	require.Equal(t, "Claude Haiku 5.5", got["anthropic/claude-haiku-5-5"].Name)
	takes, src := got["anthropic/claude-haiku-5-5"].TakesImages()
	require.True(t, takes)
	require.Equal(t, models.SourceAPI, src)
	_, src = got["explabs/glm-5.3"].TakesImages()
	require.Empty(t, src, "explabs says nothing of image input")
	require.Equal(t, true, *got["explabs/glm-5.3"].Tools)
	require.Equal(t, true, *got["explabs/glm-5.3"].Reasoning)

	luna, ok := got["openai/gpt-6-luna"]
	require.True(t, ok, "a bare row is still listed")
	require.Zero(t, luna.Window(""), "rou2 states no window for openai/*")
}

// The provider file's [models."<id>"] wins over the catalog, and holds for a
// model the catalog never lists.
func TestPerModelConfigWinsAndSeedsAnUnlistedModel(t *testing.T) {
	p := rou2Gateway(t, "rou2cfg", `
[models."explabs/glm-5.3"]
text_only  = true
max_output = 65536

[models."openai/gpt-6-luna"]
context = 400000

[models."private/unlisted"]
context    = 32000
max_output = 4000
`)
	got := listedByID(t, p)
	glm := got["explabs/glm-5.3"]
	require.Equal(t, 1_048_576, glm.Window(""))
	require.Equal(t, 65_536, glm.MaxOutput)
	require.Equal(t, models.SourceConfig, glm.Source("max_output"))
	takes, src := glm.TakesImages()
	require.False(t, takes)
	require.Equal(t, models.SourceConfig, src)

	luna := got["openai/gpt-6-luna"]
	require.Equal(t, 400_000, luna.Window(""))
	require.Equal(t, models.SourceConfig, luna.Source("context"))

	e, ok := p.SeedModel("private/unlisted")
	require.True(t, ok)
	require.Equal(t, 32_000, e.Window(""))
	require.Equal(t, 4_000, e.MaxOutput)
	_, ok = p.SeedModel("private/other")
	require.False(t, ok)
}

// [catalog] replaces the built-in mapping key by key: here only context and
// name, so max_output still reads the built-in paths.
func TestACatalogTableReplacesBuiltInKeysOneByOne(t *testing.T) {
	got := listedByID(t, rou2Gateway(t, "rou2map", `
[catalog]
context = ["top_provider.context_length"]
name    = []

[catalog.facts."gpt-6-luna"]
context = 1000000
`))
	glm := got["explabs/glm-5.3"]
	require.Equal(t, 1_048_576, glm.Window(""))
	require.Equal(t, 131_072, glm.MaxOutput, "max_output is still the built-in key")
	require.Empty(t, glm.Name, "name = [] reads no name")
	require.Zero(t, got["anthropic/claude-haiku-5-5"].Window(""), "max_input_tokens is no longer a context path")

	luna := got["openai/gpt-6-luna"]
	require.Equal(t, 1_000_000, luna.Window(""))
	require.Equal(t, models.SourceMapping, luna.Source("context"))
}

func TestABadCatalogTableFailsTheBuild(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"badmap": "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n[catalog]\nimages = [\"modalities contains:image\"]\n"})
	RegisterDeclared(loaded)
	_, err := provider.Lookup("badmap").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("")})
	require.ErrorContains(t, err, "images")
}

// OpenRouter's documented shape, read through openrouter.toml on the chat
// backend's listing (openrouter routes statically, with no catalog fetch).
func TestOpenRouterListsThroughItsMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"x-ai/grok-4.1","name":"xAI: Grok 4.1","context_length":2000000,
			 "architecture":{"input_modalities":["text","image"]},
			 "top_provider":{"context_length":2000000,"max_completion_tokens":30000},
			 "supported_parameters":["tools","reasoning"]},
			{"id":"z-ai/glm-5.3","context_length":1048576,"architecture":{"input_modalities":["text"]},"supported_parameters":[]}
		]}`))
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	p, err := provider.Lookup(openRouterName).Build(provider.BuildContext{Loaded: declaredDir(t, nil), Resolver: staticToken("k")})
	require.NoError(t, err)
	got := listedByID(t, p.(*Provider))

	grok := got["x-ai/grok-4.1"]
	require.Equal(t, "xAI: Grok 4.1", grok.Name)
	require.Equal(t, 2_000_000, grok.Window(""))
	require.Equal(t, 30_000, grok.MaxOutput)
	takes, _ := grok.TakesImages()
	require.True(t, takes)
	require.Equal(t, true, *grok.Tools)
	require.Equal(t, true, *grok.Reasoning)

	glm := got["z-ai/glm-5.3"]
	takes, src := glm.TakesImages()
	require.False(t, takes)
	require.Equal(t, models.SourceAPI, src)
	require.Equal(t, false, *glm.Tools)
}
