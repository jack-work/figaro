package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/sdk"
)

// Fork point 4: the builder runs on what its lead runs on now, not on what
// the first holder ran on when the turn it was cut at was written.
func TestABuilderTakesItsLeadsProviderAndModel(t *testing.T) {
	s := successionOf(t)
	lead, err := sdk.DialAria(ariaAddr(s.a, s.lead), nil)
	require.NoError(t, err)
	defer lead.Close()
	_, err = lead.Set(s.ctx, *rawPatch(map[string]string{
		"system.provider": `"later-provider"`, "system.model": `"later-model"`,
	}), 0)
	require.NoError(t, err)

	engine := func(id string) [2]string {
		snap, err := s.a.Backend.FormState(id)
		require.NoError(t, err)
		var out [2]string
		for i, key := range []string{"system.provider", "system.model"} {
			if v := snap.Lookup(key); v != nil {
				out[i] = *v
			}
		}
		return out
	}
	require.Equal(t, [2]string{"mock", "mock-model"}, engine(s.first), "setup: the first holder's engine moved")
	for _, cut := range builderCuts {
		id := s.builder(t, cut.keepQuestion)
		require.Equal(t, [2]string{"later-provider", "later-model"}, engine(id), cut.name)
	}

	own, err := s.acli.Fork(s.ctx, s.lead, 1, 0, nil, rawPatch(map[string]string{"system.model": `"asked-for"`}))
	require.NoError(t, err)
	require.Equal(t, [2]string{"later-provider", "asked-for"}, engine(own.Alternative), "a model named on the fork must win")
}
