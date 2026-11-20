package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

func routeBound(t *testing.T, provider, model string) form.Snapshot {
	t.Helper()
	set := map[string]json.RawMessage{}
	if provider != "" {
		set[ProviderKey] = json.RawMessage(`"` + provider + `"`)
	}
	if model != "" {
		set[ModelKey] = json.RawMessage(`"` + model + `"`)
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

func recorded(t *testing.T, s form.Snapshot, set map[string]string, drop ...string) form.Snapshot {
	t.Helper()
	raws := map[string]json.RawMessage{}
	for k, v := range set {
		raws[k] = json.RawMessage(v)
	}
	return s.Apply(RecordRoute(s, form.Build(s, raws, drop)))
}

func routeAt(t *testing.T, s form.Snapshot) []string {
	t.Helper()
	raw, ok := s.Get(form.RouteKey)
	if !ok {
		return nil
	}
	var r []string
	require.NoError(t, json.Unmarshal(raw, &r))
	return r
}

func TestRecordRouteFollowsProviderAndModel(t *testing.T) {
	s := recorded(t, form.Snapshot{}, map[string]string{ProviderKey: `"rou2"`, ModelKey: `"explabs/glm-5.3"`})
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, routeAt(t, s))

	s = recorded(t, s, map[string]string{ModelKey: `"copilot/anthropic/claude-opus-5"`})
	require.Equal(t, []string{"rou2", "copilot", "anthropic", "claude-opus-5"}, routeAt(t, s))

	s = recorded(t, s, map[string]string{ProviderKey: `"anthropic"`, ModelKey: `"claude-opus-5"`})
	require.Equal(t, []string{"anthropic", "claude-opus-5"}, routeAt(t, s))

	s = recorded(t, s, nil, ModelKey)
	require.Equal(t, []string{"anthropic"}, routeAt(t, s))

	s = recorded(t, s, nil, ProviderKey)
	require.Nil(t, routeAt(t, s))
}

func TestRecordRouteLeavesUnrelatedWritesAlone(t *testing.T) {
	s := recorded(t, form.Snapshot{}, map[string]string{ProviderKey: `"anthropic"`, ModelKey: `"claude-opus-5"`})
	p := form.Build(s, map[string]json.RawMessage{"mantra": json.RawMessage(`"x"`)}, nil)
	require.Equal(t, p, RecordRoute(s, p))
	same := form.Build(s, map[string]json.RawMessage{ModelKey: json.RawMessage(`"claude-opus-5"`)}, nil)
	require.True(t, RecordRoute(s, same).IsIdentity())
}

func TestRecordRouteHandlesAWholeSystemObject(t *testing.T) {
	s := recorded(t, form.Snapshot{}, map[string]string{"system": `{"provider":"rou2","model":"explabs/glm-5.3"}`})
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, routeAt(t, s))
	s = recorded(t, s, nil, "system")
	require.Nil(t, routeAt(t, s))
}

func TestRouteOfReadsTheRecordedRouteElseDerivesIt(t *testing.T) {
	derived := routeBound(t, "rou2", "explabs/glm-5.3")
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, RouteOf(derived))
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, RouteOf(recorded(t, derived, map[string]string{form.RouteKey: `["rou2","pinned"]`})))
	require.Nil(t, RouteOf(form.Snapshot{}))
}

func TestRecordRouteOverwritesARouteWrittenWithoutItsSubject(t *testing.T) {
	s := recorded(t, form.Snapshot{}, map[string]string{ProviderKey: `"rou2"`, ModelKey: `"explabs/glm-5.3"`})
	s = recorded(t, s, map[string]string{form.RouteKey: `["forged"]`})
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, routeAt(t, s))
	s = recorded(t, s, nil, form.RouteKey)
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, routeAt(t, s))
}
