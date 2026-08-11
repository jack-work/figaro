package store_test

import (
	"encoding/json"
	"fmt"
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

// A version quoted back is a promise the write is refused if the form moved -
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
	require.Len(t, again.PatchesBetween(0, ^uint64(0)), 2)
	assert.Equal(t, uint64(1), again.PatchesBetween(0, ^uint64(0))[0].Version)
}

// One writer, many callers: every Apply is serialized and every version is
// distinct, with readers never blocked.
//
// Each writer writes a DIFFERENT value on purpose. Thirty-two writers all
// setting k=1 is not a test of serialization: since the writer reduces a
// patch against the board, thirty-one of those are no-ops and SHOULD share a
// version. What must never happen is two real changes landing on one.
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
			v, err := f.Apply(set("k", fmt.Sprintf("%d", i)), 0)
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

// A patch that changes nothing is not an event. The rule lives in the WRITER,
// where the diff is atomic with the append, so both write paths: the agent's
// board and an agentless form: obey it identically, and an aria observing a
// form derives no transition from a set that set nothing.
func TestNoOpPatchIsNotAnEvent(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	var committed int
	f.OnCommit(func(uint64, message.Patch) { committed++ })

	v1, applied, err := f.ApplyEffect(set("k", `1`), 0)
	require.NoError(t, err)
	require.Equal(t, uint64(1), v1)
	require.Contains(t, applied.Set, "k")

	// The same value again: no version, no record, no delta, and the caller
	// is told plainly that nothing landed.
	v2, applied, err := f.ApplyEffect(set("k", `1`), 0)
	require.NoError(t, err)
	assert.Equal(t, v1, v2, "a no-op must not move the version")
	assert.True(t, applied.IsEmpty(), "and must report that nothing landed")

	// A removal of a key that is not there is the same kind of nothing.
	v3, applied, err := f.ApplyEffect(message.Patch{Remove: []string{"absent"}}, 0)
	require.NoError(t, err)
	assert.Equal(t, v1, v3)
	assert.True(t, applied.IsEmpty())

	// A real change still is one, and only the changed half of a mixed patch
	// survives the reduction.
	v4, applied, err := f.ApplyEffect(message.Patch{Set: map[string]json.RawMessage{
		"k": json.RawMessage(`1`),   // unchanged
		"j": json.RawMessage(`"n"`), // new
	}}, 0)
	require.NoError(t, err)
	assert.Greater(t, v4, v1)
	assert.NotContains(t, applied.Set, "k")
	assert.Contains(t, applied.Set, "j")
	assert.Equal(t, 2, committed, "two real events, and no others")
}

func apply(f *store.Form, p message.Patch) error {
	_, err := f.Apply(p, 0)
	return err
}
