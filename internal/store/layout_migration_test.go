package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figwal/xwal"
)

// The seam this covers: figaro migrates a store BEFORE it opens it. Nothing
// in figwal's own suite proves that the daemon's open path does it, and the
// failure it prevents is the quiet one -- a store that opens reporting its
// outfits and none of its arias.

func seedForkedArias(t *testing.T, root string) (ids []string, boards map[string]string) {
	t.Helper()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	outfit, err := be.CreateOutfit("mig", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	aria, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	// A chain of interior forks, so the store nests several levels deep and
	// every child inherits a prefix of its parent's timeline.
	for gen := 0; gen < 3; gen++ {
		ids = append(ids, aria)
		lg, err := be.Open(aria)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			m := message.Message{Role: message.RoleInput, Content: []message.Content{
				{Type: message.ContentProse, Text: fmt.Sprintf("gen%d msg%d", gen, i)},
			}}
			if _, err := lg.Append(Entry[message.Message]{Payload: m}); err != nil {
				t.Fatal(err)
			}
		}
		patch := message.Patch{Set: map[string]json.RawMessage{
			"gen": json.RawMessage(fmt.Sprintf("%d", gen)),
		}}
		if _, err := be.ApplyChalkboard(aria, patch); err != nil {
			t.Fatal(err)
		}
		_, alt, err := be.ForkAt(aria, 2)
		if err != nil {
			t.Fatal(err)
		}
		aria = alt
	}
	ids = append(ids, aria)
	boards = map[string]string{}
	for _, id := range ids {
		boards[id] = boardOf(t, be, id)
	}
	sort.Strings(ids)
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	return ids, boards
}

func boardOf(t *testing.T, be *XwalBackend, id string) string {
	t.Helper()
	snap, err := be.ChalkboardState(id)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k, v := range snap.All() {
		keys = append(keys, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func TestOpenMigratesANestedStoreAndFindsEveryAria(t *testing.T) {
	root := t.TempDir()
	ids, boards := seedForkedArias(t, root)
	nestStore(t, root)

	// Belt and braces: the store really is in the old shape, and figwal
	// really does refuse it. Without this the test could pass on a store
	// that never needed migrating.
	if need, err := xwal.NeedsFlatten(root); err != nil || !need {
		t.Fatalf("fixture does not need migrating (need=%v err=%v)", need, err)
	}
	// And it is really NESTED, not merely unstamped: three generations of
	// interior forks, in every channel.
	if n, err := xwal.NestedNodes(root); err != nil || n < 3 {
		t.Fatalf("fixture has %d nested node dirs (err %v); it should have many", n, err)
	}
	if _, err := xwal.OpenStore(root, storeOptions(0)); err == nil {
		t.Fatal("figwal opened an unmigrated store")
	}

	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatalf("open did not migrate: %v", err)
	}
	defer be.Close()

	var got []string
	for _, n := range be.Conversations() {
		got = append(got, n.ID)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Fatalf("arias after migration:\n got  %v\n want %v", got, ids)
	}
	for _, id := range ids {
		if b := boardOf(t, be, id); b != boards[id] {
			t.Errorf("aria %s chalkboard after migration: got %q want %q", id, b, boards[id])
		}
		lg, err := be.Open(id)
		if err != nil {
			t.Fatalf("aria %s: %v", id, err)
		}
		if entries := lg.ReadFrom(0, 0); len(entries) == 0 {
			t.Errorf("aria %s reads empty after migration", id)
		}
	}
}

// nestStore rewrites a flat store into the pre-migration shape: lineage
// becomes the directory path and the marker becomes the legacy .trunk. It is
// the inverse of the migration, and exists so the test can start from a
// store that was really written rather than one hand-built.
func nestStore(t *testing.T, root string) {
	t.Helper()
	main := "ir"
	chans := channelDirs(t, root)
	from, trunk := map[string]string{}, map[string]string{}
	ents, err := os.ReadDir(filepath.Join(root, main))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, main, e.Name(), ".node"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch k {
			case "from":
				from[e.Name()] = v
			case "trunk":
				trunk[e.Name()] = v
			}
		}
	}
	nested := map[string]string{}
	var pathOf func(string) string
	pathOf = func(k string) string {
		if p, ok := nested[k]; ok {
			return p
		}
		p := k
		if parent := from[k]; parent != "" {
			p = filepath.Join(pathOf(parent), k)
		}
		nested[k] = p
		return p
	}
	keys := make([]string, 0, len(from))
	for k := range from {
		keys = append(keys, k)
		pathOf(k)
	}
	// Shallowest first, so a node moves into a parent already in place.
	sort.Slice(keys, func(i, j int) bool {
		di := strings.Count(nested[keys[i]], string(os.PathSeparator))
		dj := strings.Count(nested[keys[j]], string(os.PathSeparator))
		if di != dj {
			return di < dj
		}
		return keys[i] < keys[j]
	})
	for _, ch := range chans {
		for _, k := range keys {
			src := filepath.Join(root, ch, k)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if ch == main {
				if err := os.Remove(filepath.Join(src, ".node")); err != nil {
					t.Fatal(err)
				}
				if trunk[k] != "" {
					if err := os.WriteFile(filepath.Join(src, ".trunk"), []byte(trunk[k]+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if nested[k] == k {
				continue
			}
			if err := os.Rename(src, filepath.Join(root, ch, nested[k])); err != nil {
				t.Fatal(err)
			}
		}
	}
	stripLayoutStamp(t, root)
	if err := os.Remove(filepath.Join(root, ".unclean")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func channelDirs(t *testing.T, root string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "xwal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Channels []struct {
			Name string `json:"name"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	out := []string{"ir"}
	for _, c := range man.Channels {
		if c.Name != "ir" {
			out = append(out, c.Name)
		}
	}
	return out
}

func stripLayoutStamp(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "xwal.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "layout")
	delete(m, "layout_from")
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
