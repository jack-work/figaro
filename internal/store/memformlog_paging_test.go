package store

// THE FLOOR, ASSERTED WHERE IT CAN FAIL.
//
// A mem log under a continuo is appended to for as long as the daemon lives.
// Unbounded growth there is not a slow leak, it is the whole memory of the
// process. These tests hold the three promises the paging makes: it is
// bounded, it never drops below the floor, and when it cannot answer it SAYS
// SO rather than answering short.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// setRecord is one real patch payload. THE SHAPE MATTERS: a form patch is
// {"object":{"Set":…}}, not {"set":…}, and a hand-written approximation
// unmarshals into an EMPTY patch that applies cleanly and changes nothing --
// which would let every assertion below pass over a log that had lost
// everything.
func setRecord(t *testing.T, key string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil)
	if p.IsIdentity() {
		t.Fatalf("built an identity patch for %s: the fixture is wrong, not the log", key)
	}
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	return payload
}

func setPage(t *testing.T, n int) {
	t.Helper()
	old := int(memFormPage.Load())
	SetMemFormPage(n)
	t.Cleanup(func() { SetMemFormPage(old) })
}

func appendN(t *testing.T, m *MemFormLog, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := m.AppendPatch(setRecord(t, "k", i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

func residentRecords(m *MemFormLog) int {
	st := m.load()
	n := len(st.live.patches)
	if st.sealed != nil {
		n += len(st.sealed.patches)
	}
	return n
}

// The bound is the reason the paging exists: a log appended to forever must
// not grow forever.
func TestMemFormLogIsBounded(t *testing.T) {
	setPage(t, 8)
	m := &MemFormLog{}
	appendN(t, m, 500)

	if got := residentRecords(m); got >= 2*8 {
		// [page, 2*page) is the invariant: at the instant the live page
		// reaches the floor it seals, so the pair never both stand full.
		t.Fatalf("retained %d records; the pair must stay under 2*page (%d)", got, 2*8)
	}
	if got := residentRecords(m); got < 8 {
		t.Fatalf("retained %d records, below the floor of %d: the whole point of "+
			"holding the predecessor is that the floor is never breached", got, 8)
	}
}

// THE FLOOR IS A PROMISE, not a tendency. At every append -- not merely at
// the end -- a reader positioned one floor back must still be answerable.
func TestMemFormLogNeverDropsBelowTheFloorMidStream(t *testing.T) {
	const page = 8
	setPage(t, page)
	m := &MemFormLog{}
	for i := 0; i < 200; i++ {
		if _, err := m.AppendPatch(setRecord(t, "k", i)); err != nil {
			t.Fatalf("append: %v", err)
		}
		top := m.load().live.top()
		if top < page {
			continue // not yet a floor's worth of history to promise
		}
		want := top - page
		if floor := m.Floor(); floor > want {
			t.Fatalf("at version %d the floor had risen to %d, above the %d a "+
				"reader one page back sits at: the predecessor was dropped early",
				top, floor, want)
		}
	}
}

// A range below the floor must be REFUSED. Answering with the tail that
// happens to survive is the failure mode this replaces: it is
// indistinguishable from a complete answer at the call site.
func TestMemFormLogRefusesBelowTheFloorInsteadOfAnsweringShort(t *testing.T) {
	setPage(t, 8)
	m := &MemFormLog{}
	appendN(t, m, 100)

	floor := m.Floor()
	if floor == 0 {
		t.Fatal("nothing was ever dropped, so this test proves nothing")
	}
	err := m.RangePatches(1, 0, func(uint64, []byte) error { return nil })
	if err != ErrBelowFloor {
		t.Fatalf("a read from version 1 with a floor at %d returned %v; it must "+
			"be ErrBelowFloor. A short answer is a lie the caller cannot detect", floor, err)
	}

	// And a read from ON the floor is the first answerable one.
	seen := 0
	if err := m.RangePatches(floor+1, 0, func(uint64, []byte) error { seen++; return nil }); err != nil {
		t.Fatalf("a read from the floor+1 must succeed, got %v", err)
	}
	if seen == 0 {
		t.Fatal("the floor is answerable by definition; nothing came back")
	}
}

// Versions are ABSOLUTE and survive rotation. A page boundary that renumbered
// would desync every mirror silently.
func TestMemFormLogVersionsAreAbsoluteAcrossPages(t *testing.T) {
	setPage(t, 4)
	m := &MemFormLog{}
	var last uint64
	for i := 0; i < 40; i++ {
		v, err := m.AppendPatch(setRecord(t, "k", i))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if v != last+1 {
			t.Fatalf("append %d returned version %d, wanted %d: versions must be "+
				"dense and absolute across a rotation", i, v, last+1)
		}
		last = v
	}
	var got []uint64
	if err := m.RangePatches(m.Floor()+1, 0, func(v uint64, _ []byte) error {
		got = append(got, v)
		return nil
	}); err != nil {
		t.Fatalf("range: %v", err)
	}
	for i, v := range got {
		if want := m.Floor() + uint64(i) + 1; v != want {
			t.Fatalf("record %d reported version %d, wanted %d", i, v, want)
		}
	}
}

// THE BASE IS THE POINT OF A PAGE OVER A RING. What was dropped is not lost;
// it is folded into a snapshot the log still holds, so replaying from the
// base reaches the same state as replaying from the beginning would have.
func TestMemFormLogBaseCarriesWhatWasDropped(t *testing.T) {
	setPage(t, 4)
	m := &MemFormLog{}
	// Each record sets a distinct key, so a dropped record that was NOT
	// folded into the base shows up as a missing key rather than as a
	// stale value.
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := m.AppendPatch(setRecord(t, fmt.Sprintf("k%d", i), i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	snap, version := m.Base()
	if version == 0 {
		t.Fatal("nothing rotated, so this test proves nothing")
	}
	if err := m.RangePatches(version+1, 0, func(_ uint64, payload []byte) error {
		var p message.Patch
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		snap = snap.Apply(p)
		return nil
	}); err != nil {
		t.Fatalf("range from base: %v", err)
	}
	for i := 0; i < n; i++ {
		if !snap.Has(fmt.Sprintf("k%d", i)) {
			t.Fatalf("key k%d is missing after replaying base+patches: a record was "+
				"dropped without being folded into the base, which is the one thing "+
				"a page must never do", i)
		}
	}
}

// A Form opened over a paged log must come up at the right state and the
// right version -- not at record 1, which no longer exists.
func TestOpenFormOverAPagedLogResumesFromTheBase(t *testing.T) {
	setPage(t, 4)
	m := &MemFormLog{}
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := m.AppendPatch(setRecord(t, fmt.Sprintf("k%d", i), i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	f, err := OpenForm(m)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	snap, version := f.Snapshot()
	if version != uint64(n) {
		t.Fatalf("opened at version %d, wanted %d", version, n)
	}
	for i := 0; i < n; i++ {
		if !snap.Has(fmt.Sprintf("k%d", i)) {
			t.Fatalf("key k%d missing from a form opened over a paged log", i)
		}
	}
	if floor := f.PatchFloor(); floor == 0 {
		t.Fatal("the form reports no floor over a log that has one")
	}
}

// PatchesBetween must not hand back a short prefix when it cannot answer.
func TestPatchesBetweenBelowTheFloorIsNilNotShort(t *testing.T) {
	setPage(t, 4)
	m := &MemFormLog{}
	for i := 0; i < 50; i++ {
		if _, err := m.AppendPatch(setRecord(t, fmt.Sprintf("k%d", i), i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	f, err := OpenForm(m)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if got := f.PatchesBetween(1, 50); got != nil {
		t.Fatalf("a read from version 1 under a floor of %d returned %d patches; "+
			"it must return nil. A short prefix reads at the call site as a "+
			"complete answer and surfaces later as a phantom gap",
			f.PatchFloor(), len(got))
	}
	if got := f.PatchesBetween(f.PatchFloor(), 50); len(got) == 0 {
		t.Fatal("a read from the floor itself must be answerable")
	}
}
