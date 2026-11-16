package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// Defends #119: after a restart, a dormant aria whose bound form carries
// system.tombstone while its node was never buried opens with f.sealed=true,
// refuses ordinary writes with "form is tombstoned: no further patches", yet
// accepts system.ttl and kill (soft reap and purge) so it can be reaped cleanly.
func TestTombstonedBoundFormAriasCanBeReapedAndPurged(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "mock.toml"),
		[]byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("default_outfit = \"mock\"\n"), 0o600))

	a1, acli1, ctx1, _ := daemonWithHandlers(t, dir, nil)
	byKill, err := acli1.Create(ctx1, dress(t, "mock"), nil)
	require.NoError(t, err)
	byTTL, err := acli1.Create(ctx1, dress(t, "mock"), nil)
	require.NoError(t, err)

	require.NoError(t, a1.Registry.Kill(byKill.FigaroID))
	require.NoError(t, a1.Registry.Kill(byTTL.FigaroID))

	raw, _ := json.Marshal("interrupted delete")
	seal := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.TombstoneKey: raw}, nil)
	for _, id := range []string{byKill.FigaroID, byTTL.FigaroID} {
		_, err := a1.Backend.(*store.XwalBackend).ApplyFormPrivileged(id, seal)
		require.NoError(t, err)
	}
	acli1.Close()
	a1.Shutdown(time.Second)
	require.NoError(t, a1.Backend.Close())

	a2, acli2, ctx2, _ := daemonWithHandlers(t, dir, nil)

	var setOut rpc.SetResponse
	err = dialAria(t, a2, byKill.FigaroID, nil).Call(ctx2, rpc.MethodSet,
		rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"mantra": "blocked"})}, &setOut)
	require.ErrorContains(t, err, "form is tombstoned: no further patches")

	err = dialAria(t, a2, byKill.FigaroID, nil).Call(ctx2, rpc.MethodSet,
		rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemTTLKey: "1h"})}, &setOut)
	require.NoError(t, err)

	kill(t, acli2, ctx2, rpc.KillRequest{FigaroID: byKill.FigaroID, GraceMS: time.Hour.Milliseconds()})
	require.True(t, reaped(t, a2, byKill.FigaroID))

	listed, err := acli2.ListGlobal(ctx2)
	require.NoError(t, err)
	for _, row := range listed.Figaros {
		if row.ID == byKill.FigaroID {
			require.NotZero(t, row.ReapedAt, "killed tombstoned aria must carry ReapedAt")
		}
	}

	setReapedUntil(t, a2, time.Now().Add(-time.Second), byKill.FigaroID)
	a2.SweepLifetimesNow()
	require.False(t, ariaExists(a2, byKill.FigaroID))

	require.NoError(t, dialAria(t, a2, byTTL.FigaroID, nil).Call(ctx2, rpc.MethodSet,
		rpc.SetRequest{Patch: *heldPatch(t, map[string]any{
			store.SystemTTLKey:       "1ms",
			store.SystemKeepaliveKey: "0s",
		})}, &setOut))
	time.Sleep(5 * time.Millisecond)
	a2.SweepLifetimesNow()
	require.True(t, reaped(t, a2, byTTL.FigaroID) || !ariaExists(a2, byTTL.FigaroID))
}
