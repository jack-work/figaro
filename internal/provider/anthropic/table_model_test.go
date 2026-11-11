package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/catalog"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

var _ provider.ModelSeeder = (*Anthropic)(nil)

// The window table moved into catalogmaps/anthropic.toml as facts; it must
// answer as the table did, for aliases the models endpoint never lists.
func TestAnthropicSeedsFromItsFacts(t *testing.T) {
	a := &Anthropic{}
	cases := []struct {
		model string
		want  int
	}{
		{"claude-opus-5", 1_000_000},
		{"claude-opus-5-20260115", 1_000_000},
		{"claude-opus-5-5", 1_000_000},
		{"claude-opus-5.5", 1_000_000},
		{"claude-opus-4-8", 1_000_000},
		{"claude-opus-4-7", 1_000_000},
		{"claude-opus-4-6", 1_000_000},
		{"claude-sonnet-5", 1_000_000},
		{"claude-sonnet-4-6", 1_000_000},
		{"claude-sonnet-4-5-20250929", 1_000_000},
		{"claude-fable-5", 1_000_000},
		{"claude-opus-4.6", 1_000_000},
		{"anthropic/claude-opus-5", 1_000_000},
		{"Claude-Opus-5", 1_000_000},

		{"claude-opus-4-5-20251101", 200_000},
		{"claude-opus-4-1-20250805", 200_000},
		{"claude-opus-4-20250514", 200_000},
		{"claude-haiku-4-5-20251001", 200_000},
		{"claude-haiku-4.5", 200_000},
		{"claude-3-5-sonnet-20241022", 200_000},

		{"claude-sonnet-4-20250514", 0}, // delisted; we will not guess
		{"claude-opus-6", 0},
		{"gpt-5.6-terra", 0},
		{"", 0},
		{"claude", 0},
		{"claude-opus-42", 0}, // prefix must land on an id boundary
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			e, ok := a.SeedModel(tc.model)
			assert.Equal(t, tc.want > 0, ok)
			assert.Equal(t, tc.want, e.Window(""))
			if ok {
				assert.Equal(t, models.SourceMapping, e.Source("context"))
			}
		})
	}
}

// Anthropic's list is paged (has_more, last_id, after_id); 0.57 read the
// first hundred and stopped. A listed model's own numbers beat the facts, and
// a fact fills what the list leaves out.
func TestAnthropicListsEveryPageThroughItsMapping(t *testing.T) {
	var after []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		after = append(after, r.URL.Query().Get("after_id"))
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","max_input_tokens":900000,"max_tokens":128000,
				"capabilities":{"image_input":{"supported":true},"thinking":{"supported":true}}}],"has_more":true,"last_id":"claude-opus-5-5"}`)
		default:
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-5","display_name":"Claude Sonnet 5"}],"has_more":false,"last_id":"claude-sonnet-5"}`)
		}
	}))
	defer srv.Close()
	route := provider.AnthropicDirect()
	route.BaseURL = srv.URL + "/v1"
	a := &Anthropic{auth: &staticAuth{token: "t"}, HTTPClient: srv.Client(), Route: route}

	listed, err := a.Models(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"", "claude-opus-5-5"}, after)
	require.Len(t, listed, 2)
	opus, sonnet := listed[0], listed[1]
	require.Equal(t, "Claude Opus 5.5", opus.Name)
	require.Equal(t, 900_000, opus.Window(""), "the list beats the fact")
	require.Equal(t, models.SourceAPI, opus.Source("context"))
	require.Equal(t, 128_000, opus.MaxOutput)
	takes, src := opus.TakesImages()
	require.True(t, takes)
	require.Equal(t, models.SourceAPI, src)
	require.Equal(t, true, *opus.Reasoning)
	require.Equal(t, 1_000_000, sonnet.Window(""), "a fact fills what the list leaves out")
	require.Equal(t, models.SourceMapping, sonnet.Source("context"))
}

func TestTheAnthropicFileAdjustsTheCatalog(t *testing.T) {
	dir := t.TempDir()
	loaded := &config.Loaded{ConfigDir: dir}
	path := loaded.ProviderAuthPath("anthropic")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`
[catalog.facts."claude-opus-6*"]
context = 2000000

[models."claude-opus-5"]
context = 500000
`), 0o600))
	r, err := catalogFromFileForTest(loaded)
	require.NoError(t, err)
	a := &Anthropic{Catalog: r}
	e, _ := a.SeedModel("claude-opus-6")
	require.Equal(t, 2_000_000, e.Window(""))
	e, _ = a.SeedModel("claude-opus-5")
	require.Equal(t, 500_000, e.Window(""))
	require.Equal(t, models.SourceConfig, e.Source("context"))
	e, _ = a.SeedModel("claude-opus-4-6")
	require.Equal(t, 1_000_000, e.Window(""), "the built-in facts stay")

	r, err = catalogFromFileForTest(&config.Loaded{ConfigDir: t.TempDir()})
	require.NoError(t, err, "no file is the built-in mapping")
	_, ok := (&Anthropic{Catalog: r}).SeedModel("claude-opus-5")
	require.True(t, ok)
}

func catalogFromFileForTest(loaded *config.Loaded) (*catalog.Reader, error) {
	data, err := readProviderFile(loaded)
	if err != nil {
		return nil, err
	}
	return catalogFrom(data)
}
