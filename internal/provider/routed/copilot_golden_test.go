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
		MaxOutput int    `json:"max_output"`
		Hidden    bool   `json:"hidden"`
	} `json:"models"`
	Routes   map[string]string `json:"routes"`
	TextOnly []string          `json:"text_only"`
}

// testdata/copilot-models.json is ten models trimmed from Copilot's live
// catalog on 2026-11-08 plus one billing-shaped entry; the golden file is
// what main e00be639 (0.57.4) made of it. Copilot's built-in mapping must
// give the same windows, outputs, picker flags, routes and image decisions.
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
			MaxOutput int    `json:"max_output"`
			Hidden    bool   `json:"hidden"`
		}{m.ID, m.Name, m.Window(""), m.Window(models.LongContextTier), m.MaxOutput, m.Hidden}
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
