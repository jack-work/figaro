package angelus_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

// review-lane-usability-1 #9: the client words the one-line failure of an
// unroutable default, so the daemon hands it its own config path and which
// outfits are missing; the client's config may not be the daemon's.
func TestAnUnroutableDefaultNamesTheDaemonsConfigAndWhatIsMissing(t *testing.T) {
	for name, c := range map[string]struct {
		outfit  string
		missing []string
	}{
		"missing":     {"", []string{"default_kic"}},
		"no provider": {"[system]\nmodel = \"m\"\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, acli, ctx, dir := daemonFixtureConfig(t, "")
			require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"default_kic\"\n"), 0o600))
			if c.outfit != "" {
				require.NoError(t, os.WriteFile(dir+"/outfits/default_kic.toml", []byte(c.outfit), 0o600))
			}
			_, err := acli.Configure(ctx, rpc.ConfigureRequest{Refresh: true})
			require.NoError(t, err)
			_, err = acli.Create(ctx, nil, nil)
			var jerr *jkrpc.Error
			require.True(t, errors.As(err, &jerr), "not a typed error: %v", err)
			require.Equal(t, rpc.ErrNoProvider, jerr.Code)
			var data rpc.ErrorData
			require.NoError(t, json.Unmarshal(jerr.Data, &data))
			require.Equal(t, dir+"/config.toml", data.ConfigPath)
			require.Equal(t, c.missing, data.Missing)
		})
	}
}

func TestAnUnroutableDefaultOverOutfitsThatDoNotIndexSaysSo(t *testing.T) {
	_, acli, ctx, dir := daemonFixtureConfig(t, "")
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"default_kic\"\n"), 0o600))
	_, err := acli.Configure(ctx, rpc.ConfigureRequest{Refresh: true})
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(dir+"/outfits/sealed", 0o000))
	t.Cleanup(func() { os.Chmod(dir+"/outfits/sealed", 0o700) })
	_, err = acli.Create(ctx, nil, nil)
	require.ErrorContains(t, err, "permission denied", "an outfit tree the daemon cannot read is reported as that, not as a missing outfit")
	var jerr *jkrpc.Error
	if errors.As(err, &jerr) {
		require.NotEqual(t, rpc.ErrNoProvider, jerr.Code)
	}
}
