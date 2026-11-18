package angelus

import (
	"testing"

	"github.com/stretchr/testify/require"

	providerPkg "github.com/jack-work/figaro/internal/provider"
)

// A seat naming a provider and no model drops a model that belongs to a
// different provider, on both the fork and the threshold path, and refuses
// when the seat's provider has no default to fall back to.
func TestOrphanedModel(t *testing.T) {
	providerPkg.Register(&providerPkg.Registration{Name: "orphan-defaulted", DefaultModel: "m"})
	providerPkg.Register(&providerPkg.Registration{Name: "orphan-declared", Declared: true})
	providerPkg.Register(&providerPkg.Registration{Name: "orphan-bare"})
	for _, tc := range []struct {
		seat, under string
		drop        bool
		err         string
	}{
		{"orphan-defaulted", "orphan-defaulted", false, ""},
		{"orphan-defaulted", "chatgpt-personal", true, ""},
		{"orphan-declared", "chatgpt-personal", true, ""},
		{"orphan-bare", "chatgpt-personal", false, "orphan-bare has no default model"},
		{"orphan-unknown", "chatgpt-personal", false, "orphan-unknown has no default model"},
	} {
		drop, err := orphanedModel(tc.seat, tc.under)
		if tc.err != "" {
			require.ErrorContains(t, err, tc.err, tc.seat)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.drop, drop, "%s over %s", tc.seat, tc.under)
	}
}
