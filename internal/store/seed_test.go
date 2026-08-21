package store

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// SEEDING A FORK'S DECODED CACHE FROM ITS ANCESTOR.
//
// Phase 3 measured the duplication and named the fix: a fork re-decodes its
// parent's prefix, and a shallow copy shares every payload string. This is the
// hazard test for that fix, written before it.
//
// THE HAZARD IS NOT PERFORMANCE, IT IS TRUTH. A seeded cache builds its
// resident window from rows another log decoded. If the rows are the wrong
// ones -- wrong lineage, wrong side of the fork base, an off-by-one on the
// seam -- the cache does not go slow, it SERVES THE WRONG HISTORY, and
// trimmed/belowWindow decide whether a read falls through to disk or is
// answered from memory. A miss is recoverable; this would be a lie.
//
// So the oracle is an UNSEEDED cache over the same aria: every read method
// must agree, row for row, LT for LT.

// unseededOpen builds a cache for id the way a cold process would: a fresh
// backend, no ancestor resident, nothing to seed from.
func unseededOpen(t *testing.T, dir, id string) []Entry[message.Message] {
	t.Helper()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	return l.Read()
}

func ltsOf(rows []Entry[message.Message]) []uint64 {
	out := make([]uint64, len(rows))
	for i, e := range rows {
		out[i] = e.FigaroLT
	}
	return out
}

func TestSeededForkAnswersIdenticallyToAnUnseededOne(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	lay, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	parent, _ := b.CreateConversation(lay)
	pIR, _ := b.OpenFigIR(parent)
	for i := 0; i < 200; i++ {
		if _, err := pIR.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("a record with text long enough to be worth sharing")},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	_, child, err := b.ForkAt(parent, 150)
	if err != nil {
		t.Fatal(err)
	}
	// THE CHILD MUST HAVE HISTORY OF ITS OWN AT CONSTRUCTION TIME, or the seam
	// is never exercised. A fork opened the instant it is cut owns nothing
	// above the base, so seed+own degenerates to seed and an off-by-one at the
	// seam is invisible -- proven: a deliberate +2 mutation passed this test
	// before this paragraph existed.
	cIR, err := b.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := cIR.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("the child's own record")},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	b.Close()

	// Reopen: the parent first, so it is RESIDENT and the child's open seeds
	// from it while the child also has records of its own to decode.
	b, err = NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.OpenFigIR(parent); err != nil {
		t.Fatal(err)
	}
	cIR, err = b.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	seeded := cIR.Read()
	seededAfterAppend := seeded
	fromLT := cIR.ReadFrom(100, 0)
	tailLen := cIR.Len()
	b.Close()

	unseeded := unseededOpen(t, dir, child)

	if len(seededAfterAppend) != len(unseeded) {
		t.Fatalf("seeded cache read %d rows, unseeded read %d\nseeded LTs: %v\nunseeded LTs: %v",
			len(seededAfterAppend), len(unseeded), ltsOf(seededAfterAppend), ltsOf(unseeded))
	}
	for i := range unseeded {
		if seededAfterAppend[i].FigaroLT != unseeded[i].FigaroLT {
			t.Fatalf("row %d: seeded LT %d, unseeded LT %d", i, seededAfterAppend[i].FigaroLT, unseeded[i].FigaroLT)
		}
		if textOf(seededAfterAppend[i]) != textOf(unseeded[i]) {
			t.Fatalf("row %d (LT %d): seeded text %q, unseeded text %q",
				i, unseeded[i].FigaroLT, textOf(seededAfterAppend[i]), textOf(unseeded[i]))
		}
	}
	if tailLen != len(unseeded) {
		t.Errorf("seeded Len() reported %d, the log holds %d: trimmed is wrong, and trimmed decides whether a read falls through to disk", tailLen, len(unseeded))
	}
	if len(seeded) == 0 || len(fromLT) == 0 {
		t.Fatalf("the fixture read nothing (seeded %d, ReadFrom %d); it cannot compare anything", len(seeded), len(fromLT))
	}
	// ReadFrom must agree with the same suffix of the unseeded read.
	var want []Entry[message.Message]
	for _, e := range unseeded {
		if e.FigaroLT >= 100 {
			want = append(want, e)
		}
	}
	if len(fromLT) != len(want) {
		t.Errorf("ReadFrom(100) returned %d rows on a seeded cache, %d on the log itself\nseeded: %v\nwant:   %v",
			len(fromLT), len(want), ltsOf(fromLT), ltsOf(want))
	}
}

// THE MEASUREMENT THE FIX EXISTS FOR, by identity: with the ancestor resident,
// the child's shared prefix must point at the ancestor's bytes.
func TestSeededForkSharesTheAncestorsStrings(t *testing.T) {
	b, parent, childA, childB := forkedPair(t)
	defer b.Close()

	pIR, _ := b.OpenFigIR(parent)
	p := pIR.Read()
	aIR, _ := b.OpenFigIR(childA)
	bIR, _ := b.OpenFigIR(childB)
	a, c := aIR.Read(), bIR.Read()

	byLT := func(rows []Entry[message.Message]) map[uint64]Entry[message.Message] {
		m := make(map[uint64]Entry[message.Message], len(rows))
		for _, e := range rows {
			m[e.FigaroLT] = e
		}
		return m
	}
	pm, am, cm := byLT(p), byLT(a), byLT(c)

	compared, shared := 0, 0
	for lt, pe := range pm {
		if lt > 150 { // above the fork base nothing is shared by construction
			continue
		}
		pt := textOf(pe)
		if pt == "" {
			continue
		}
		for _, child := range []map[uint64]Entry[message.Message]{am, cm} {
			ce, ok := child[lt]
			if !ok {
				continue
			}
			compared++
			if sameBytes(pt, textOf(ce)) {
				shared++
			}
		}
	}
	if compared == 0 {
		t.Fatal("compared nothing; the fixture cannot show sharing")
	}
	t.Logf("shared prefix, two children: %d strings compared, %d SHARED, %d MINTED", compared, shared, compared-shared)
	if shared != compared {
		t.Errorf("%d of %d prefix strings were MINTED by a fork whose ancestor is resident; seeding did not happen", compared-shared, compared)
	}
}
