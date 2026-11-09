package routed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/catalogmaps"
)

type catalogGolden struct {
	Models []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Context   int    `json:"context"`
		Long      int    `json:"long"`
		MaxPrompt int    `json:"max_prompt"`
		MaxOutput int    `json:"max_output"`
		Hidden    bool   `json:"hidden"`
	} `json:"models"`
	Routes   map[string]string `json:"routes"`
	TextOnly []string          `json:"text_only"`
}

// testdata/copilot-models.json is ten models trimmed from Copilot's live
// catalog on 2026-11-08 plus one billing-shaped entry; the golden file is
// what main e00be639 (0.57.4) made of it, plus each model's prompt cap.
// Copilot's built-in mapping must give the same windows, outputs, picker
// flags, routes and image decisions. 0.58.0 measured the context against the
// prompt cap instead (claude-haiku-4.5: 136k, not 200k); a gen6 take sent
// ~150k-token requests to Copilot that all succeeded, so the cap is listed
// and the window is the context again.
func TestCopilotsMappingMatchesTheGolden(t *testing.T) {
	body, err := os.ReadFile("testdata/copilot-models.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	p := mustNew(t, Spec{Name: "copilot", BaseURL: func() string { return srv.URL }, Catalog: "/models",
		Mapping: catalogmaps.Builtin("copilot"), Rules: []Rule{{Model: "claude-*", Transport: Messages}}})
	listed, err := p.Models(context.Background())
	require.NoError(t, err)

	var got catalogGolden
	got.Routes = map[string]string{}
	for _, m := range listed {
		row := struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Context   int    `json:"context"`
			Long      int    `json:"long"`
			MaxPrompt int    `json:"max_prompt"`
			MaxOutput int    `json:"max_output"`
			Hidden    bool   `json:"hidden"`
		}{m.ID, m.Name, m.Window(""), m.Window(models.LongContextTier), m.MaxPrompt, m.MaxOutput, m.Hidden}
		got.Models = append(got.Models, row)
	}
	sort.Slice(got.Models, func(i, j int) bool { return got.Models[i].ID < got.Models[j].ID })
	var raw struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &raw))
	for _, d := range raw.Data {
		got.Routes[d.ID] = string(p.cachedRoute(d.ID))
		if p.textOnly(d.ID) != "" {
			got.TextOnly = append(got.TextOnly, d.ID)
		}
	}
	sort.Strings(got.TextOnly)

	var want catalogGolden
	golden, err := os.ReadFile("testdata/copilot-models.golden.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(golden, &want))
	require.Equal(t, want, got)
}

// The stage manager's gen6 take on copilot claude-haiku-4.5 drew
// '150.5k/136.0k 110.7%' while every ~150k-token request succeeded: the
// footer measured against the prompt cap. The context is the window; the cap
// is listed beside it and enforced nowhere.
func TestCopilotsContextIsTheWindowAndThePromptCapIsListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","supported_endpoints":["/chat/completions","/v1/messages"],
			"capabilities":{"limits":{"max_context_window_tokens":200000,"max_output_tokens":64000,"max_prompt_tokens":136000}}}]}`))
	}))
	defer srv.Close()
	p := mustNew(t, Spec{Name: "copilot", BaseURL: func() string { return srv.URL }, Catalog: "/models",
		Mapping: catalogmaps.Builtin("copilot"), Rules: []Rule{{Model: "claude-*", Transport: Messages}}})
	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, 200_000, listed[0].Window(""))
	require.Equal(t, 200_000, listed[0].Window(models.LongContextTier))
	require.Equal(t, 136_000, listed[0].MaxPrompt)
	require.Equal(t, models.SourceAPI, listed[0].Source("max_prompt"))
}
