package catalog

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
)

func read(t *testing.T, m Mapping, overrides map[string]Override, body string) map[string]models.Entry {
	t.Helper()
	r, err := New(m, overrides)
	require.NoError(t, err)
	listed, _, err := r.Page(strings.NewReader(body))
	require.NoError(t, err)
	out := map[string]models.Entry{}
	for _, l := range listed {
		out[l.ID] = l.Entry
	}
	return out
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// The first path holding a value of the field's type wins: rou2 serves two
// shapes of entry in one list, and a mapping names both.
func TestTheFirstPathOfTheRightTypeWins(t *testing.T) {
	m := Mapping{
		List:      "data",
		Name:      []string{"name", "display_name"},
		Context:   []string{"context_length", "max_input_tokens"},
		MaxOutput: []string{"max_output_tokens", "max_tokens"},
	}
	got := read(t, m, nil, `{"data":[
		{"id":"explabs/glm","name":"GLM","context_length":1048576,"max_output_tokens":131072,"max_tokens":"lots"},
		{"id":"anthropic/haiku","display_name":"Haiku","context_length":"n/a","max_input_tokens":1000000,"max_tokens":128000},
		{"id":"openai/bare","object":"model"},
		{"object":"model","name":"no id, no entry"}
	]}`)
	require.Len(t, got, 3)
	require.Equal(t, "GLM", got["explabs/glm"].Name)
	require.Equal(t, 1_048_576, got["explabs/glm"].Window(""))
	require.Equal(t, 131_072, got["explabs/glm"].MaxOutput)
	require.Equal(t, "Haiku", got["anthropic/haiku"].Name)
	require.Equal(t, 1_000_000, got["anthropic/haiku"].Window(""), "a string where a number belongs is skipped")
	require.Equal(t, 128_000, got["anthropic/haiku"].MaxOutput)
	require.Equal(t, models.Entry{}, got["openai/bare"], "nothing stated is nothing known")
}

func TestValueForms(t *testing.T) {
	m := Mapping{
		List:      "data",
		Images:    []string{"architecture.input_modalities has:image", "capabilities.supports.vision"},
		Tools:     []string{"supported_parameters has:tools"},
		Reasoning: []string{"capabilities.supports.reasoning_effort nonempty"},
		Visible:   []string{"model_picker_enabled"},
		Endpoints: []string{"supported_endpoints"},
	}
	got := read(t, m, nil, `{"data":[
		{"id":"a","architecture":{"input_modalities":["text","image"]},"supported_parameters":["tools"],
		 "capabilities":{"supports":{"reasoning_effort":["low","high"]}},"model_picker_enabled":false,
		 "supported_endpoints":["/responses","ws:/responses"]},
		{"id":"b","architecture":{"input_modalities":["text"]},"supported_parameters":[],
		 "capabilities":{"supports":{"reasoning_effort":[]}}},
		{"id":"c","capabilities":{"supports":{"vision":true}},"supported_endpoints":["/chat/completions",7]}
	]}`)

	a := got["a"]
	require.Equal(t, yes(), a.Images)
	require.Equal(t, yes(), a.Tools)
	require.Equal(t, yes(), a.Reasoning)
	require.True(t, a.Hidden)
	require.Equal(t, []string{"/responses", "ws:/responses"}, a.Endpoints)

	b := got["b"]
	require.Equal(t, no(), b.Images, "has: on a present list without the value is false")
	require.Equal(t, no(), b.Tools)
	require.Equal(t, no(), b.Reasoning, "nonempty on an empty list is false")
	require.False(t, b.Hidden)
	require.Empty(t, b.Source("hidden"), "visible absent: hidden is unknown, read as shown")

	c := got["c"]
	require.Equal(t, yes(), c.Images, "has: on an absent list falls through to the next path")
	require.Nil(t, c.Tools, "absent is unknown")
	require.Nil(t, c.Reasoning)
	require.Nil(t, c.Endpoints, "an array holding a non-string is not a string array")
}

func TestEveryFieldIsTaggedWithItsSource(t *testing.T) {
	m := Mapping{
		List:      "data",
		Context:   []string{"context_length"},
		MaxOutput: []string{"max_output_tokens"},
		Images:    []string{"vision"},
		Facts: map[string]Facts{
			"glm*":   {Context: 1, MaxOutput: 2, Tools: yes()},
			"qwen-3": {Context: 262_144, Long: 1_000_000, Images: no()},
		},
	}
	overrides := map[string]Override{"explabs/glm-5.3": {TextOnly: yes(), MaxOutput: 65_536}}
	got := read(t, m, overrides, `{"data":[
		{"id":"explabs/glm-5.3","context_length":1048576,"max_output_tokens":131072,"vision":true},
		{"id":"explabs/qwen.3"}
	]}`)

	glm := got["explabs/glm-5.3"]
	require.Equal(t, 1_048_576, glm.Window(""), "the API beats a fact")
	require.Equal(t, models.SourceAPI, glm.Source("context"))
	require.Equal(t, 65_536, glm.MaxOutput, "config beats the API")
	require.Equal(t, models.SourceConfig, glm.Source("max_output"))
	takes, src := glm.TakesImages()
	require.False(t, takes, "text_only beats the API's vision flag")
	require.Equal(t, models.SourceConfig, src)
	require.Equal(t, yes(), glm.Tools, "a fact fills what the API left out")
	require.Equal(t, models.SourceMapping, glm.Source("tools"))

	qwen := got["explabs/qwen.3"]
	require.Equal(t, 262_144, qwen.Window(""), "facts match the normalized id: no vendor prefix, dots as dashes")
	require.Equal(t, 1_000_000, qwen.Window(models.LongContextTier))
	require.Equal(t, models.SourceMapping, qwen.Source("context"))
}

// "claude-opus-4*" names claude-opus-4 and its dashed descendants, never
// claude-opus-42; the longest pattern wins, and an exact id beats a prefix.
func TestFactPatternsMatchOnDashBoundaries(t *testing.T) {
	m := Mapping{Facts: map[string]Facts{
		"claude-opus-4*":   {Context: 200_000},
		"claude-opus-4-6*": {Context: 1_000_000},
		"claude-3*":        {Context: 200_000},
		"exact":            {Context: 7},
		"exact*":           {Context: 8},
	}}
	r, err := New(m, nil)
	require.NoError(t, err)
	for id, want := range map[string]int{
		"claude-opus-4":                1 * 200_000,
		"claude-opus-4-5-20251101":     200_000,
		"claude-opus-4-6":              1_000_000,
		"anthropic/claude-opus-4.6":    1_000_000,
		"claude-opus-4-6-20260101":     1_000_000,
		"claude-3-5-sonnet":            200_000,
		"exact":                        7,
		"exact-2":                      8,
		"claude-opus-42":               0,
		"claude-opus-4x":               0,
		"some-unreleased-model":        0,
		"CLAUDE-OPUS-4-6-something-up": 1_000_000,
	} {
		e, ok := r.Seed(id)
		require.Equal(t, want > 0, ok, id)
		require.Equal(t, want, e.Window(""), id)
	}
}

// A model the endpoint never lists still gets what the mapping and the
// provider file say about it.
func TestSeedAnswersFromFactsAndConfigAlone(t *testing.T) {
	r, err := New(Mapping{Facts: map[string]Facts{"claude-opus-5*": {Context: 1_000_000}}},
		map[string]Override{"private/model": {Context: 64_000, TextOnly: yes()}})
	require.NoError(t, err)

	e, ok := r.Seed("claude-opus-5-5")
	require.True(t, ok)
	require.Equal(t, 1_000_000, e.Window(""))
	require.Equal(t, models.SourceMapping, e.Source("context"))

	e, ok = r.Seed("private/model")
	require.True(t, ok)
	require.Equal(t, 64_000, e.Window(""))
	require.Equal(t, 64_000, e.Window(models.LongContextTier), "a configured context is the long tier's too")
	takes, src := e.TakesImages()
	require.False(t, takes)
	require.Equal(t, models.SourceConfig, src)

	_, ok = r.Seed("unknown")
	require.False(t, ok)
}

func TestPaging(t *testing.T) {
	r, err := New(Mapping{List: "data", More: "has_more", Cursor: "last_id", CursorParam: "after_id"}, nil)
	require.NoError(t, err)
	require.True(t, r.Paged())

	listed, cursor, err := r.Page(strings.NewReader(`{"data":[{"id":"a"},{"id":"b"}],"has_more":true,"last_id":"b"}`))
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, "b", cursor)
	next, err := r.NextURL("https://api.example/v1/models?limit=100", cursor)
	require.NoError(t, err)
	require.Equal(t, "https://api.example/v1/models?after_id=b&limit=100", next)

	_, cursor, err = r.Page(strings.NewReader(`{"data":[{"id":"c"}],"has_more":false,"last_id":"c"}`))
	require.NoError(t, err)
	require.Empty(t, cursor)

	_, err = New(Mapping{More: "has_more"}, nil)
	require.Error(t, err, "half a paging mapping is refused")
}

func TestABadMappingIsRefused(t *testing.T) {
	for _, m := range []Mapping{
		{Context: []string{""}},
		{Context: []string{"a b c"}},
		{Images: []string{"x has:"}},
		{Images: []string{"x contains:image"}},
		{Facts: map[string]Facts{"claude-*-opus": {Context: 1}}},
		{Facts: map[string]Facts{"*": {Context: 1}}},
	} {
		_, err := New(m, nil)
		require.Error(t, err, "%+v", m)
	}
}

func TestAResponseWithoutTheListIsAnError(t *testing.T) {
	r, err := New(Mapping{List: "data"}, nil)
	require.NoError(t, err)
	_, _, err = r.Page(strings.NewReader(`{"models":[]}`))
	require.ErrorContains(t, err, "no list at data")
	_, _, err = r.Page(strings.NewReader(`not json`))
	require.Error(t, err)

	top, err := New(Mapping{}, nil)
	require.NoError(t, err)
	listed, _, err := top.Page(strings.NewReader(`[{"id":"a"}]`))
	require.NoError(t, err)
	require.Len(t, listed, 1, "an empty list path reads a top-level array")
}

// A provider file's [catalog] replaces the built-in mapping key by key: a key
// it names (even as []) is its own, a key it leaves out stays built-in.
func TestAFileMappingReplacesKeyByKey(t *testing.T) {
	builtin := Mapping{
		List:      "data",
		Context:   []string{"context_length"},
		Long:      []string{"long_length"},
		MaxOutput: []string{"max_output_tokens"},
		Facts:     map[string]Facts{"a*": {Context: 1}, "b*": {Context: 2}},
	}
	var file struct {
		Catalog *Mapping `toml:"catalog"`
	}
	_, err := toml.Decode(`
[catalog]
context = ["limits.max_prompt_tokens"]
long = []
[catalog.facts."b*"]
context = 3
`, &file)
	require.NoError(t, err)
	got := builtin.With(file.Catalog)
	require.Equal(t, "data", got.List)
	require.Equal(t, []string{"limits.max_prompt_tokens"}, got.Context)
	require.Empty(t, got.Long, "an empty array clears the key")
	require.Equal(t, []string{"max_output_tokens"}, got.MaxOutput)
	require.Equal(t, 1, got.Facts["a*"].Context)
	require.Equal(t, 3, got.Facts["b*"].Context)
	require.Equal(t, builtin, builtin.With(nil))
}
