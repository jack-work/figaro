package store_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

func routeOf(t *testing.T, f *store.Form) []string {
	t.Helper()
	snap, _ := f.Snapshot()
	raw, ok := snap.Get(form.RouteKey)
	if !ok {
		return nil
	}
	var r []string
	require.NoError(t, json.Unmarshal(raw, &r))
	return r
}

func setKeys(t *testing.T, f *store.Form, set map[string]string) error {
	t.Helper()
	snap, _ := f.Snapshot()
	raws := map[string]json.RawMessage{}
	for k, v := range set {
		raws[k] = json.RawMessage(v)
	}
	_, err := f.Apply(form.Build(snap, raws, nil), 0)
	return err
}

func TestTheDaemonRecordsTheRouteWhenProviderOrModelMoves(t *testing.T) {
	f := store.NewMemForm()
	require.NoError(t, setKeys(t, f, map[string]string{"system.provider": `"rou2"`, "system.model": `"explabs/glm-5.3"`}))
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, routeOf(t, f))

	require.NoError(t, setKeys(t, f, map[string]string{"system.provider": `"anthropic"`, "system.model": `"claude-opus-5"`}))
	require.Equal(t, []string{"anthropic", "claude-opus-5"}, routeOf(t, f))

	require.NoError(t, setKeys(t, f, map[string]string{"mantra": `"x"`}))
	require.Equal(t, []string{"anthropic", "claude-opus-5"}, routeOf(t, f))
}

func TestNobodyButTheDaemonWritesTheRoute(t *testing.T) {
	f := store.NewMemForm()
	require.Error(t, setKeys(t, f, map[string]string{form.RouteKey: `["rou2","x"]`}))
	require.Nil(t, routeOf(t, f))
}
