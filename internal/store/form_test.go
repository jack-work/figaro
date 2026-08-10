package store_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

func set(k, v string) message.Patch {
	return message.Patch{Set: map[string]json.RawMessage{k: json.RawMessage(v)}}
}

// A form needs no aria, no daemon and no store: the algebra and the published
// state stand on their own.
func TestFormStandsAlone(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	snap, version := f.Snapshot()
	assert.Zero(t, version)
	assert.Zero(t, snap.Len())

	v1, err := f.Apply(set("mantra", `"sing"`), 0)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), v1)

	snap, version = f.Snapshot()
	assert.Equal(t, uint64(1), version)
	got, _ := snap.Get("mantra")
	assert.Equal(t, `"sing"`, string(got))
}

// A version quoted back is a promise the write is refused if the form moved —
// the guard a read-modify-write needs, since editing inside a value means
// reading it first.
func TestFormIfVersionRefusesALostUpdate(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	require.NoError(t, apply(f, set("a", `1`)))
	_, version := f.Snapshot()

	// Someone else writes in between.
	require.NoError(t, apply(f, set("b", `2`)))

	_, err := f.Apply(set("a", `3`), version)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "form moved")

	// Unconditional still lands, and the stale value never did.
	_, err = f.Apply(set("a", `3`), 0)
	require.NoError(t, err)
	snap, _ := f.Snapshot()
	a, _ := snap.Get("a")
	assert.Equal(t, `3`, string(a))
}

// Replay rebuilds the published state and the patches the projection renders.
func TestFormReplaysItsLog(t *testing.T) {
	log := &store.MemFormLog{}
	f, err := store.OpenForm(log)
	require.NoError(t, err)
	require.NoError(t, apply(f, set("a", `1`)))
	require.NoError(t, apply(f, set("b", `2`)))
	f.Close()

	again, err := store.OpenForm(log)
	require.NoError(t, err)
	defer again.Close()

	snap, version := again.Snapshot()
	assert.Equal(t, uint64(2), version)
	assert.Equal(t, 2, snap.Len())
	require.Len(t, again.Patches(), 2)
	assert.Equal(t, uint64(1), again.Patches()[0].Version)
}

// One writer, many callers: every Apply is serialized and every version is
// distinct, with readers never blocked.
func TestFormSerializesConcurrentWrites(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	const writers = 32
	var wg sync.WaitGroup
	versions := make([]uint64, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := f.Apply(set("k", `1`), 0)
			assert.NoError(t, err)
			versions[i] = v
			f.Snapshot() // readers run throughout
		}(i)
	}
	wg.Wait()

	seen := map[uint64]bool{}
	for _, v := range versions {
		assert.False(t, seen[v], "version %d handed out twice", v)
		seen[v] = true
	}
	_, version := f.Snapshot()
	assert.Equal(t, uint64(writers), version)
}

func apply(f *store.Form, p message.Patch) error {
	_, err := f.Apply(p, 0)
	return err
}
