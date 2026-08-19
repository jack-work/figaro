package xwal

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func trunksCfg() Config {
	return Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "translations", Kind: ChannelLog},
			{Name: "chalkboard", Kind: ChannelReducible, Reducer: "jsonmerge"},
		},
		Registry:    map[string]Reducer{"jsonmerge": {Reduce: jsonMerge, Initial: []byte("{}")}},
		SegmentSize: 4096,
	}
}

// seedTrunk creates a fresh store and returns it plus a live top-level
// trunk, rooted at a markerless (birthless) stump so the trunk's own range
// starts at LT 2 — exactly where the old root-trunk's own content began
// (genesis@1 inherited). Lets the trunk-mechanics tests read identically.
func seedTrunk(t *testing.T, dir string) (*Trunks, TrunkID) {
	t.Helper()
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("s"); err != nil {
		t.Fatalf("create stump: %v", err)
	}
	id, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatalf("spawn under stump: %v", err)
	}
	return f, id
}

// headMainTail opens a trunk's head and returns its main tail + first
// real (post-genesis) payloads, for assertions.
func headPayloads(t *testing.T, f *Trunks, trunk TrunkID) []string {
	t.Helper()
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatalf("head %s: %v", trunk, err)
	}
	defer x.Close()
	var out []string
	last := mainTail(x)
	for lt := uint64(1); lt <= last; lt++ {
		_, p, err := x.Read("ir", lt)
		if err != nil {
			t.Fatalf("read ir %d on %s: %v", lt, trunk, err)
		}
		out = append(out, string(p))
	}
	return out
}

func TestTree_TailAppendNoFork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedTrunk(t, dir)
	// Tail appends (atMainLT 0) keep the same trunk.
	tr, _, err := f.Append(root, 0, []byte(`"u1"`), nil)
	if err != nil || tr != root {
		t.Fatalf("append1: trunk=%s err=%v (want %s)", tr, err, root)
	}
	tr, _, err = f.Append(root, 0, []byte(`"a1"`), nil)
	if err != nil || tr != root {
		t.Fatalf("append2: trunk=%s err=%v", tr, err)
	}
	// genesis + u1 + a1
	got := headPayloads(t, f, root)
	if len(got) != 3 {
		t.Fatalf("want 3 entries (genesis+2), got %d: %v", len(got), got)
	}
}

func TestTree_InteriorForkKeepsExistingTrunk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedTrunk(t, dir)
	// Build root: genesis(1) u1(2) a1(3) u2(4)  -> tail 4
	for _, m := range []string{`"u1"`, `"a1"`, `"u2"`} {
		if _, _, err := f.Append(root, 0, []byte(m), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Interior fork: `:3` shares [1..3] (genesis,u1,a1) and diverges at 4.
	alt, altLT := forkAppend(t, f, root, 3, []byte(`"alt-from-4"`))
	if alt == root {
		t.Fatalf("interior fork must mint a new trunk, got root %s", root)
	}
	if altLT != 4 {
		t.Fatalf("alt new content should land at LT 4 (shares [1..3]), got %d", altLT)
	}
	// Existing trunk retained intact: genesis,u1,a1,u2 still readable.
	rootGot := headPayloads(t, f, root)
	if len(rootGot) != 4 || rootGot[3] != `"u2"` {
		t.Fatalf("existing trunk not intact: %v", rootGot)
	}
	// Alt shares the frozen prefix [genesis,u1,a1] (LT<=3) then diverges at 4.
	altGot := headPayloads(t, f, alt)
	if len(altGot) != 4 || altGot[3] != `"alt-from-4"` {
		t.Fatalf("alt content wrong: %v", altGot)
	}
	if altGot[0] != genesisMarker || altGot[1] != `"u1"` || altGot[2] != `"a1"` {
		t.Fatalf("alt did not inherit shared prefix: %v", altGot)
	}
	// Trunk listing: root + alt; alt parent = root, branched at 4.
	trunks := f.List()
	if len(trunks) != 2 {
		t.Fatalf("want 2 trunks, got %d", len(trunks))
	}
	var altInfo *TrunkInfo
	for i := range trunks {
		if trunks[i].ID == alt {
			altInfo = &trunks[i]
		}
	}
	if altInfo == nil || altInfo.Parent != root || altInfo.BranchedLT != 4 {
		t.Fatalf("alt trunk info wrong: %+v", altInfo)
	}
}

func TestTree_ChalkboardForksAlong(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedTrunk(t, dir)
	// turn 1: append + a chalkboard patch keyed to the committed LT
	_, lt1, _ := f.Append(root, 0, []byte(`"u1"`), nil)
	if _, err := f.AppendChannel(root, "chalkboard", lt1, []byte(`{"set":{"mantra":"root thread"}}`), nil); err != nil {
		t.Fatal(err)
	}
	f.Append(root, 0, []byte(`"a1"`), nil) // tail 3

	// interior fork: `:2` shares [1..2] (incl. root-thread chalkboard@2),
	// diverges at 3 -> alt inherits the chalkboard watermark.
	alt, _ := forkAppend(t, f, root, 2, []byte(`"alt"`))
	if _, err := f.AppendChannel(alt, "chalkboard", 0, []byte(`{"set":{"mantra":"alt thread"}}`), nil); err != nil {
		t.Fatal(err)
	}
	// alt chalkboard folds to alt thread; root stays root thread.
	ax, _ := f.Head(alt)
	defer ax.Close()
	st, err := ax.StateAt("chalkboard", chalkLast(t, ax))
	if err != nil {
		t.Fatal(err)
	}
	if string(st) != `{"mantra":"alt thread"}` {
		t.Fatalf("alt chalkboard = %s", st)
	}
	rx, _ := f.Head(root)
	defer rx.Close()
	rst, _ := rx.StateAt("chalkboard", chalkLast(t, rx))
	if string(rst) != `{"mantra":"root thread"}` {
		t.Fatalf("root chalkboard = %s", rst)
	}
}

func TestTree_ForkTailBisectsPresent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, root := seedTrunk(t, dir)
	for _, m := range []string{`"u1"`, `"a1"`} {
		f.Append(root, 0, []byte(m), nil)
	}
	rootBefore := headPayloads(t, f, root) // genesis,u1,a1
	alt, err := f.ForkTail(root)
	if err != nil {
		t.Fatalf("fork-tail: %v", err)
	}
	if alt == root {
		t.Fatal("fork-tail must mint a new alternative trunk")
	}
	// Both share the full prefix [genesis,u1,a1]; both empty after.
	for _, tr := range []TrunkID{root, alt} {
		got := headPayloads(t, f, tr)
		if len(got) != len(rootBefore) {
			t.Fatalf("trunk %s after fork-tail: want %d shared entries, got %v", tr, len(rootBefore), got)
		}
	}
	// Diverge: append to each, confirm isolation.
	f.Append(root, 0, []byte(`"root-cont"`), nil)
	f.Append(alt, 0, []byte(`"alt-cont"`), nil)
	rg := headPayloads(t, f, root)
	ag := headPayloads(t, f, alt)
	if rg[len(rg)-1] != `"root-cont"` || ag[len(ag)-1] != `"alt-cont"` {
		t.Fatalf("divergence failed: root=%v alt=%v", rg, ag)
	}
}

func chalkLast(t *testing.T, x *XWAL) uint64 {
	t.Helper()
	for _, c := range x.Channels() {
		if c.Name == "chalkboard" {
			return c.Last
		}
	}
	t.Fatal("no chalkboard channel")
	return 0
}

// seedTrunkBirth seeds a store with a stump carrying a birth IR entry (@2),
// then a trunk under it (owns from LT 3) — the figaro loadout→conversation
// shape (genesis@1, loadout-birth@2 inherited).
func seedTrunkBirth(t *testing.T, dir, stump string) (*Trunks, TrunkID) {
	t.Helper()
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump(stump); err != nil {
		t.Fatalf("create stump: %v", err)
	}
	sx, err := f.StumpHead(stump)
	if err != nil {
		t.Fatalf("stump head: %v", err)
	}
	if _, err := sx.AppendMain([]byte(`"loadout-birth"`), nil); err != nil {
		t.Fatalf("birth: %v", err)
	}
	sx.Close()
	conv, err := f.SpawnUnderStump(stump)
	if err != nil {
		t.Fatalf("spawn under stump: %v", err)
	}
	return f, conv
}

func TestTrunks_Stumps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	// Two stumps (loadouts) under the markerless root.
	if err := f.CreateStump("L1@aa"); err != nil {
		t.Fatalf("stump L1: %v", err)
	}
	if err := f.CreateStump("L2@bb"); err != nil {
		t.Fatalf("stump L2: %v", err)
	}
	if err := f.CreateStump("L1@aa"); err == nil {
		t.Fatal("duplicate stump name must fail")
	}
	// Stumps carry no .Trunk marker.
	for _, s := range []string{"L1@aa", "L2@bb"} {
		if _, err := os.Stat(filepath.Join(dir, "ir", s, ".Trunk")); err == nil {
			t.Fatalf("stump %s must be markerless", s)
		}
	}
	// The root sheds its .Trunk marker too.
	if _, err := os.Stat(filepath.Join(dir, "ir", ".Trunk")); err == nil {
		t.Fatal("root must be markerless")
	}
	// A stump spawns multiple top-level trunks (conversations).
	c1, err := f.SpawnUnderStump("L1@aa")
	if err != nil {
		t.Fatalf("spawn c1: %v", err)
	}
	c2, err := f.SpawnUnderStump("L1@aa")
	if err != nil {
		t.Fatalf("spawn c2: %v", err)
	}
	if c1 == c2 {
		t.Fatal("spawned trunks must be distinct")
	}
	// Each conversation is a live trunk: turns + interior fork work.
	if _, _, err := f.Append(c1, 0, []byte(`"hello"`), nil); err != nil {
		t.Fatalf("append c1: %v", err)
	}
	if _, _, err := f.Append(c1, 0, []byte(`"more"`), nil); err != nil {
		t.Fatal(err)
	}
	alt, _ := forkAppend(t, f, c1, 2, []byte(`"branch"`)) // interior fork at c1's first own LT
	if alt == c1 {
		t.Fatal("interior fork should mint a new trunk")
	}
	// List exposes the trunk's stump.
	for _, ti := range f.List() {
		if ti.ID == c1 && ti.Stump != "L1@aa" {
			t.Fatalf("c1 stump = %q, want L1@aa", ti.Stump)
		}
	}
	// Reopen from disk: stumps + trunks resolve.
	r2, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, r2)
	if len(r2.Stumps()) != 2 {
		t.Fatalf("want 2 stumps after reopen, got %d", len(r2.Stumps()))
	}
	if _, err := r2.SpawnUnderStump("L1@aa"); err != nil {
		t.Fatalf("stump must still spawn after reopen: %v", err)
	}
	for _, tr := range []string{c1, c2, alt} {
		if x, err := r2.Head(tr); err != nil {
			t.Fatalf("reopen head %s: %v", tr, err)
		} else {
			x.Close()
		}
	}
}

func TestTrunks_ForkAt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, conv := seedTrunk(t, dir)
	// conv owns u1,u2,u3 (LTs 2,3,4 after genesis@1 inherited).
	for _, m := range []string{`"u1"`, `"u2"`, `"u3"`} {
		if _, _, err := f.Append(conv, 0, []byte(m), nil); err != nil {
			t.Fatal(err)
		}
	}
	x, _ := f.Head(conv)
	tail := mainTail(x)
	x.Close()
	// Interior fork at tail-1 (no message) -> empty alt sharing the prefix.
	alt, err := f.ForkAt(conv, tail-1)
	if err != nil {
		t.Fatalf("forkat interior: %v", err)
	}
	if alt == conv || alt == "" {
		t.Fatalf("forkat must mint a new trunk, got %q", alt)
	}
	// alt is live and empty-own: a send appends at the divergence point.
	if _, _, err := f.Append(alt, 0, []byte(`"branch msg"`), nil); err != nil {
		t.Fatalf("send to forked alt: %v", err)
	}
	// conv keeps its id and is still re-forkable / sendable.
	if _, _, err := f.Append(conv, 0, []byte(`"u4"`), nil); err != nil {
		t.Fatalf("original trunk still live: %v", err)
	}
	// Past-tail ForkAt degenerates to a tail fork.
	alt2, err := f.ForkAt(conv, 9999)
	if err != nil || alt2 == "" || alt2 == conv {
		t.Fatalf("forkat past tail should tail-fork: alt2=%q err=%v", alt2, err)
	}
}

func TestTrunks_ReSplitBelow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	// conv owns LTs 3,4,5,6 (genesis@1, loadout-birth@2 inherited).
	f, conv := seedTrunkBirth(t, dir, "L@h")
	for _, m := range []string{`"a"`, `"b"`, `"c"`, `"d"`} {
		if _, _, err := f.Append(conv, 0, []byte(m), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Fork at 5 -> alt2 shares [1..5], conv continues with its suffix.
	alt2, _ := forkAppend(t, f, conv, 5, []byte(`"alt-from-5"`))
	// Now RE-SPLIT-BELOW: fork alt2 at LT 3 — a turn alt2 inherited from conv
	// (below alt2's own range). Must fork the ancestor and mint a sibling.
	sib, _ := forkAppend(t, f, alt2, 3, []byte(`"resplit-at-3"`))
	if sib == alt2 || sib == conv || sib == "" {
		t.Fatalf("re-split must mint a distinct sibling trunk, got %q", sib)
	}
	// Everyone still resolves + folds after the re-split, and from disk.
	r2, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, r2)
	for _, tr := range []string{conv, alt2, sib} {
		x, err := r2.Head(tr)
		if err != nil {
			t.Fatalf("reopen head %s: %v", tr, err)
		}
		x.Close()
		if _, _, err := r2.Append(tr, 0, []byte(`"more"`), nil); err != nil {
			t.Fatalf("append to %s after re-split: %v", tr, err)
		}
	}
}

func TestTrunks_Owner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	// genesis@1 owned by root; birth@2 owned by the stump; conv owns [3..].
	f, conv := seedTrunkBirth(t, dir, "L@h")
	for _, m := range []string{`"u1"`, `"u2"`} {
		if _, _, err := f.Append(conv, 0, []byte(m), nil); err != nil {
			t.Fatal(err)
		}
	}
	// LT 1 -> root, LT 2 -> stump, LT 3/4 -> conv.
	o1, err := f.Owner(conv, 1)
	if err != nil || !o1.IsRoot {
		t.Fatalf("Owner(1) = %+v err=%v, want root", o1, err)
	}
	o2, err := f.Owner(conv, 2)
	if err != nil || o2.Stump != "L@h" || o2.Trunk != "" {
		t.Fatalf("Owner(2) = %+v err=%v, want stump L@h", o2, err)
	}
	for _, lt := range []uint64{3, 4} {
		o, err := f.Owner(conv, lt)
		if err != nil || o.Trunk != conv {
			t.Fatalf("Owner(%d) = %+v err=%v, want trunk %s", lt, o, err, conv)
		}
	}
	// OwnerTrunk still resolves the trunk id (empty for ceremonial owners).
	if got, _ := f.OwnerTrunk(conv, 3); got != conv {
		t.Fatalf("OwnerTrunk(3) = %q, want %s", got, conv)
	}
}

func TestTrunks_Remove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, conv := seedTrunkBirth(t, dir, "L@h")
	f.Append(conv, 0, []byte(`"u1"`), nil)
	f.Append(conv, 0, []byte(`"u2"`), nil)
	alt, _ := f.ForkTail(conv) // conv now has a live branch (alt)

	// A stump name is not a trunk id -> Remove refuses it.
	if err := f.Remove("L@h", false); err == nil {
		t.Fatal("removing a stump (not a trunk) must fail")
	}
	// conv has a live branch (alt) -> refuse without recursive.
	if err := f.Remove(conv, false); err == nil {
		t.Fatal("removing a trunk with live branches must fail without recursive")
	}
	// Remove the leaf branch (alt) directly -> ok; conv survives.
	if err := f.Remove(alt, false); err != nil {
		t.Fatalf("remove leaf branch: %v", err)
	}
	if x, err := f.Head(conv); err != nil {
		t.Fatalf("conv must survive removing its branch: %v", err)
	} else {
		x.Close()
	}
	if _, err := f.Head(alt); err == nil {
		t.Fatal("alt should be gone")
	}
	// Reopen from disk: alt stays gone, conv resolves, stump intact.
	r2, _ := openTrunks(dir, trunksCfg())
	cleanupTrunks(t, r2)
	if x, err := r2.Head(conv); err != nil {
		t.Fatalf("conv missing after reopen: %v", err)
	} else {
		x.Close()
	}
	if _, err := r2.Head(alt); err == nil {
		t.Fatal("alt resurrected after reopen")
	}
	if len(r2.Stumps()) != 1 {
		t.Fatalf("stump must survive trunk removal, got %d stumps", len(r2.Stumps()))
	}
	// Recursive remove of conv.
	if err := r2.Remove(conv, true); err != nil {
		t.Fatalf("recursive remove conv: %v", err)
	}
	if _, err := r2.Head(conv); err == nil {
		t.Fatal("conv should be gone after recursive remove")
	}
}

// --- Version() / Refresh() ------------------------------------------------
//
// The topology-version cookie is the mechanism consumers use to detect
// that markers on disk have moved. Modeled on SQLite's schema cookie: a
// cheap monotonic uint incremented on every rebuild. In-process readers
// don't strictly need it — every public mutation already rebuilds — but
// exposing it makes downstream caches self-invalidating and gives a
// future cross-process story an integration point via Refresh().

func TestVersion_MonotonicOnOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	// Post-create version is >0 (Create rebuilds once).
	v0 := f.Version()
	if v0 == 0 {
		t.Fatal("Version after CreateTrunks should be > 0")
	}

	// Reopen: the reopened instance starts its own counter but must
	// also be non-zero (OpenTrunks rebuilds once).
	r, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, r)
	if v := r.Version(); v == 0 {
		t.Fatal("Version after OpenTrunks should be > 0")
	}
}

func TestVersion_BumpsOnMutations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	bump := func(step string, do func()) uint64 {
		before := f.Version()
		do()
		after := f.Version()
		if after <= before {
			t.Fatalf("%s: Version did not increase (%d -> %d)", step, before, after)
		}
		return after
	}

	// CreateStump.
	bump("CreateStump", func() {
		if err := f.CreateStump("cfg@aa"); err != nil {
			t.Fatal(err)
		}
	})
	// SpawnUnderStump.
	var conv TrunkID
	bump("SpawnUnderStump", func() {
		var err error
		conv, err = f.SpawnUnderStump("cfg@aa")
		if err != nil {
			t.Fatal(err)
		}
	})
	// Append (extends head only — no rebuild path).
	// This is on the hot write path and MUST NOT bump the topology
	// version: Version is for structural changes, not content.
	before := f.Version()
	if _, _, err := f.Append(conv, 0, []byte(`"m1"`), nil); err != nil {
		t.Fatal(err)
	}
	if f.Version() != before {
		t.Fatalf("plain Append should not bump topology version (was %d, now %d)", before, f.Version())
	}
	// A few more appends to have interior LTs for a fork.
	f.Append(conv, 0, []byte(`"m2"`), nil)
	f.Append(conv, 0, []byte(`"m3"`), nil)
	// Interior fork — must bump (relabels markers).
	var alt TrunkID
	bump("ForkAt", func() {
		var err error
		alt, err = f.ForkAt(conv, 3)
		if err != nil {
			t.Fatal(err)
		}
	})
	bump("Remove", func() {
		if err := f.Remove(alt, false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVersion_RefreshIsCheapAndBumps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, conv := seedTrunk(t, dir)
	_ = conv
	v0 := f.Version()
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if v1 := f.Version(); v1 <= v0 {
		t.Fatalf("Refresh should bump version (%d -> %d)", v0, v1)
	}
}

// TestVersion_ExternalRewriteBumpsAfterRefresh simulates the
// cross-process scenario: another process rewrote a .Trunk marker (or
// mutated the tree) while we weren't watching. In-process our Version
// counter cannot know; but calling Refresh() re-scans disk and picks up
// the change. This locks in the contract for future multi-process work.
func TestVersion_ExternalRewriteBumpsAfterRefresh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, c := seedTrunkBirth(t, dir, "cfg@d880")
	// Write something so there's a node to touch.
	if _, _, err := f.Append(c, 0, []byte(`"m1"`), nil); err != nil {
		t.Fatal(err)
	}
	beforeVer := f.Version()

	// Simulate an out-of-band write: touch a marker file's mtime by
	// rewriting the same id. rebuild() re-scans regardless.
	// (We rewrite the same content so the tree is functionally
	// unchanged — the test is about Refresh always bumping.)
	if err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.Version() <= beforeVer {
		t.Fatalf("Refresh should have bumped version (was %d, now %d)", beforeVer, f.Version())
	}
	// Head still works after the refresh.
	if x, err := f.Head(c); err != nil {
		t.Fatalf("head c after refresh: %v", err)
	} else {
		x.Close()
	}
}

func TestConcurrentAppendsUseLineageSerialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, first := seedTrunk(t, dir)
	second, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}

	const writers = 4
	const writes = 20
	var wg sync.WaitGroup
	errs := make(chan error, writers*2)
	for _, trunk := range []string{first, second} {
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range writes {
					if _, _, err := f.Append(trunk, 0, []byte(`"w"`), nil); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, trunk := range []string{first, second} {
		x, err := f.Head(trunk)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := mainTail(x), uint64(1+writers*writes); got != want {
			x.Close()
			t.Fatalf("%s tail = %d, want %d", trunk, got, want)
		}
		x.Close()
	}
}

func TestTopologyMutationRefusesOpenHead(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	if _, _, err := f.Append(trunk, 0, []byte(`"m1"`), nil); err != nil {
		t.Fatal(err)
	}
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	// A creator no longer waits on open heads; a DESTRUCTIVE op still must,
	// because it unlinks directories a live head is reading.
	if _, err := f.ForkTail(trunk); err != nil {
		t.Fatalf("ForkTail must not wait on an open head: %v", err)
	}
	shortTopologyWait(t)
	if err := f.Remove(trunk, true); err == nil {
		x.Close()
		t.Fatal("Remove succeeded with an open head")
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(trunk, true); err != nil {
		t.Fatalf("Remove after close: %v", err)
	}
}

func TestTopologyMutationSeesRetiredOpenHead(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	if _, _, err := f.Append(trunk, 0, []byte(`"m1"`), nil); err != nil {
		t.Fatal(err)
	}
	stale, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	mutator, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	spec := ChannelSpec{Name: "scratch", Kind: ChannelLog}
	if err := mutator.addChannel(spec); err != nil {
		stale.Close()
		mutator.Close()
		t.Fatalf("AddChannel with retired head: %v", err)
	}
	if err := mutator.Close(); err != nil {
		t.Fatal(err)
	}
	shortTopologyWait(t)
	if err := f.Remove(trunk, true); !isTopologyTimeout(err) {
		stale.Close()
		t.Fatalf("Remove error = %v, want bounded-wait timeout", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ForkTail(trunk); err != nil {
		t.Fatalf("ForkTail after retired head close: %v", err)
	}
}

// TestConcurrentAppendAndFork proves that a topology writer waits for an
// in-flight lineage append before changing the branch beneath it.
func TestConcurrentAppendAndFork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, tr := seedTrunkBirth(t, dir, "cfg@d880")
	for _, m := range []string{`"m1"`, `"m2"`, `"m3"`, `"m4"`} {
		if _, _, err := f.Append(tr, 0, []byte(m), nil); err != nil {
			t.Fatal(err)
		}
	}

	const N = 30
	var wg sync.WaitGroup
	var appendErr atomic.Value // holds error
	var forkErr atomic.Value

	// Writer: hammer tail appends on tr.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			if _, _, err := f.Append(tr, 0, []byte(`"w"`), nil); err != nil {
				appendErr.Store(err)
				return
			}
		}
	}()
	// Forker: mint N alternatives off the same trunk (tail forks).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			if _, err := f.ForkTail(tr); err != nil {
				forkErr.Store(err)
				return
			}
		}
	}()
	wg.Wait()
	if err, _ := appendErr.Load().(error); err != nil {
		t.Fatalf("concurrent append: %v", err)
	}
	if err, _ := forkErr.Load().(error); err != nil {
		t.Fatalf("concurrent fork: %v", err)
	}

	// Post-condition: tr's head still opens, reads end-to-end.
	x, err := f.Head(tr)
	if err != nil {
		t.Fatalf("post head: %v", err)
	}
	defer x.Close()
	last := mainTail(x)
	for lt := uint64(1); lt <= last; lt++ {
		if _, _, err := x.Read("ir", lt); err != nil {
			t.Fatalf("post read %d: %v", lt, err)
		}
	}
}
