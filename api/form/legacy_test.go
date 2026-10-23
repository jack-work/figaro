package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// A flat patch no longer decodes; migration replays each record against the
// bound form it lands on.

func TestLegacyFlatSnapshotNests(t *testing.T) {
	var s form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(`{"system.model":"m","system.cwd":"/tmp","mantra":"hi"}`), &s))
	require.Equal(t, "m", *s.Lookup("system.model"))
	require.Equal(t, "/tmp", *s.Lookup("system.cwd"))
	require.Equal(t, "hi", *s.Lookup("mantra"))

	v, ok := s.Get("system")
	require.True(t, ok)
	require.JSONEq(t, `{"model":"m","cwd":"/tmp"}`, string(v))
}
