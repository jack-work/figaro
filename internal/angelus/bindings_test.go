package angelus_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

	_, err = os.Stat(path)
	assert.NoError(t, err, "a crash right after a restore must find the bindings again")
}

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
}

func TestRestoreBindings_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")

	r := angelus.NewRegistry()
	angelus.RestoreBindings(r, path, nil)
}

func persistedIDs(t *testing.T, path string) map[int]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file struct {
		Bindings []struct {
			PID      int    `json:"pid"`
			FigaroID string `json:"figaro_id"`
		} `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal(data, &file))
	out := map[int]string{}
	for _, b := range file.Bindings {
		out[b.PID] = b.FigaroID
	}
	return out
}

func waitPersisted(t *testing.T, path string, want map[int]string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := persistedIDs(t, path)
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bindings on disk = %v, want %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A daemon that dies without running any shutdown code (SIGKILL, a panic,
// the OOM killer) leaves only what is already on disk: every bind, unbind and
// kill must reach the file while the daemon is still up.
func TestPersistBindings_EveryChangeReachesDiskWithoutAShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("kept")))
	require.NoError(t, r.Register(newMock("killed")))
	go angelus.PersistBindings(r, path)

	self, parent := os.Getpid(), os.Getppid()
	require.NoError(t, r.Bind(self, "kept", 0))
	require.NoError(t, r.Bind(parent, "killed", 0))
	waitPersisted(t, path, map[int]string{self: "kept", parent: "killed"})

	require.NoError(t, r.Kill("killed"))
	waitPersisted(t, path, map[int]string{self: "kept"})

	r.Unbind(self)
	waitPersisted(t, path, map[int]string{})

	require.NoError(t, r.Bind(self, "kept", 0))
	waitPersisted(t, path, map[int]string{self: "kept"})

	r2 := angelus.NewRegistry()
	angelus.RestoreBindings(r2, path, nil)
	id, _, _ := r2.Resolve(self)
	assert.Equal(t, "kept", id, "the next daemon did not find the binding the dead one held")
}

// The drain kills every resident aria, and Kill drops its pids: the file must
// keep what the shells held before the drain began.
func TestPersistBindings_TheDrainDoesNotEmptyTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("resident")))
	go angelus.PersistBindings(r, path)

	self := os.Getpid()
	require.NoError(t, r.Bind(self, "resident", 0))
	waitPersisted(t, path, map[int]string{self: "resident"})

	r.SetDraining()
	require.NoError(t, r.Kill("resident"))
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, map[int]string{self: "resident"}, persistedIDs(t, path),
		"the drain's kills reached the bindings file")
}

func TestSaveBindings_RefusedWhileDraining(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	r := angelus.NewRegistry()
	require.NoError(t, r.Bind(os.Getpid(), "aria", 0))
	r.SetDraining()
	assert.Error(t, angelus.SaveBindings(r, path))
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "a save during the drain wrote a file")
}
