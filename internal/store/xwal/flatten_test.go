package xwal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The fixture is built with the real API and then UN-flattened, so the
// nested store under test holds records a v3 store would hold rather than
// bytes a test author imagined. What Flatten must restore is then a
// comparison against a state that was really produced, not asserted.

type treeSnapshot struct {
	trunks map[string]uint64            // trunk id -> main tail
	main   map[string][]string          // trunk id -> ir payloads, in order
	board  map[string]string            // trunk id -> reduced chalkboard state
	bases  map[string]map[string]uint64 // channel -> node leaf -> .fork base
}

func buildNestedFixture(t *testing.T) (dir string, before treeSnapshot) {
	t.Helper()
	dir = t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateStump("config@abc"); err != nil {
		t.Fatal(err)
	}
	// A chain, so the fixture has depth: each conversation forks the one
	// before it, which in v3 means each nests inside the last.
	var chain []string
	prev, err := s.SpawnUnderStump("config@abc")
	if err != nil {
		t.Fatal(err)
	}
	chain = append(chain, string(prev))
	for i := 0; i < 3; i++ {
		var last uint64
		for j := 0; j < 3; j++ {
			last, err = s.Append(string(prev), "ir", 0, fmt.Appendf(nil, `{"t":%d,"i":%d}`, i, j), nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		// A REAL patch: MapReducer folds MapPatch, not a bare object, so a
		// raw `{"depth":N}` is a no-op and every board assertion built on
		// one compares "{}" to "{}".
		patch, perr := MapSetPatch([]string{fmt.Sprintf("depth%d", i)}, []byte(`"set"`))
		if perr != nil {
			t.Fatal(perr)
		}
		if _, err := s.Append(string(prev), "chalkboard", 0, patch, nil); err != nil {
			t.Fatal(err)
		}
		// INTERIOR of prev's OWN records, so the child nests under prev
		// rather than beside it, and inherits a prefix. Forking at the tail,
		// or below a node's own base, is a different code path and would
		// have produced a flat fixture that proves nothing.
		next, err := s.Fork(string(prev), last-1)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, next)
		prev = TrunkID(next)
	}
	// One conversation straight off the root, the n161 case: already at
	// depth 1, already flat, and it must not be moved.
	rooted, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(rooted), "ir", 0, []byte(`{"rooted":1}`), nil); err != nil {
		t.Fatal(err)
	}
	before = snapshotTree(t, s, dir)
	folded := false
	for _, b := range before.board {
		if b != "" && b != "{}" {
			folded = true
		}
	}
	if !folded {
		t.Fatal("no aria's board folded to anything: every board assertion in this file would be vacuous")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	nest(t, dir)
	return dir, before
}

func snapshotTree(t *testing.T, s *Store, dir string) treeSnapshot {
	t.Helper()
	snap := treeSnapshot{
		trunks: map[string]uint64{},
		main:   map[string][]string{},
		board:  map[string]string{},
	}
	for _, ti := range s.List() {
		snap.trunks[ti.ID] = ti.Tip
		recs, err := s.RecordsFrom(ti.ID, "ir", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			snap.main[ti.ID] = append(snap.main[ti.ID], canonJSON(t, r.Payload))
		}
		state, err := chalkboardState(t, s, ti.ID)
		if err != nil {
			t.Fatal(err)
		}
		snap.board[ti.ID] = state
	}
	snap.bases = forkBases(t, dir)
	return snap
}

// canonJSON re-marshals a payload with sorted keys. A record read from
// memory and the same record read back from a segment differ in key order
// alone, and comparing the raw bytes would have made this suite fail for a
// reason that has nothing to do with the migration.
func canonJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// chalkboardState reduces a trunk's board at its own tail. The reduced
// state is what a moved watermark would break, so it is worth reading
// rather than the raw patches.
func chalkboardState(t *testing.T, s *Store, trunk string) (string, error) {
	t.Helper()
	chans, err := s.Channels(trunk)
	if err != nil {
		return "", err
	}
	for _, c := range chans {
		if c.Name != "chalkboard" || c.Last == 0 {
			continue
		}
		state, err := s.StateAt(trunk, "chalkboard", c.Last)
		return canonJSON(t, state), err
	}
	return "", nil
}

// forkBases records every node's .fork base, keyed by LEAF name so it
// survives the move. An LT does not change when a directory moves; this is
// the assertion that says so.
func forkBases(t *testing.T, dir string) map[string]map[string]uint64 {
	t.Helper()
	chans, _, _, err := flattenManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]uint64{}
	for _, ch := range chans {
		out[ch] = map[string]uint64{}
		paths, err := nodePaths(filepath.Join(dir, ch))
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range paths {
			base, err := readForkBaseFile(filepath.Join(dir, ch, rel, ".fork"))
			if err != nil {
				continue // the "owns its log from 1" case; nothing to compare
			}
			out[ch][filepath.Base(rel)] = base
		}
	}
	return out
}

// nest turns a v4 store back into the v3 shape: lineage becomes the path,
// .node becomes the legacy .trunk, and the manifest loses its layout stamp.
// Test-only, and deliberately the exact inverse of what Flatten claims to
// do, so a round trip has something to be equal to.
func nest(t *testing.T, dir string) {
	t.Helper()
	chans, main, _, err := flattenManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(dir, main)
	from, trunk := map[string]string{}, map[string]string{}
	ents, err := os.ReadDir(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m, ok := readNodeMarker(filepath.Join(mainDir, e.Name()))
		if !ok {
			t.Fatalf("fixture node %q has no .node", e.Name())
		}
		from[e.Name()], trunk[e.Name()] = m.from, m.trunk
	}
	// Nested path of a node is its whole lineage joined.
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
	// Shallowest first: a node moves into a parent that is already in place.
	sort.Slice(keys, func(i, j int) bool {
		di, dj := pathDepth(nested[keys[i]]), pathDepth(nested[keys[j]])
		if di != dj {
			return di < dj
		}
		return keys[i] < keys[j]
	})
	for _, ch := range chans {
		for _, k := range keys {
			src := filepath.Join(dir, ch, k)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if ch == main {
				if err := os.Remove(filepath.Join(src, nodeMarkerName)); err != nil {
					t.Fatal(err)
				}
				if trunk[k] != "" {
					if err := os.WriteFile(filepath.Join(src, legacyTrunkName), []byte(trunk[k]+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if nested[k] == k {
				continue
			}
			if err := os.Rename(src, filepath.Join(dir, ch, nested[k])); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "layout")
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, uncleanName)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestFlattenRestoresANestedTree(t *testing.T) {
	dir, before := buildNestedFixture(t)

	nestedCount, err := NestedNodes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if nestedCount == 0 {
		t.Fatal("fixture is not nested; the test would prove nothing")
	}

	rep, err := Flatten(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Markers != rep.Nodes {
		t.Errorf("markers %d for %d nodes: every node needs one", rep.Markers, rep.Nodes)
	}
	if rep.Moved == 0 {
		t.Error("nothing moved")
	}
	if left, err := NestedNodes(dir); err != nil || left != 0 {
		t.Fatalf("after flatten: %d nested nodes left (err %v)", left, err)
	}

	// The bases are the claim worth pinning: a move must not change one.
	if after := forkBases(t, dir); !equalBases(before.bases, after) {
		t.Errorf("fork bases changed\n before %v\n after  %v", before.bases, after)
	}

	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer s.Close()
	after := snapshotTree(t, s, dir)
	if len(after.trunks) != len(before.trunks) {
		t.Fatalf("trunks after migration: %d, before: %d", len(after.trunks), len(before.trunks))
	}
	for id, tip := range before.trunks {
		if got, ok := after.trunks[id]; !ok || got != tip {
			t.Errorf("trunk %s: tip %d (ok %v), want %d", id, got, ok, tip)
		}
		if got := strings.Join(after.main[id], "|"); got != strings.Join(before.main[id], "|") {
			t.Errorf("trunk %s ir records:\n got  %s\n want %s", id, got, strings.Join(before.main[id], "|"))
		}
		if after.board[id] != before.board[id] {
			t.Errorf("trunk %s chalkboard: got %s want %s", id, after.board[id], before.board[id])
		}
	}
}

func TestFlattenIsIdempotent(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	first, err := Flatten(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Flatten(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Moved != 0 || second.Markers != 0 {
		t.Errorf("second run did work: moved %d markers %d (first: moved %d markers %d)",
			second.Moved, second.Markers, first.Moved, first.Markers)
	}
}

// A migration interrupted in the main channel must resume: the node whose
// marker was written but whose rename never happened is the state the
// ordering exists to make safe.
func TestFlattenResumesAfterAMarkerWithoutItsRename(t *testing.T) {
	dir, before := buildNestedFixture(t)
	plan, err := PlanFlatten(dir)
	if err != nil {
		t.Fatal(err)
	}
	main := plan.Main
	wrote := 0
	for _, cp := range plan.Channels {
		if cp.Name != main {
			continue
		}
		for _, n := range cp.Nodes {
			if n.Marker == nil || pathDepth(n.Rel) == 1 {
				continue
			}
			if err := writeNodeMarker(filepath.Join(dir, main, n.Rel), *n.Marker); err != nil {
				t.Fatal(err)
			}
			wrote++
			break // exactly one node half-done, then die
		}
	}
	if wrote != 1 {
		t.Fatalf("planted %d partial markers, want 1", wrote)
	}
	if _, err := Flatten(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if left, err := NestedNodes(dir); err != nil || left != 0 {
		t.Fatalf("after the resumed migration: %d nested nodes left (err %v)", left, err)
	}
	after := snapshotTree(t, s, dir)
	if len(after.trunks) != len(before.trunks) {
		t.Fatalf("trunks after the resumed migration: %d, want %d", len(after.trunks), len(before.trunks))
	}
	for id := range before.trunks {
		if strings.Join(after.main[id], "|") != strings.Join(before.main[id], "|") {
			t.Errorf("trunk %s lost records across the resumed migration", id)
		}
	}
}

// The main channel is migrated FIRST and the others follow, so an
// interrupted run leaves a store whose main channel is flat and whose
// chalkboard is still nested. That state must resume. It is also the state
// in which a naive divergence check refuses its own unfinished work.
func TestFlattenResumesWithOnlyTheMainChannelMigrated(t *testing.T) {
	dir, before := buildNestedFixture(t)
	plan, err := PlanFlatten(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range plan.Channels {
		if cp.Name != plan.Main {
			continue
		}
		for _, n := range cp.Nodes {
			src := filepath.Join(dir, plan.Main, n.Rel)
			if n.Marker != nil {
				if err := writeNodeMarker(src, *n.Marker); err != nil {
					t.Fatal(err)
				}
				_ = os.Remove(filepath.Join(src, legacyTrunkName))
			}
			if n.Rel == n.Flat {
				continue
			}
			if err := os.Rename(src, filepath.Join(dir, plan.Main, n.Flat)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if left, err := NestedNodes(dir); err != nil || left == 0 {
		t.Fatalf("half-migrated fixture has %d nested nodes (err %v); it should still have some", left, err)
	}

	if _, err := Flatten(dir); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if left, err := NestedNodes(dir); err != nil || left != 0 {
		t.Fatalf("after resume: %d nested nodes left (err %v)", left, err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := snapshotTree(t, s, dir)
	if len(after.trunks) != len(before.trunks) {
		t.Fatalf("trunks after resume: %d, want %d", len(after.trunks), len(before.trunks))
	}
	for id := range before.trunks {
		if after.board[id] != before.board[id] {
			t.Errorf("trunk %s chalkboard after resume: got %q want %q", id, after.board[id], before.board[id])
		}
	}
}

// A channel nested under a parent the main channel does not claim is a real
// divergence: its .fork base is an index in THAT parent's numbering, so
// flattening would re-home its data.
func TestFlattenRefusesAChannelNestedUnderADifferentParent(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	paths, err := nodePaths(filepath.Join(dir, "chalkboard"))
	if err != nil {
		t.Fatal(err)
	}
	var deepest string
	for _, p := range paths {
		if pathDepth(p) > pathDepth(deepest) {
			deepest = p
		}
	}
	if pathDepth(deepest) < 3 {
		t.Fatalf("fixture is too shallow: deepest chalkboard node %q", deepest)
	}
	// Re-home it one level up: same leaf, a grandparent for a parent.
	moved := filepath.Join(filepath.Dir(filepath.Dir(deepest)), filepath.Base(deepest))
	if err := os.Rename(filepath.Join(dir, "chalkboard", deepest), filepath.Join(dir, "chalkboard", moved)); err != nil {
		t.Fatal(err)
	}
	shapeBefore := shapeOf(t, dir)
	_, err = Flatten(dir)
	if err == nil || !strings.Contains(err.Error(), "lineage says") {
		t.Fatalf("expected a divergence refusal, got %v", err)
	}
	if got := shapeOf(t, dir); got != shapeBefore {
		t.Error("a refused migration moved something")
	}
}

func TestFlattenRefusesOnALeafNameCollisionAndMovesNothing(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	// Give two nodes the same leaf name in the main channel: copy one
	// stump-level node's name into a deeper position.
	paths, err := nodePaths(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	var deep string
	for _, p := range paths {
		if pathDepth(p) >= 3 {
			deep = p
			break
		}
	}
	if deep == "" {
		t.Fatal("fixture has no node at depth 3")
	}
	clash := filepath.Join(filepath.Dir(deep), filepath.Base(paths[0]))
	if err := os.MkdirAll(filepath.Join(dir, "ir", clash), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ir", clash, ".fork"), []byte("base=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	shapeBefore := shapeOf(t, dir)

	_, err = Flatten(dir)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "flatten") || !strings.Contains(err.Error(), filepath.Base(paths[0])) {
		t.Errorf("refusal does not name the collision: %v", err)
	}
	if got := shapeOf(t, dir); got != shapeBefore {
		t.Errorf("a refused migration moved something:\n got  %s\n want %s", got, shapeBefore)
	}
}

func TestFlattenRefusesANodeWhoseBaseIsOnlyItsNesting(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	paths, err := nodePaths(filepath.Join(dir, "chalkboard"))
	if err != nil {
		t.Fatal(err)
	}
	var victim string
	for _, p := range paths {
		if pathDepth(p) >= 2 {
			victim = p
			break
		}
	}
	if victim == "" {
		t.Fatal("fixture has no nested chalkboard node")
	}
	// No .fork and no segment at index 1: the base was the nesting, and
	// after a move it would be derived from the root instead.
	if err := os.Remove(filepath.Join(dir, "chalkboard", victim, ".fork")); err != nil {
		t.Fatal(err)
	}
	shapeBefore := shapeOf(t, dir)
	_, err = Flatten(dir)
	if err == nil || !strings.Contains(err.Error(), "no .fork") {
		t.Fatalf("expected a refusal naming the missing .fork, got %v", err)
	}
	if got := shapeOf(t, dir); got != shapeBefore {
		t.Errorf("a refused migration moved something")
	}
}

func TestFlattenStampsTheLayoutOnlyWhenItFinishes(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	m, err := readManifestFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Layout != 0 {
		t.Fatalf("nested fixture claims layout %d", m.Layout)
	}
	if _, err := Flatten(dir); err != nil {
		t.Fatal(err)
	}
	if m, err = readManifestFile(dir); err != nil {
		t.Fatal(err)
	}
	if m.Layout != layoutVersion || !m.UnstampedRecords {
		t.Errorf("stamp after migration: layout %d unstamped_records %v, want %d and true",
			m.Layout, m.UnstampedRecords, layoutVersion)
	}
	// A store created by this build is v4 outright, and has no migration
	// provenance to sanction anything with.
	fresh := t.TempDir()
	s, err := OpenStore(fresh, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if m, err = readManifestFile(fresh); err != nil {
		t.Fatal(err)
	}
	// A store this build created has no pre-stamp records, so it must NOT
	// carry the flag that sanctions reading as if it did.
	if m.Layout != layoutVersion || m.UnstampedRecords {
		t.Errorf("fresh store: layout %d unstamped_records %v, want %d and false",
			m.Layout, m.UnstampedRecords, layoutVersion)
	}
}

// shapeOf is every node path in every channel, sorted: the string that
// changes if anything moved.
func shapeOf(t *testing.T, dir string) string {
	t.Helper()
	chans, _, _, err := flattenManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, ch := range chans {
		paths, err := nodePaths(filepath.Join(dir, ch))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			all = append(all, ch+"/"+p)
		}
	}
	sort.Strings(all)
	return strings.Join(all, "\n")
}

func equalBases(a, b map[string]map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for ch, m := range a {
		n, ok := b[ch]
		if !ok || len(m) != len(n) {
			return false
		}
		for k, v := range m {
			if n[k] != v {
				return false
			}
		}
	}
	return true
}

// A v3 trunk was a CHAIN of nodes: a continuation forked a child that
// inherited the trunk id, and the head was the deepest of them. Flat, one
// trunk has one head, and two of them is an open error. So the migration
// must keep the id on the deepest node and demote its ancestors -- deepest
// because that is where a chain's newest records are, which is measured on
// the real store (fork bases rise strictly with depth across every chain).
func TestFlattenLeavesOneHeadPerTrunkChain(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	// Turn a parent/child pair into a v3 continuation chain: the child
	// inherits the parent's trunk id, and its own ceases to exist.
	paths, err := nodePaths(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	var parent, child string
	for _, p := range paths {
		for _, q := range paths {
			if filepath.Dir(q) == p && pathDepth(p) >= 2 {
				parent, child = p, q
			}
		}
	}
	if child == "" {
		t.Fatal("fixture has no nested parent/child pair")
	}
	shared, err := os.ReadFile(filepath.Join(dir, "ir", parent, legacyTrunkName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ir", child, legacyTrunkName), shared, 0o644); err != nil {
		t.Fatal(err)
	}
	trunk := strings.TrimSpace(string(shared))

	if _, err := Flatten(dir); err != nil {
		t.Fatalf("flatten a chain: %v", err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("open after migrating a chain: %v", err)
	}
	defer s.Close()

	head, ok := s.HeadNode(trunk)
	if !ok {
		t.Fatalf("trunk %s has no head", trunk)
	}
	if want := filepath.Base(child); head != want {
		t.Errorf("head of the chain is %q, want the DEEPEST node %q", head, want)
	}
	if m, ok := readNodeMarker(filepath.Join(dir, "ir", filepath.Base(parent))); !ok || m.trunk != "" {
		t.Errorf("ancestor %q still claims a trunk: %+v", parent, m)
	}
	// The demoted ancestor is still lineage, so the head reads through it.
	recs, err := s.RecordsFrom(trunk, "ir", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Error("the surviving head reads nothing")
	}
}

// An interrupted run leaves a chain MIXED: the deep node has moved to depth
// 1 while its ancestor is still nested. Ranking those by PATH depth puts the
// moved head first, the chain check then compares the wrong pair, and the
// store is refused as tangled -- by that run and by every one after it. The
// real store has 44 chains, so it is 44 chances per migration.
func TestFlattenResumesAChainInterruptedMidMove(t *testing.T) {
	dir, before := buildNestedFixture(t)
	paths, err := nodePaths(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	var parent, child string
	for _, p := range paths {
		for _, q := range paths {
			if filepath.Dir(q) == p && pathDepth(p) >= 2 {
				parent, child = p, q
			}
		}
	}
	if child == "" {
		t.Fatal("fixture has no nested parent/child pair")
	}
	shared, err := os.ReadFile(filepath.Join(dir, "ir", parent, legacyTrunkName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ir", child, legacyTrunkName), shared, 0o644); err != nil {
		t.Fatal(err)
	}
	trunk := strings.TrimSpace(string(shared))

	// Interrupt exactly where it hurts: the head is migrated and moved, its
	// ancestor is untouched and still nested under its own parent.
	head := filepath.Base(child)
	if err := writeNodeMarker(filepath.Join(dir, "ir", child), nodeMarker{
		from: filepath.Base(parent), kind: "conversation", trunk: trunk,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "ir", child, legacyTrunkName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "ir", child), filepath.Join(dir, "ir", head)); err != nil {
		t.Fatal(err)
	}

	if _, err := Flatten(dir); err != nil {
		t.Fatalf("resume a chain interrupted mid-move: %v", err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("open after the resumed chain migration: %v", err)
	}
	defer s.Close()
	if got, ok := s.HeadNode(trunk); !ok || got != head {
		t.Errorf("head after resume is %q (ok %v), want the deepest node %q", got, ok, head)
	}
	if len(s.List()) != len(before.trunks)-1 { // the chain absorbed one trunk id
		t.Errorf("trunks after resume: %d, want %d", len(s.List()), len(before.trunks)-1)
	}
}

func TestOpenRefusesAStoreFromANewerBuild(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := readManifestFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Layout = layoutVersion + 1
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	// A migration cannot help a store from the future, and must not be
	// offered: Flatten would walk it and stamp it BACK to this layout.
	if need, err := NeedsFlatten(dir); err != nil || need {
		t.Errorf("NeedsFlatten on a newer store: %v (err %v), want false", need, err)
	}
	if _, err := OpenStore(dir, testStoreOptions()); !errors.Is(err, ErrFutureLayout) {
		t.Errorf("open of a newer store: got %v, want ErrFutureLayout", err)
	}
}

// The cursor fallback is migration-era, and the flag is its gate. A channel
// born AFTER a main record leaves that record with no stamp for it, and on a
// store this build wrote the fallback then answers with the channel's whole
// TAIL -- unkeyed records carry main LT 0, so every one of them reads as
// "at or below" any main LT. A fork there would inherit records written
// after it branched.
func TestCursorFallbackIsUnreachableOnAStoreThisBuildCreated(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.Unkeyed = []string{"notes"}
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	trunk, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(string(trunk), "ir", 0, fmt.Appendf(nil, `{"i":%d}`, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	// The channel is born AFTER those records, so none of them stamps it.
	for i := 0; i < 2; i++ {
		if _, err := s.Append(string(trunk), "notes", 0, fmt.Appendf(nil, `{"n":%d}`, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	x, err := s.Trunks.Head(string(trunk))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	// The premise, checked rather than assumed: the channel really is
	// unkeyed, so its records carry no main LT and the fallback has
	// something wrong to say.
	if lt, ok, err := x.chans["notes"].lookupAtOrBelow(1); err != nil || !ok || lt == 0 {
		t.Fatalf("the fallback would answer %d (ok %v, err %v); this test cannot fail unless it answers something", lt, ok, err)
	}
	for _, at := range []uint64{1, 2} {
		cur, err := x.CursorAt(at, "notes")
		if err != nil {
			t.Fatal(err)
		}
		if cur != 0 {
			t.Errorf("cursor at main %d is %d; the channel did not exist yet, so it must be 0", at, cur)
		}
	}
}

// And it IS reachable on a migrated one, which is the whole reason the
// fallback survives: those stores have related records still carrying the
// main LT they were keyed by.
func TestCursorFallbackServesAMigratedStore(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	if _, err := Flatten(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	found := false
	for _, ti := range s.List() {
		x, err := s.Trunks.Head(ti.ID)
		if err != nil {
			t.Fatal(err)
		}
		cur, err := x.CursorAt(mainTail(x), "chalkboard")
		x.Close()
		if err != nil {
			t.Fatal(err)
		}
		if cur > 0 {
			found = true
		}
	}
	if !found {
		t.Error("no trunk resolved a chalkboard cursor: the migration-era fallback is not running")
	}
}

// A collision must be REPORTED as a collision. The lineage walk indexes
// nodes by leaf name, so a duplicate leaf corrupts the depths and the run
// can refuse with "not one chain" -- a true refusal for a false reason, and
// the reason is what the user acts on.
func TestACollisionIsReportedAsACollisionNotAsATangledChain(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	paths, err := nodePaths(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate a DEEP node's leaf name higher up, and give both the same
	// trunk id, so the tangled-chain path is reachable too.
	var deep string
	for _, p := range paths {
		if pathDepth(p) > pathDepth(deep) {
			deep = p
		}
	}
	clash := filepath.Join(filepath.Dir(filepath.Dir(deep)), filepath.Base(deep))
	if err := os.MkdirAll(filepath.Join(dir, "ir", clash), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ir", clash, ".fork"), []byte("base=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trunk, err := os.ReadFile(filepath.Join(dir, "ir", deep, legacyTrunkName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ir", clash, legacyTrunkName), trunk, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Flatten(dir)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "both flatten to") {
		t.Errorf("refusal does not name the collision: %v", err)
	}
	if strings.Contains(err.Error(), "not one chain") {
		t.Errorf("refusal blames the chain for a collision: %v", err)
	}
}

// A kill lands between two demotion writes, and the nodes still claiming
// the trunk are then a grandparent and a grandchild: one line of descent,
// with the demoted node between them, NOT adjacent. Testing immediate
// parentage called that tangled and refused the store forever -- with every
// byte of every aria intact on disk. A fuzz worker measured the window at
// roughly 80% of the main phase, which makes it the ordinary case rather
// than a race.
func TestFlattenResumesAChainWhoseMiddleWasAlreadyDemoted(t *testing.T) {
	dir, _ := buildNestedFixture(t)
	paths, err := nodePaths(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	// A three-node chain: grandparent -> parent -> child, all one trunk.
	var gp, mid, child string
	for _, p := range paths {
		for _, q := range paths {
			for _, r := range paths {
				if filepath.Dir(q) == p && filepath.Dir(r) == q {
					gp, mid, child = p, q, r
				}
			}
		}
	}
	if child == "" {
		t.Fatal("fixture has no three-node chain")
	}
	trunk, err := os.ReadFile(filepath.Join(dir, "ir", gp, legacyTrunkName))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{mid, child} {
		if err := os.WriteFile(filepath.Join(dir, "ir", n, legacyTrunkName), trunk, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := strings.TrimSpace(string(trunk))

	// The interrupted state: the MIDDLE node has been demoted and its
	// legacy marker retired; the other two still claim the trunk.
	if err := writeNodeMarker(filepath.Join(dir, "ir", mid), nodeMarker{
		from: filepath.Base(gp), kind: "conversation", trunk: "",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "ir", mid, legacyTrunkName)); err != nil {
		t.Fatal(err)
	}

	if _, err := Flatten(dir); err != nil {
		t.Fatalf("resume with a demoted middle: %v", err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("open after the resumed migration: %v", err)
	}
	defer s.Close()
	head, ok := s.HeadNode(id)
	if !ok {
		t.Fatalf("trunk %s has no head after the resume", id)
	}
	if want := filepath.Base(child); head != want {
		t.Errorf("head is %q, want the deepest node %q", head, want)
	}
	if recs, err := s.RecordsFrom(id, "ir", 0, 0); err != nil || len(recs) == 0 {
		t.Errorf("the surviving head reads %d records (err %v)", len(recs), err)
	}
}
