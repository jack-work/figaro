package angelus_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
)

func TestSaveAndRestoreBindings_LivePIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")

	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("aria-one")))
	require.NoError(t, r.Register(newMock("aria-two")))

	self := os.Getpid()
	require.NoError(t, r.Bind(self, "aria-one", 0))
	require.NoError(t, r.Bind(os.Getppid(), "aria-two", 0))

	require.NoError(t, angelus.SaveBindings(r, path))
	_, err := os.Stat(path)
	require.NoError(t, err)

	// New registry with the same figaros but no PID bindings.
	r2 := angelus.NewRegistry()
	require.NoError(t, r2.Register(newMock("aria-one")))
	require.NoError(t, r2.Register(newMock("aria-two")))

	angelus.RestoreBindings(r2, path, nil)

	id, f, _ := r2.Resolve(self)
	assert.NotNil(t, f)
	assert.Equal(t, "aria-one", id)

	id, f, _ = r2.Resolve(os.Getppid())
	assert.NotNil(t, f)
	assert.Equal(t, "aria-two", id)

	// File should be consumed.
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "bindings file should be removed after restore")
}

// A binding to a dormant aria must survive the round trip. Nothing is
// registered here: dormant is the point.
func TestSaveAndRestoreBindings_DormantAria(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")

	r := angelus.NewRegistry()
	self := os.Getpid()
	require.NoError(t, r.Bind(self, "sleeper", 0))
	require.Empty(t, r.List(), "the aria is dormant; nothing is resident")

	require.NoError(t, angelus.SaveBindings(r, path))

	r2 := angelus.NewRegistry()
	angelus.RestoreBindings(r2, path, nil)

	id, f, _ := r2.Resolve(self)
	assert.Equal(t, "sleeper", id, "binding to a dormant aria was not persisted")
	assert.Nil(t, f, "still dormant after restore: a rebind must not wake anything")
}

// The file is rewritten on every save, so identical state must produce
// identical bytes.
func TestSaveBindings_StableOrdering(t *testing.T) {
	dir := t.TempDir()

	r := angelus.NewRegistry()
	for i, id := range []string{"zeta", "alpha", "mu"} {
		require.NoError(t, r.Bind(9000+i, id, 0))
	}

	var first []byte
	for i := range 5 {
		path := filepath.Join(dir, "bindings.json")
		require.NoError(t, angelus.SaveBindings(r, path))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.Remove(path))
		if i == 0 {
			first = data
			continue
		}
		assert.Equal(t, string(first), string(data), "save %d differs: map order leaked", i)
	}
}

// A v1 file (parent pids) must not be replayed into a v2 daemon.
func TestRestoreBindings_SkipsOldVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	self := os.Getpid()
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"bindings":[{"pid":`+strconv.Itoa(self)+`,"figaro_id":"stale"}]}`), 0o600))

	r := angelus.NewRegistry()
	angelus.RestoreBindings(r, path, nil)

	id, _, _ := r.Resolve(self)
	assert.Empty(t, id, "a v1 binding was replayed under v2 key semantics")
}

func TestRestoreBindings_SkipsDeadPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")

	// 2^30 is well above default kernel.pid_max (4M): cannot be alive.
	const deadPID = 1 << 30
	file := `{"bindings":[{"pid":1073741824,"figaro_id":"ghost","start_time":42}]}`
	require.NoError(t, os.WriteFile(path, []byte(file), 0600))

	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("ghost")))

	angelus.RestoreBindings(r, path, nil)

	_, f, _ := r.Resolve(deadPID)
	assert.Nil(t, f, "dead pid should not be rebound")

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRestoreBindings_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")

	r := angelus.NewRegistry()
	// Should not panic or log-error on a missing file.
	angelus.RestoreBindings(r, path, nil)
}
