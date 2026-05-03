package angelus_test

import (
	"log"
	"os"
	"path/filepath"
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
	require.NoError(t, r.Bind(self, "aria-one"))
	require.NoError(t, r.Bind(os.Getppid(), "aria-two"))

	require.NoError(t, angelus.SaveBindings(r, path))
	_, err := os.Stat(path)
	require.NoError(t, err)

	// New registry with the same figaros but no PID bindings.
	r2 := angelus.NewRegistry()
	require.NoError(t, r2.Register(newMock("aria-one")))
	require.NoError(t, r2.Register(newMock("aria-two")))

	logger := log.New(os.Stderr, "test: ", 0)
	angelus.RestoreBindings(r2, path, logger, nil)

	id, f := r2.Resolve(self)
	assert.NotNil(t, f)
	assert.Equal(t, "aria-one", id)

	id, f = r2.Resolve(os.Getppid())
	assert.NotNil(t, f)
	assert.Equal(t, "aria-two", id)

	// File should be consumed.
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "bindings file should be removed after restore")
}

func TestRestoreBindings_SkipsDeadPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")

	// 2^30 is well above default kernel.pid_max (4M) — cannot be alive.
	const deadPID = 1 << 30
	file := `{"bindings":[{"pid":1073741824,"figaro_id":"ghost","start_time":42}]}`
	require.NoError(t, os.WriteFile(path, []byte(file), 0600))

	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("ghost")))

	angelus.RestoreBindings(r, path, log.New(os.Stderr, "test: ", 0), nil)

	_, f := r.Resolve(deadPID)
	assert.Nil(t, f, "dead pid should not be rebound")

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRestoreBindings_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")

	r := angelus.NewRegistry()
	// Should not panic or log-error on a missing file.
	angelus.RestoreBindings(r, path, log.New(os.Stderr, "test: ", 0), nil)
}
