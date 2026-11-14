package store

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUnknownContextSuccessionUsesAnExplicitTokenFallback(t *testing.T) {
	r, err := RetirementOf(retirementSnap(t, map[string]any{"context_threshold": "60%", "fallback_tokens": 75000, "action": "succeed"}))
	require.NoError(t, err)
	require.Equal(t, 75000, r.Threshold(0))
	require.Equal(t, 60000, r.WarnAt(0))
	require.Equal(t, 120000, r.Threshold(200000))
	require.True(t, r.Succeeds())
	for _, invalid := range []any{0, -1, "60%", 1.5} {
		_, err = RetirementOf(retirementSnap(t, map[string]any{"context_threshold": "60%", "fallback_tokens": invalid}))
		require.Error(t, err)
	}
}
