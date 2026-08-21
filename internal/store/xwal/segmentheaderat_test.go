package xwal

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// SegmentHeaderAt MIRRORED UP: disk.Log -> log.Log -> XWAL -> Store.
//
// A mirror is exactly where a fact gets dropped -- each layer re-states the
// call and one of them can forget the parent walk, or the sync, or the base.
// disk/segmentheaderat_test.go proves the bottom layer; this proves the fact
// survives all four, through a real reducible channel on a real trunk fork.
//
// The oracle is StateAt at the same layer: fold the header the call returns
// with the records in [base..lt] and it must equal what the library already
// returns for lt.

func chalkCfgSmallSegments() Config {
	c := trunksCfg()
	// Small enough that a few dozen patches rotate several times: the whole
	// question is which SEGMENT a header comes from, and a single-segment
	// channel cannot ask it.
	c.SegmentSize = 512
	return c
}

// foldFromXWALHeader is the consumer pattern this API exists to enable: one
// call for the header and its base, then fold the channel's own records with
// the caller's reducer -- here jsonMerge, the same one the channel is
// configured with.
func foldFromXWALHeader(t *testing.T, x *XWAL, channel string, lt uint64) ([]byte, uint64) {
	t.Helper()
	header, base, err := x.SegmentHeaderAt(channel, lt)
	if err != nil {
		t.Fatalf("SegmentHeaderAt(%s, %d): %v", channel, lt, err)
	}
	if base == 0 {
		t.Fatalf("SegmentHeaderAt(%s, %d) returned base 0 with a nil error", channel, lt)
	}
	if base > lt {
		t.Fatalf("SegmentHeaderAt(%s, %d) returned base %d above the index", channel, lt, base)
	}
	if len(header) == 0 {
		// Worth pinning: openActiveLocked writes a header for EVERY segment,
		// so the first segment's header is the reducer's INITIAL state and
		// never empty. A consumer folding from a header must not have to
		// special-case "no header yet".
		t.Fatalf("SegmentHeaderAt(%s, %d) returned an EMPTY header at base %d: a "+
			"consumer would fold onto nothing and produce a plausible from-empty "+
			"state", channel, lt, base)
	}
	state := header
	for i := base; i <= lt; i++ {
		_, patch, err := x.Read(channel, i)
		if err != nil {
			t.Fatalf("read %s@%d: %v", channel, i, err)
		}
		state, err = jsonMerge(state, patch)
		if err != nil {
			t.Fatalf("fold %s@%d: %v", channel, i, err)
		}
	}
	return state, base
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var ma, mb map[string]any
	if err := json.Unmarshal(a, &ma); err != nil {
		t.Fatalf("unmarshal %q: %v", a, err)
	}
	if err := json.Unmarshal(b, &mb); err != nil {
		t.Fatalf("unmarshal %q: %v", b, err)
	}
	if len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		w, ok := mb[k]
		if !ok || v != w {
			return false
		}
	}
	return true
}

// TestSegmentHeaderAt_SurvivesEveryLayerAcrossATrunkFork writes enough
// chalkboard patches to rotate several segments, forks the trunk in the
// middle, and then, THROUGH THE CHILD, folds from the header at every channel
// LT -- including the ones owned by the parent, which is where the two-call
// route goes wrong.
func TestSegmentHeaderAt_SurvivesEveryLayerAcrossATrunkFork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, chalkCfgSmallSegments())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("s"); err != nil {
		t.Fatal(err)
	}
	trunk, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}

	set := func(k, v string) uint64 {
		_, mainLT, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.AppendChannel(trunk, "chalkboard", mainLT,
			[]byte(`{"set":{"`+k+`":"`+v+`"}}`), nil); err != nil {
			t.Fatal(err)
		}
		return mainLT
	}

	const n = 40
	var forkAt uint64
	for i := 0; i < n; i++ {
		mainLT := set(string(rune('a'+i%26))+string(rune('0'+i/26)), "v")
		if i == 24 {
			forkAt = mainLT
		}
	}

	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	// SYNC FIRST, and this is not a formality: unsynced appends have no
	// segment on disk at all, so SegmentBaseIndexes reads 0 and every
	// segment-shaped question answers about a log older than the one that
	// was written. It is why log.Log.SegmentHeaderAt syncs before asking.
	if err := head.SyncCoherent(); err != nil {
		t.Fatal(err)
	}
	parentBases := len(head.chans["chalkboard"].mustLog().SegmentBaseIndexes())
	head.Close()
	if parentBases < 3 {
		t.Fatalf("fixture did not rotate enough: %d chalkboard segments, want >= 3",
			parentBases)
	}

	alt, err := f.ForkAt(trunk, forkAt)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	// Write on the branch so it owns segments of its own and the interesting
	// indices really are below ITS fork base.
	for i := 0; i < 6; i++ {
		_, mainLT, err := f.Append(alt, 0, []byte(`"turn"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.AppendChannel(alt, "chalkboard", mainLT,
			[]byte(`{"set":{"branch`+string(rune('0'+i))+`":"v"}}`), nil); err != nil {
			t.Fatal(err)
		}
	}

	child, err := f.Head(alt)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	last := channelTail(child, "chalkboard")
	if last < 10 {
		t.Fatalf("chalkboard tail %d is too short to cross a segment boundary", last)
	}

	// AN INDEPENDENT ORACLE FOR THE BASE, and it is here because a canary
	// caught this test passing without it.
	//
	// Sabotaging disk.Log.SegmentHeaderAt to return the PARENT's header with
	// the CHILD's base -- the exact bug the two-call API would have shipped --
	// left this test GREEN when it checked only that folds equal StateAt. The
	// reason is structural, not a fixture accident: jsonMerge patches are
	// IDEMPOTENT, so a base that is too LOW merely re-applies records the
	// header already contains, and the value is identical. A value oracle
	// cannot see a wrong base in that direction, ever, for any idempotent
	// reducer -- which is what figaro's form patches are.
	//
	// So the base is checked against a fact obtained from somewhere else
	// entirely: the segment bases of the log that actually OWNS the index.
	// Below the child's fork base that is the PARENT's chalkboard log.
	parentHead, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := parentHead.SyncCoherent(); err != nil {
		t.Fatal(err)
	}
	parentBaseList := append([]uint64(nil), parentHead.chans["chalkboard"].mustLog().SegmentBaseIndexes()...)
	parentHead.Close()
	if len(parentBaseList) < 2 {
		t.Fatalf("the parent owns %d chalkboard segments: with fewer than two, "+
			"a wrong base below the fork point cannot be distinguished from a right one",
			len(parentBaseList))
	}
	expectedParentBase := func(lt uint64) uint64 {
		best := uint64(0)
		for _, b := range parentBaseList {
			if b <= lt && b > best {
				best = b
			}
		}
		return best
	}

	childForkBase := child.chans["chalkboard"].mustLog().ForkBase()
	if childForkBase == 0 {
		t.Fatal("the child's chalkboard log has no fork base, so this test is not " +
			"exercising a fork at all")
	}

	// THE CLAIM: at every channel LT the child can answer, folding from the
	// header the ONE call returns reproduces StateAt exactly.
	distinctBases := map[uint64]bool{}
	belowForkBase := 0
	for lt := uint64(1); lt <= last; lt++ {
		want, err := child.StateAt("chalkboard", lt)
		if err != nil {
			// Below the trunk's own first LT there is nothing to read; skip
			// only what StateAt itself refuses.
			continue
		}
		got, base := foldFromXWALHeader(t, child, "chalkboard", lt)
		distinctBases[base] = true
		if lt < childForkBase {
			belowForkBase++
			if base >= childForkBase {
				t.Fatalf("chalkboard@%d is below the child's fork base %d but resolved "+
					"to base %d in the CHILD's lineage: the parent walk did not survive "+
					"the mirroring", lt, childForkBase, base)
			}
			if want := expectedParentBase(lt); want != 0 && base != want {
				t.Fatalf("chalkboard@%d: base %d, but the PARENT's own segment bases "+
					"say the owning segment starts at %d. The fold still produced the "+
					"right value only because the reducer is idempotent; the base is "+
					"wrong and a non-idempotent reducer would have been corrupted",
					lt, base, want)
			}
		}
		if !sameJSON(t, got, want) {
			t.Fatalf("chalkboard@%d (segment base %d): fold-from-header %s, StateAt %s",
				lt, base, got, want)
		}
	}

	// POSITIVE ASSERTION THAT THE FIXTURE REACHED MORE THAN ONE SEGMENT: if
	// every answer came from one base, this test proves nothing about which
	// segment the header came from.
	// AND THAT IT WENT BELOW THE FORK BASE, which is the only region where a
	// dropped parent walk shows up at all.
	if belowForkBase == 0 {
		t.Fatalf("no answered LT fell below the child's fork base %d, so this "+
			"fixture cannot see a dropped parent walk", childForkBase)
	}
	t.Logf("%d of the answered LTs were below the child's fork base %d",
		belowForkBase, childForkBase)

	if len(distinctBases) < 2 {
		t.Fatalf("every answer came from %d distinct segment base(s): the fixture "+
			"never crossed a boundary, so it cannot see a wrong base", len(distinctBases))
	}
	t.Logf("chalkboard answers spanned %d distinct segment bases through the child",
		len(distinctBases))
}

// TestSegmentHeaderAt_StoreLayerRefusesRatherThanLies: the Store wrapper must
// carry the refusals up, not flatten them into a zero base.
func TestSegmentHeaderAt_StoreLayerRefusesRatherThanLies(t *testing.T) {
	s, err := OpenStore(t.TempDir(), testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	trunk, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(trunk, "ir", 0, []byte(`{"n":1}`), nil); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.SegmentHeaderAt(trunk, "nosuchchannel", 1); err == nil {
		t.Fatal("SegmentHeaderAt on an unknown channel returned no error")
	}
	// "ir" is the main LOG channel, not reducible: it has no headers at all.
	if _, _, err := s.SegmentHeaderAt(trunk, "ir", 1); err == nil {
		t.Fatal("SegmentHeaderAt on a non-reducible channel returned no error")
	}
	if _, _, err := s.SegmentHeaderAt("nosuchtrunk", "chalkboard", 1); err == nil {
		t.Fatal("SegmentHeaderAt on an unknown trunk returned no error")
	}
}
