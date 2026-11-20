package models

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

func realCatalog(t *testing.T, patch func(map[string]map[string]any)) form.Snapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/catalog-2026-11-19.json")
	require.NoError(t, err)
	var provs map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &provs))
	if patch != nil {
		patch(provs)
	}
	raw, err = json.Marshal(provs)
	require.NoError(t, err)
	var s form.Snapshot
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}

func rou2Row(provs map[string]map[string]any, model string, row map[string]any) {
	var n node = provs["rou2"]
	segs := strings.Split(model, "/")
	for _, seg := range segs[:len(segs)-1] {
		up, _ := n[BranchesKey].(map[string]any)
		if up == nil {
			up = map[string]any{}
			n[BranchesKey] = up
		}
		next, _ := up[seg].(map[string]any)
		if next == nil {
			next = map[string]any{}
			up[seg] = next
		}
		n = next
	}
	leaves, _ := n[LeavesKey].(map[string]any)
	if leaves == nil {
		leaves = map[string]any{}
		n[LeavesKey] = leaves
	}
	leaves[segs[len(segs)-1]] = row
}

type node = map[string]any

// The keys that read "capacity unknown" on 11-19 against the catalog as the
// daemon held it, with the rows rou2 does not yet fill sent the way it is
// asked to send them.
func TestRealCatalogKeysResolveAWindow(t *testing.T) {
	cat := realCatalog(t, func(p map[string]map[string]any) {
		rou2Row(p, "anthropic-kic-2/claude-opus-5-5", map[string]any{"context": map[string]any{"default": 1000000}})
		rou2Row(p, "anthropic-kic/claude-opus-5-5-auto", map[string]any{"rou2_auto": map[string]any{
			"generation": "g1", "context_status": "known",
			"candidates": []any{
				map[string]any{"model": "anthropic-kic/claude-opus-5-5", "apis": []any{"messages"}, "context_length": 1000000},
				map[string]any{"model": "anthropic-kic-2/claude-opus-5-5", "apis": []any{"messages"}, "context_length": 200000},
			},
		}})
	})
	for _, c := range []struct {
		provider, model string
		want            int
	}{
		{"anthropic", "claude-opus-5", 1_000_000},
		{"anthropic", "claude-fable-5", 1_000_000},
		{"anthropic", "anthropic-kic/claude-opus-5-5", 1_000_000},
		{"gemini", "gemini-4-carbon", 1_048_576},
		{"rou2", "explabs/glm-5.3", 1_048_576},
		{"rou2", "explabs/glm-5.3-uncensored", 1_048_576},
		{"rou2", "explabs/qwen3.8-max", 1_000_000},
		{"rou2", "explabs/kimi-k3", 1_048_576},
		{"rou2", "anthropic-kic-2/claude-opus-5-5", 1_000_000},
		{"rou2", "anthropic-kic/claude-opus-5-5-auto", 200_000},
	} {
		bound := boundFormOf(map[string]string{ProviderKey: `"` + c.provider + `"`, ModelKey: `"` + c.model + `"`})
		require.Equal(t, c.want, ContextLimit(cat, bound), "%s/%s", c.provider, c.model)
		require.Equal(t, c.want, EntryLimit(cat, c.provider, c.model, ""), "%s/%s", c.provider, c.model)
	}
}

// A row with no window stays unknown: today's rou2 rows for the
// anthropic-* pools, and ids no provider lists.
func TestRealCatalogKeysWithoutAWindowStayUnknown(t *testing.T) {
	cat := realCatalog(t, nil)
	for _, c := range [][2]string{
		{"rou2", "anthropic-kic-2/claude-opus-5-5"},
		{"rou2", "anthropic-max/claude-sonnet-5-5"},
		{"rou2", "modelflare/claude-opus-5"},
		{"anthropic", "sonnet-5"},
		{"gateway", "explabs/glm-5.3"},
	} {
		bound := boundFormOf(map[string]string{ProviderKey: `"` + c[0] + `"`, ModelKey: `"` + c[1] + `"`})
		require.Zero(t, ContextLimit(cat, bound), "%s/%s", c[0], c[1])
	}
}
