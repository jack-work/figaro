package xwal

import (
	"path/filepath"
	"testing"
)

// --- unit: the fold ---

func TestMap_DeepSetCreatesPath(t *testing.T) {
	p, err := MapSetPatch([]string{"system", "tags", "42"}, []byte(`{"cache":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := reduceMap([]byte("{}"), p)
	if err != nil {
		t.Fatal(err)
	}
	if string(st) != `{"system":{"tags":{"42":{"cache":"x"}}}}` {
		t.Fatalf("deep set = %s", st)
	}
}

func TestMap_LeafSetDoesNotClobberSiblings(t *testing.T) {
	st := []byte(`{"system":{"a":1,"b":2}}`)
	p, _ := MapSetPatch([]string{"system", "b"}, []byte(`9`))
	out, err := reduceMap(st, p)
	if err != nil {
		t.Fatal(err)
	}
	// a preserved, b updated — the whole subtree is NOT rewritten.
	if string(out) != `{"system":{"a":1,"b":9}}` {
		t.Fatalf("leaf set clobbered siblings: %s", out)
	}
}

func TestMap_Remove(t *testing.T) {
	st := []byte(`{"system":{"a":1,"b":2},"mantra":"x"}`)
	p, _ := MapRemovePatch([]string{"system", "a"})
	out, err := reduceMap(st, p)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"mantra":"x","system":{"b":2}}` {
		t.Fatalf("remove = %s", out)
	}
}

func TestMap_EmptyPatchIsNoOp(t *testing.T) {
	st := []byte(`{"a":1}`)
	out, err := reduceMap(st, []byte("{}"))
	if err != nil || string(out) != `{"a":1}` {
		t.Fatalf("empty patch not a no-op: %s err=%v", out, err)
	}
}

func TestMap_BigIntPrecision(t *testing.T) {
	p, _ := MapSetPatch([]string{"n"}, []byte(`9007199254740993`)) // > 2^53
	out, err := reduceMap([]byte("{}"), p)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"n":9007199254740993}` {
		t.Fatalf("int precision lost: %s", out)
	}
}

func TestMap_SetValidatesJSON(t *testing.T) {
	if _, err := MapSetPatch([]string{"a"}, []byte(`not json`)); err == nil {
		t.Fatal("expected invalid-JSON error")
	}
	if _, err := MapSetPatch(nil, []byte(`1`)); err == nil {
		t.Fatal("expected empty-path error")
	}
}

// --- integration: native "map" reducer via a reducible channel + fork ---

func mapTrunksCfg() Config {
	return Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "chalkboard", Kind: ChannelReducible, Reducer: MapReducerName}, // built-in, no registration
		},
		SegmentSize: 4096,
	}
}

// seedMapTrunk seeds a store (map reducer cfg) with a birthless stump and a
// trunk under it (own range from LT 2), the migration of the old root-trunk.
func seedMapTrunk(t *testing.T, dir string) (*Trunks, TrunkID) {
	t.Helper()
	f, err := createTrunks(dir, mapTrunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("s"); err != nil {
		t.Fatal(err)
	}
	id, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	return f, id
}

func TestMap_BuiltinReducerNoRegistration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	// Note: cfg.Registry is nil — "map" must resolve from the built-ins.
	f, root := seedMapTrunk(t, dir)
	_, lt, _ := f.Append(root, 0, []byte(`"u1"`), nil)
	sp, _ := MapSetPatch([]string{"system", "provider"}, []byte(`"anthropic"`))
	if _, err := f.AppendChannel(root, "chalkboard", lt, sp, nil); err != nil {
		t.Fatal(err)
	}
	mp, _ := MapSetPatch([]string{"mantra"}, []byte(`"root thread"`))
	if _, err := f.AppendChannel(root, "chalkboard", lt, mp, nil); err != nil {
		t.Fatal(err)
	}
	x, _ := f.Head(root)
	defer x.Close()
	var last uint64
	for _, c := range x.Channels() {
		if c.Name == "chalkboard" {
			last = c.Last
		}
	}
	st, err := x.StateAt("chalkboard", last)
	if err != nil {
		t.Fatal(err)
	}
	if string(st) != `{"mantra":"root thread","system":{"provider":"anthropic"}}` {
		t.Fatalf("folded state = %s", st)
	}
}

func TestMap_ForksAlongDeep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedMapTrunk(t, dir)
	_, lt, _ := f.Append(root, 0, []byte(`"u1"`), nil)
	sp, _ := MapSetPatch([]string{"system", "model"}, []byte(`"opus"`))
	f.AppendChannel(root, "chalkboard", lt, sp, nil)
	f.Append(root, 0, []byte(`"a1"`), nil) // tail 3

	// interior fork at :2 -> alt inherits system.model, sets a deep leaf
	alt, _ := forkAppend(t, f, root, 2, []byte(`"alt"`))
	ap, _ := MapSetPatch([]string{"system", "tags", "42"}, []byte(`{"cache":"ephemeral"}`))
	f.AppendChannel(alt, "chalkboard", 0, ap, nil)

	ax, _ := f.Head(alt)
	defer ax.Close()
	var al uint64
	for _, c := range ax.Channels() {
		if c.Name == "chalkboard" {
			al = c.Last
		}
	}
	st, _ := ax.StateAt("chalkboard", al)
	// alt keeps inherited system.model AND its own deep tags leaf.
	if string(st) != `{"system":{"model":"opus","tags":{"42":{"cache":"ephemeral"}}}}` {
		t.Fatalf("alt deep-fork state = %s", st)
	}
}
