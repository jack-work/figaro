package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/internal/store"
)

// A fork must cost the same in a store of 400 figaros as in one of 2,600:
// a birth invalidates the whole-store topology snapshot, so any fork-path
// lookup that reads it rebuilds it, O(nodes) per fork.
func TestForkRebuildsNoTopologySnapshot(t *testing.T) {
	r := successionDaemon(t)
	parent := r.aria(nil)
	turnOn(t, r.a, parent, "parent")
	ids := []string{parent}
	fork := func(ref string) {
		ids = append(ids, r.forkOf(ref))
	}
	settle := func() {
		for _, id := range ids {
			waitIdle(t, r.a, id)
		}
	}
	fork(parent)
	settle()
	r.a.Backend.Nodes()

	before := store.TopologyBuildsForTest()
	for range 3 {
		fork(parent)
	}
	settle()
	require.Zero(t, store.TopologyBuildsForTest()-before, "a fork by id rebuilt the topology snapshot")

	r.a.Backend.Nodes()
	before = store.TopologyBuildsForTest()
	for range 3 {
		fork(handle.Words(parent, handle.MaxWords))
	}
	settle()
	require.Zero(t, store.TopologyBuildsForTest()-before, "a fork by handle rebuilt the topology snapshot")
}
