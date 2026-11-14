package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/config"
)

// Only an endpoint whose callers' groups come from the edge ends follows at the
// edge follow age; a bearer endpoint's groups come from the token it verifies.
func TestOnlyTheEdgeEndpointTakesTheFollowAge(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[endpoint]\nedge_follow_max_age = \"20m\"\n"), 0o600))
	loaded, err := config.Load(dir)
	require.NoError(t, err)

	edge, err := webEndpointLimits(loaded, config.WebEndpoint{Authn: "upstream", MaxConnAge: 8 * time.Hour}, nil)
	require.NoError(t, err)
	require.Equal(t, 20*time.Minute, edge.FollowMaxAge)
	require.Equal(t, 8*time.Hour, edge.MaxConnAge)

	bearer, err := webEndpointLimits(loaded, config.WebEndpoint{Authn: "bearer", MaxConnAge: 8 * time.Hour}, nil)
	require.NoError(t, err)
	require.Zero(t, bearer.FollowMaxAge)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[endpoint]\nedge_follow_max_age = \"0s\"\n"), 0o600))
	loaded, err = config.Load(dir)
	require.NoError(t, err)
	_, err = webEndpointLimits(loaded, config.WebEndpoint{Authn: "upstream"}, nil)
	require.ErrorContains(t, err, "edge_follow_max_age")
}
