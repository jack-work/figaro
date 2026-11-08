package catalogmaps

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryBuiltInMappingLoads(t *testing.T) {
	names, err := fs.Glob(files, "*.toml")
	require.NoError(t, err)
	require.NotEmpty(t, names)
	for _, n := range names {
		_, err := load(strings.TrimSuffix(n, ".toml"))
		require.NoError(t, err, n)
	}
	_, err = load("nope")
	require.Error(t, err)
}
