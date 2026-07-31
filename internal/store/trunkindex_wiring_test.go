package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The store must actually use the maintained index, not figwal's default. The
// evidence is topology.json: it exists only because trunkindex persisted it,
// and it must name every conversation that was created.
//
// Without this the whole change is invisible. Everything still passes on
// figwal's marker walk, just slowly, which is precisely the failure mode this
// work exists to remove.
func TestStoreUsesTheMaintainedTopologyIndex(t *testing.T) {
	root := t.TempDir()
	st, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	l, err := st.CreateLoadout("d", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	var convs []string
	for range 4 {
		c, err := st.CreateConversation(l)
		if err != nil {
			t.Fatal(err)
		}
		convs = append(convs, c)
	}
	if err := st.Close(); err != nil { // Close flushes the background writer
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(root, "topology.json"))
	if err != nil {
		t.Fatalf("no topology.json: the maintained index was never wired in: %v", err)
	}
	for _, id := range convs {
		if !contains(string(b), id) {
			t.Fatalf("topology.json does not know conversation %s", id)
		}
	}

	// And it survives a reopen: the index is a cache, but a durable one, so a
	// restart must not have to re-walk the forest to answer anything.
	st2, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	seen := map[string]bool{}
	for _, n := range st2.Nodes() {
		seen[n.ID] = true
	}
	for _, id := range convs {
		if !seen[id] {
			t.Fatalf("conversation %s missing after reopen", id)
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
