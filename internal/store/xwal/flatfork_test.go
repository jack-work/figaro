package xwal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fork must not mutate the parent's topology.
func TestForkDoesNotMutateParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	headKey, _ := f.idx.Head(trunk)
	parentDir := f.irDir(headKey)
	subdirs := func() []string {
		ents, err := os.ReadDir(parentDir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range ents {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
		return out
	}
	before := subdirs()

	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if after := subdirs(); len(after) != len(before) {
		t.Fatalf("fork nested a child under the parent: %v -> %v", before, after)
	}

	// The parent keeps its id and its head.
	if k, ok := f.idx.Head(trunk); !ok || k != headKey {
		t.Fatalf("parent head moved: %q -> %q", headKey, k)
	}
	// The alternative is a sibling, one level deep.
	altKey, ok := f.idx.Head(alt)
	if !ok {
		t.Fatal("no head for the alternative")
	}
	n, _ := f.idx.Node(altKey)
	if strings.Contains(altKey, "/") {
		t.Fatalf("alternative is nested: key=%q", altKey)
	}
	if n.From != headKey {
		t.Fatalf("alternative lineage = %q, want %q", n.From, headKey)
	}
}

// A flat child reads the shared prefix through the index, not the directory.
func TestFlatForkInheritsPrefix(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	for i := range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"t`+string(rune('0'+i))+`"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	x, err := f.Head(alt)
	if err != nil {
		t.Fatalf("open flat child: %v", err)
	}
	defer x.Close()
	if got := mainTail(x); got < 3 {
		t.Fatalf("child tail = %d, want >= 3: the prefix is not visible", got)
	}
	if _, err := x.chans[f.main].log.Read(2); err != nil {
		t.Fatalf("child cannot read shared LT 2: %v", err)
	}
}

// A fork below a node's own first index must fork the ancestor that owns
// that LT. Walking the directory branch cannot find it: every flat node is
// depth-1, so the walk falls through to the root and the child ends up
// numbering above a hole in its own prefix.
func TestResplitBelowForksTheOwner(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	for range 6 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := f.idx.Head(trunk)
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	const at = 3
	deep, err := f.ForkAt(alt, at)
	if err != nil {
		t.Fatalf("fork at %d: %v", at, err)
	}
	key, _ := f.idx.Head(deep)
	n, _ := f.idx.Node(key)
	if n.From != owner {
		t.Errorf("re-split lineage = %q, want the owner %q", n.From, owner)
	}
	x, err := f.Head(deep)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	defer x.Close()
	for i := uint64(1); i <= at; i++ {
		if _, err := x.chans[f.main].log.Read(i); err != nil {
			t.Fatalf("shared prefix LT %d unreadable: %v", i, err)
		}
	}
}

// Reopening a flat store must not walk the directory tree as if it were
// the lineage. A reducible channel's fork base belongs to a sibling
// parent, so the open-time repair resolved it against the root and failed.
func TestFlatStoreReopens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	for range 4 {
		_, lt, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.AppendChannel(string(trunk), "chalkboard", lt, []byte(`{"a":1}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.ForkTail(trunk); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	g.Close()
}

// A .from that points at itself must be an error, not a hung daemon.
func TestOwnerOfRefusesALineageCycle(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	head, _ := f.idx.Head(trunk)
	stump := f.idx.ParentOf(head)
	n, _ := f.idx.Node(stump)
	cycled := *n
	cycled.From = stump
	f.idx.mu.Lock()
	f.idx.nodes[stump] = &cycled
	f.idx.mu.Unlock()

	if _, err := f.ownerOf(head, 1); err == nil {
		t.Fatal("a self-referencing lineage climbed clean")
	}
}

// A fork does not freeze the parent, so a rebuild from disk must not
// demote it. The incremental path keeps the parent's head; the walk used
// the nested rule "a node with subdirectories is frozen" and dropped it.
func TestRebuildKeepsAForkedParentLive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	head, _ := f.idx.Head(trunk)
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	cleanupTrunks(t, g)
	if k, ok := g.idx.Head(trunk); !ok || k != head {
		t.Errorf("parent head after rebuild = %q (ok=%v), want %q", k, ok, head)
	}
	if _, ok := g.idx.Head(alt); !ok {
		t.Errorf("child trunk %q has no head after rebuild", alt)
	}
}

// lineage climbed n.Parent, which no flat writer sets, so every trunk
// reported no parent and no stump.
func TestLineageReadsTheFlatParent(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	if _, stump, _ := f.lineage(string(trunk)); stump != "s" {
		t.Errorf("trunk under stump: stump = %q, want %q", stump, "s")
	}
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	parent, _, bl := f.lineage(string(alt))
	if parent != string(trunk) {
		t.Errorf("forked trunk: parent = %q, want %q", parent, trunk)
	}
	if bl == 0 {
		t.Error("forked trunk: branched-at LT = 0")
	}
}

// The bottom line: an aria forks itself without blocking any other aria.
// A flat fork writes only its own new sibling, so a head open elsewhere
// must not delay it. Asserted by COUNT, never by duration: we hold real
// heads open and require the fork to succeed, where the old global gate
// spun until it timed out.
func TestForkDoesNotWaitOnOtherOpenHeads(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	others := make([]TrunkID, 0, 3)
	for range 3 {
		id, err := f.SpawnUnderRoot()
		if err != nil {
			t.Fatal(err)
		}
		others = append(others, id)
	}
	// Hold every other aria's head open, as a live daemon does.
	for _, id := range others {
		x, err := f.Head(id)
		if err != nil {
			t.Fatal(err)
		}
		defer x.Close()
	}
	if _, err := f.ForkTail(trunk); err != nil {
		t.Fatalf("fork blocked by %d open heads: %v", len(others), err)
	}
	if _, err := f.SpawnUnderRoot(); err != nil {
		t.Fatalf("spawn blocked by open heads: %v", err)
	}
}

// A related channel is sparse: most main LTs have no record of their own.
// An interior fork at such an LT must still inherit everything keyed below
// it. Taking the boundary from an EXACT main-LT match instead made the
// child claim base 1, severing the channel and dropping inherited state.
func TestInteriorForkInheritsSparseChannel(t *testing.T) {
	f, trunk := seedMapTrunk(t, filepath.Join(t.TempDir(), "f"))
	_, lt, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := MapSetPatch([]string{"model"}, []byte(`"m"`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AppendChannel(string(trunk), "chalkboard", lt, patch, nil); err != nil {
		t.Fatal(err)
	}
	// Three more turns with NO chalkboard record, so the fork point below
	// is a main LT the related channel never keyed.
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	at := mainTail(x) - 1
	x.Close()

	alt, err := f.ForkAt(trunk, at)
	if err != nil {
		t.Fatalf("fork at %d: %v", at, err)
	}
	ax, err := f.Head(alt)
	if err != nil {
		t.Fatal(err)
	}
	defer ax.Close()
	for _, c := range ax.Channels() {
		if c.Name != "chalkboard" {
			continue
		}
		if c.Last == 0 {
			t.Fatalf("alt chalkboard is empty: it lost the inherited patch")
		}
		st, err := ax.StateAt("chalkboard", c.Last)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(st), `"model":"m"`) {
			t.Fatalf("alt chalkboard state = %s, want the inherited model", st)
		}
	}
}

// A fork sharing main [1..at] must not inherit a RELATED record keyed
// above at. Such a record references a turn the child does not have, which
// the crash harness reports as [orphan-related] -- and it needs no crash
// to produce, only a related channel with records on both sides of the
// fork point.
func TestInteriorForkDropsRelatedRecordsPastTheForkPoint(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	var lts []uint64
	for range 6 {
		_, lt, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		lts = append(lts, lt)
		if _, err := f.AppendChannel(string(trunk), "translations", lt, []byte(`"note"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	at := lts[2] // fork in the middle: notes exist above and below
	alt, err := f.ForkAt(trunk, at)
	if err != nil {
		t.Fatalf("fork at %d: %v", at, err)
	}
	x, err := f.Head(alt)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	ch := x.chans["translations"]
	last := ch.log.LastIndex()
	if last == 0 {
		return // nothing inherited at all is fine
	}
	if err := ch.log.Range(1, func(idx uint64, payload []byte) error {
		m, derr := decodeMainLT(payload)
		if derr != nil {
			return derr
		}
		if m > at {
			t.Errorf("related record at %d references main %d, past the fork point %d", idx, m, at)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A related record may be keyed AHEAD of main: a note written for a turn
// whose main record is not durable yet. A tail fork must not hand that
// record to the child, which would then hold a note referencing a turn it
// does not have -- the crash harness's [orphan-related], and it needs no
// crash to produce.
func TestTailForkDropsRelatedRecordsAheadOfMain(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	var lts []uint64
	for range 3 {
		_, lt, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		lts = append(lts, lt)
	}
	// A note for a turn that does not exist yet.
	ahead := lts[len(lts)-1] + 5
	if _, err := f.AppendChannel(string(trunk), "translations", ahead, []byte(`"ahead"`), nil); err != nil {
		t.Fatal(err)
	}
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	x, err := f.Head(alt)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	mainTailLT := mainTail(x)
	ch := x.chans["translations"]
	if err := ch.log.Range(1, func(idx uint64, payload []byte) error {
		m, derr := decodeMainLT(payload)
		if derr != nil {
			return derr
		}
		if m > mainTailLT {
			t.Errorf("inherited note at %d references main %d, past the child's main tail %d", idx, m, mainTailLT)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A store written before the markers merged is REFUSED, and Flatten is what
// carries it across: every node keeps its from, kind and trunk, and the
// pair is retired. The upgrade used to happen lazily on the read path; old
// formats now live only in the migration.
func TestLegacyMarkersMigrateWithFlatten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	for range 3 {
		if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]NodeInfo{}
	for k, n := range f.Nodes() {
		before[k] = n
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewind every node to the old pair.
	ents, err := os.ReadDir(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	rewound := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		nd := filepath.Join(dir, "ir", e.Name())
		n, ok := readNodeMarker(nd)
		if !ok {
			continue
		}
		if err := os.WriteFile(filepath.Join(nd, ".from"),
			[]byte(n.from+"\n"+n.kind+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if n.trunk != "" {
			if err := os.WriteFile(filepath.Join(nd, ".trunk"), []byte(n.trunk+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(filepath.Join(nd, ".node")); err != nil {
			t.Fatal(err)
		}
		rewound++
	}
	if rewound == 0 {
		t.Fatal("rewound no nodes; the test proves nothing")
	}

	unstampManifest(t, dir)
	if _, err := openTrunks(dir, trunksCfg()); !errors.Is(err, ErrLegacyLayout) {
		t.Fatalf("open of a legacy store: got %v, want ErrLegacyLayout", err)
	}
	if _, err := Flatten(dir); err != nil {
		t.Fatal(err)
	}

	g, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatalf("migrated store failed to open: %v", err)
	}
	cleanupTrunks(t, g)
	after := g.Nodes()
	if len(after) != len(before) {
		t.Fatalf("migrated open sees %d nodes, want %d", len(after), len(before))
	}
	for k, want := range before {
		got, ok := after[k]
		if !ok {
			t.Fatalf("node %q lost", k)
		}
		if got.From != want.From || got.Kind != want.Kind || got.Trunk != want.Trunk {
			t.Fatalf("node %q: got %+v, want %+v", k, got, want)
		}
	}
	if _, ok := g.idx.Head(alt); !ok {
		t.Errorf("forked trunk %s lost its head across the migration", alt)
	}
	// One identity file per node afterwards, so no later reader has two to
	// disagree about.
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		nd := filepath.Join(dir, "ir", e.Name())
		if _, err := os.Stat(filepath.Join(nd, ".node")); err != nil {
			t.Errorf("node %q not migrated: %v", e.Name(), err)
		}
		for _, legacy := range []string{".from", ".trunk"} {
			if _, err := os.Stat(filepath.Join(nd, legacy)); !os.IsNotExist(err) {
				t.Errorf("node %q kept %s (err=%v)", e.Name(), legacy, err)
			}
		}
	}
}
