package store

import (
	"github.com/jack-work/figaro/api/message"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A store several versions behind runs every migration in turn, on open,
// without anybody naming them.
func TestMigrationsRunBackToBackOnOpen(t *testing.T) {
	root := t.TempDir()

	// Build a store the current way, then wind its sidecar back so the open
	// has both channels to migrate.
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	ir, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")}}})
	require.NoError(t, err)
	require.NoError(t, be.Close())

	f, err := readSchema(root)
	require.NoError(t, err)
	f.Channels[chanForm] = 1
	f.Channels[chanIR] = 4
	require.NoError(t, writeSchema(root, f))

	// Opening is the whole trigger.
	be2, err := NewXwalBackend(root, 0)
	require.NoError(t, err, "an out-of-date store must migrate, not refuse")
	defer be2.Close()

	after, err := readSchema(root)
	require.NoError(t, err)
	require.Equal(t, 2, after.Channels[chanForm], "the form channel did not reach v2")
	require.Equal(t, 5, after.Channels[chanIR], "the IR channel did not reach v5")

	// And the board survived both.
	snap, err := be2.FormState(aria)
	require.NoError(t, err)
	require.Equal(t, "/tmp", *snap.Lookup("system.cwd"))
	require.Equal(t, "m", *snap.Lookup("system.model"))
}

// Running the chain twice must change nothing: the sidecar is stamped only
// after every step, so a crash re-runs from the version on disk.
func TestMigrationsAreIdempotent(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	_, _, err = be.ForkWith(outfit, 0, setPatch(map[string]string{"a.b": "c"}))
	require.NoError(t, err)
	require.NoError(t, be.Close())

	before := snapshotTree(t, root)
	require.NoError(t, MigrateFormToStructural(root))
	require.NoError(t, MigrateStampTurnIDs(root))
	first := snapshotTree(t, root)
	require.NoError(t, MigrateFormToStructural(root))
	require.NoError(t, MigrateStampTurnIDs(root))
	require.Equal(t, first, snapshotTree(t, root), "a second run changed bytes")
	_ = before
}

// snapshotTree hashes every record file under the store.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(p) != ".jsonl" {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = string(b)
		return nil
	})
	require.NoError(t, err)
	return out
}
