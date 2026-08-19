package store

import (
	"fmt"
	"strings"
	"testing"
)

// THE GATE FOR (c): DO HOPS RE-DECODE?
//
// The layered-cache design gates `mem` under fig IR on one number and says so
// in its own words: "DO NOT BUILD WITHOUT DATA... the hop/scroll fall-through
// count. Build mem under fig IR only if that number says hops re-decode."
//
// Phase 3 found forest's three candidate jobs already taken -- sharing by the
// seed, bounding by the window, serving-what-nobody-holds by the pass-through
// -- leaving exactly one case: a bounded NON-SCAN re-read of a cold range. A
// hop, not a scroll. A cache pays for itself there only if the SAME cold range
// is asked for more than once; a hop to a range visited once is a miss no
// cache can prevent.
//
// So the measurement is not "how many fall-throughs" but "how many fall-
// throughs are REPEATS". That is the only number a cache can remove.
//
// PARAMETERS ARE THE OWNER'S, NOT INVENTED: ir_window_mb = 4 in the live
// config, byte-budgeted, no row window; a ~2500-message aria with short prose
// at the head and large tool results at the tail, which is the skew the config
// comment records from a real 2556-message aria.

// rangeRecorder counts what the layer below is asked for AND which range,
// so repeats can be told from first visits. A count alone cannot answer the
// gate's question.
type rangeRecorder[T any] struct {
	Log[T]
	calls []string
	seen  map[string]int
}

func newRangeRecorder[T any](inner Log[T]) *rangeRecorder[T] {
	return &rangeRecorder[T]{Log: inner, seen: map[string]int{}}
}

func (r *rangeRecorder[T]) note(k string) {
	r.calls = append(r.calls, k)
	r.seen[k]++
}

func (r *rangeRecorder[T]) Read() []Entry[T] {
	r.note("Read(all)")
	return r.Log.Read()
}

func (r *rangeRecorder[T]) ReadFrom(lt uint64, n int) []Entry[T] {
	r.note(fmt.Sprintf("ReadFrom(%d,%d)", lt, n))
	return r.Log.ReadFrom(lt, n)
}

func (r *rangeRecorder[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	r.note(fmt.Sprintf("ReadPage(%d,%d,%d)", from, before, n))
	return r.Log.ReadPage(from, before, n)
}

// repeats is the number of fall-throughs a cache could have removed: every
// call after the first for the same range.
func (r *rangeRecorder[T]) repeats() int {
	n := 0
	for _, c := range r.seen {
		if c > 1 {
			n += c - 1
		}
	}
	return n
}

// realisticAria mimics the skew the config comment records: short prose at the
// head, large tool results toward the tail.
func realisticAria(t *testing.T, n int) (*cachedLog[string], *rangeRecorder[string]) {
	t.Helper()
	inner := NewMemLog[string]()
	for i := 1; i <= n; i++ {
		size := 200
		if i > n/2 && i%3 == 0 {
			size = 24 << 10 // a tool result
		}
		if _, err := inner.Append(Entry[string]{Payload: strings.Repeat("x", size)}); err != nil {
			t.Fatal(err)
		}
	}
	rec := newRangeRecorder[string](inner)
	c := newWindowedLog[string](rec, 0, 4<<20, 1, func(e Entry[string]) int { return len(e.Payload) + 48 })
	return c, rec
}

func TestHopGate_DoHopsReDecode(t *testing.T) {
	const total = 2500
	c, rec := realisticAria(t, total)

	resident := len(c.load().rows)
	if resident == 0 || resident >= total {
		t.Fatalf("the window holds %d of %d rows; this fixture cannot measure a fall-through", resident, total)
	}
	firstResident := c.load().rows[0].FigaroLT
	t.Logf("window: %d of %d rows resident at 4 MiB, oldest resident LT %d", resident, total, firstResident)

	// VACUITY GUARD: a warm read must cost the layer below NOTHING, or the
	// fixture is measuring "nothing crashed" rather than residency.
	// NOTE, and it is a policy finding in its own right: ReadPage(from, 0, n)
	// on a TRIMMED window always falls through -- `before == 0` means "to the
	// end of history", which the cache refuses to answer from a partial
	// window. So a warm read must name its upper bound.
	base := len(rec.calls)
	c.ReadPage(firstResident, firstResident+20, 20)
	if len(rec.calls) != base {
		t.Fatalf("a warm read fell through (%v); the fixture is not measuring residency", rec.calls[base:])
	}

	// --- SCROLL: page backward through cold history, once, as a user does
	// when walking up a long transcript.
	scrollStart := len(rec.calls)
	const page = 20
	for lt := firstResident; lt > uint64(page); lt -= page {
		c.ReadPage(lt-page, lt, page)
	}
	scroll := len(rec.calls) - scrollStart
	scrollRepeats := rec.repeats()
	t.Logf("SCROLL: %d fall-throughs, %d of them repeats", scroll, scrollRepeats)

	// --- HOP: revisit a few distant anchors, the way a reader jumps to a
	// search hit, returns to the tail, and comes back.
	c2, rec2 := realisticAria(t, total)
	firstResident2 := c2.load().rows[0].FigaroLT
	anchors := []uint64{120, 640, 1180}
	for round := 0; round < 3; round++ {
		for _, a := range anchors {
			c2.ReadPage(a, a+page, page) // the hop
		}
		c2.ReadPage(firstResident2, firstResident2+page, page) // back to the warm tail
	}
	t.Logf("HOP: %d fall-throughs over 3 rounds x %d anchors, %d of them REPEATS",
		len(rec2.calls), len(anchors), rec2.repeats())
	for k, n := range rec2.seen {
		if n > 1 {
			t.Logf("  repeated range %s: %d times", k, n)
		}
	}

	// --- SCROLL UP THEN BACK DOWN, and READ THE CAVEAT BELOW BEFORE CITING IT.
	//
	// This arm measures THIS LAYER's behaviour when the same ranges are asked
	// for twice. It does NOT describe the product: the CLI client holds folded
	// ranges in its own aria.Store and Ensure fetches only HOLES
	// (internal/livelog/aria/store.go), with no eviction, so a reader who
	// overshoots and comes back is served client-side and the daemon never
	// hears the second request. The arm is kept because it isolates the
	// question -- given a repeat, does this layer re-decode? -- and because
	// writing it down is what caught the assumption.
	c3, rec3 := realisticAria(t, total)
	firstResident3 := c3.load().rows[0].FigaroLT
	var walked []uint64
	for lt, i := firstResident3, 0; i < 5 && lt > uint64(page); i++ {
		c3.ReadPage(lt-page, lt, page)
		walked = append(walked, lt)
		lt -= page
	}
	for i := len(walked) - 1; i >= 0; i-- {
		c3.ReadPage(walked[i]-page, walked[i], page) // back down over the same rows
	}
	t.Logf("SCROLL UP THEN BACK DOWN: %d fall-throughs, %d of them REPEATS of a range already decoded",
		len(rec3.calls), rec3.repeats())

	// THE FACT THIS PINS, in the direction it is true today: given a repeated
	// request for a cold range, this layer decodes it again, every time. There
	// is no memo below the window; that is what a `mem` layer would add.
	if rec2.repeats() == 0 {
		t.Fatal("a repeated cold-range request cost the layer below nothing: " +
			"something now memoizes below the window, and the gate for (c) " +
			"must be re-asked rather than assumed")
	}
	t.Logf("VERDICT, in two parts:\n"+
		"  (1) THIS LAYER re-decodes a repeated cold range: %d of %d hop reads were repeats,\n"+
		"      each one a full decode. A mem layer under fig IR would remove exactly these.\n"+
		"  (2) THE PRODUCT does not generate them on the dominant path: the CLI client\n"+
		"      retains folded ranges and refetches only holes, so scroll-back is served\n"+
		"      client-side. The repeats that survive are ACROSS clients and processes --\n"+
		"      a reattach, a second pane, `fig show` -- where the window is cold anyway.\n"+
		"  Therefore locality does NOT justify mem under fig IR on this evidence.",
		rec2.repeats(), len(rec2.calls))
}
