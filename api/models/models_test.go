package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

func catalogOf(t *testing.T, provs map[string]Provider) form.Snapshot {
	t.Helper()
	set := map[string]json.RawMessage{}
	for name, p := range provs {
		raw, err := json.Marshal(p)
		require.NoError(t, err)
		set[name] = raw
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

func boardOf(kv map[string]string) form.Snapshot {
	m := map[string]json.RawMessage{}
	for k, v := range kv {
		m[k] = json.RawMessage(v)
	}
	return form.FromMap(m)
}

// The same model id under two providers is two entries: a gateway's prompt cap
// is not the vendor's window.
func TestLookupIsKeyedByProviderAndModel(t *testing.T) {
	cat := catalogOf(t, map[string]Provider{
		"anthropic": {Models: map[string]Entry{"claude-opus-4-5": {Context: Context{Default: 200_000}}}},
		"copilot":   {Models: map[string]Entry{"claude-opus-4-5": {Context: Context{Default: 128_000}}}},
	})
	a, ok := Lookup(cat, "anthropic", "claude-opus-4-5")
	require.True(t, ok)
	require.Equal(t, 200_000, a.Context.Default)
	c, ok := Lookup(cat, "copilot", "claude-opus-4-5")
	require.True(t, ok)
	require.Equal(t, 128_000, c.Context.Default)

	_, ok = Lookup(cat, "openrouter", "claude-opus-4-5")
	require.False(t, ok, "a miss under one provider must not borrow another's entry")
	_, ok = Lookup(cat, "", "claude-opus-4-5")
	require.False(t, ok)
}

// Dots are the form's nesting separator, and model ids carry them. A dotted id
// must survive a publish, a wire round trip and a mirror apply, and must not be
// confused with a shorter id it extends.
func TestDottedModelIDsSurviveTheWire(t *testing.T) {
	before := catalogOf(t, map[string]Provider{
		"openai": {Models: map[string]Entry{"gpt-4": {Context: Context{Default: 8_192}}}},
	})
	next, err := json.Marshal(Provider{Models: map[string]Entry{
		"gpt-4":     {Context: Context{Default: 8_192}},
		"gpt-4.1":   {Context: Context{Default: 1_047_576}},
		"o4.5-mini": {Context: Context{Default: 200_000, Long: 400_000}},
	}})
	require.NoError(t, err)
	patch := form.Build(before, map[string]json.RawMessage{"openai": next}, nil)

	wire, err := json.Marshal(patch)
	require.NoError(t, err)
	var received form.Patch
	require.NoError(t, json.Unmarshal(wire, &received))
	mirror := before.Apply(received)

	for id, want := range map[string]int{"gpt-4": 8_192, "gpt-4.1": 1_047_576, "o4.5-mini": 200_000} {
		e, ok := Lookup(mirror, "openai", id)
		require.True(t, ok, id)
		require.Equal(t, want, e.Context.Default, id)
	}
	_, ok := Lookup(mirror, "openai", "gpt-4.2")
	require.False(t, ok, "gpt-4.2 must not resolve to gpt-4 with a remainder")

	snap, err := json.Marshal(mirror)
	require.NoError(t, err)
	var reloaded form.Snapshot
	require.NoError(t, json.Unmarshal(snap, &reloaded))
	e, ok := Lookup(reloaded, "openai", "gpt-4.1")
	require.True(t, ok, "a snapshot reload must keep the dotted id whole")
	require.Equal(t, 1_047_576, e.Context.Default)
}

func TestContextLimitPrecedence(t *testing.T) {
	cat := catalogOf(t, map[string]Provider{
		"routed": {Models: map[string]Entry{"m": {Context: Context{Default: 128_000, Long: 1_000_000}}}},
	})
	subject := map[string]string{"system.provider": `"routed"`, "system.model": `"m"`}
	with := func(extra map[string]string) form.Snapshot {
		kv := map[string]string{}
		for k, v := range subject {
			kv[k] = v
		}
		for k, v := range extra {
			kv[k] = v
		}
		return boardOf(kv)
	}

	require.Equal(t, 128_000, ContextLimit(cat, with(nil)))
	require.Equal(t, 1_000_000, ContextLimit(cat, with(map[string]string{"system.context_tier": `"long_context"`})))
	require.Equal(t, 200_000, ContextLimit(cat, with(map[string]string{"system.max_context_tokens": `200000`})),
		"the board's pin beats the catalog")
	require.Equal(t, 128_000, ContextLimit(cat, with(map[string]string{"system.max_context_tokens": `"big"`})),
		"an unreadable pin is no pin")
	require.Equal(t, 128_000, ContextLimit(cat, with(map[string]string{"system.max_context_tokens": `0`})))
	require.Equal(t, 0, ContextLimit(cat, boardOf(map[string]string{"system.provider": `"routed"`, "system.model": `"other"`})))
	require.Equal(t, 0, ContextLimit(form.Snapshot{}, with(nil)))
	require.Equal(t, 300_000, ContextLimit(form.Snapshot{}, with(map[string]string{"system.max_context_tokens": `300000`})),
		"a pin needs no catalog")
}

func TestProviderStatus(t *testing.T) {
	cat := catalogOf(t, map[string]Provider{
		"anthropic": {RefreshedAt: 1_700_000_000_000, AttemptedAt: 1_700_000_100_000, Error: "401"},
	})
	st, ok := ProviderStatus(cat, "anthropic")
	require.True(t, ok)
	require.Equal(t, int64(1_700_000_000_000), st.RefreshedAt.UnixMilli())
	require.Equal(t, int64(1_700_000_100_000), st.AttemptedAt.UnixMilli())
	require.Equal(t, "401", st.Error)
	_, ok = ProviderStatus(cat, "copilot")
	require.False(t, ok)
}

// Providers decodes the whole catalog in one read; a dotted id must come back
// as one model, not as a nested object.
func TestProvidersDecodesTheCatalogWithDottedIDs(t *testing.T) {
	cat := catalogOf(t, map[string]Provider{
		"openai": {RefreshedAt: 7, Models: map[string]Entry{
			"gpt-4.1": {Context: Context{Default: 1_047_576}, Hidden: true, Source: SourceAPI},
		}},
		"copilot": {Error: "no credential"},
	})
	provs, err := Providers(cat)
	require.NoError(t, err)
	require.Len(t, provs, 2)
	require.Equal(t, int64(7), provs["openai"].RefreshedAt)
	require.Equal(t, Entry{Context: Context{Default: 1_047_576}, Hidden: true, Source: SourceAPI}, provs["openai"].Models["gpt-4.1"])
	require.Equal(t, "no credential", provs["copilot"].Error)

	empty, err := Providers(form.Snapshot{})
	require.NoError(t, err)
	require.Empty(t, empty)
}
