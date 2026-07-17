package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
)

func TestEnvironmentStrategiesUsesRegistrationOrder(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "copilot")
	t.Setenv("GH_TOKEN", "gh")
	t.Setenv("GITHUB_TOKEN", "github")

	resolver := &auth.Aggregate{Strategies: environmentStrategies(&provider.Registration{
		EnvVars: []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
	})}
	token, err := resolver.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "copilot", token)

	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	token, err = resolver.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "gh", token)

	t.Setenv("GH_TOKEN", "")
	token, err = resolver.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "github", token)
}

func TestEnvironmentStrategiesFallsBackToSingleEnvVar(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	resolver := &auth.Aggregate{Strategies: environmentStrategies(&provider.Registration{
		EnvVar: "ANTHROPIC_API_KEY",
	})}
	token, err := resolver.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "key", token)
}
