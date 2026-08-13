package store

// The view's three properties, pinned.
//
// This file exists because the change it guards is a change of KIND, not of
// speed: Patches() copied the whole history and handed over ownership;
// PatchesBetween answers a range with a window onto memory the writer still
// owns. Faster, and it can be wrong in ways a copy could not be. So: it must
// answer exactly what the old walk answered, it must not let a caller scribble
// on the writer, and its cost must not track the history it is drawn from.
//
// The differential test is the one that matters. "Pin the identity with a
// verbatim-old-implementation differential test before you optimize it" is
// advice from the aria that did the allocation surgery on the render
// primitives, and it is right: a benchmark that is faster and wrong is the
// worst outcome available here.

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// oldPatchesThenWalk is the pre-view path, verbatim in behaviour: copy the
// entire published history out of the form (Form.Patches), then walk a fresh
// forward cursor to the absolute range (figaro.patchCursor).
func oldPatchesThenWalk(f *Form, after, upTo uint64) []VersionedPatch {
	all := append([]VersionedPatch(nil), f.state.Load().patches...)
	i := 0
	for i < len(all) && all[i].Version <= after {
		i++
	}
	var out []VersionedPatch
	for i < len(all) && all[i].Version <= upTo {
		out = append(out, all[i])
		i++
	}
	return out
}

func formWithPatches(t testing.TB, n int) *Form {
	t.Helper()
	f := NewMemForm()
	t.Cleanup(f.Close)
	for i := 0; i < n; i++ {
		raw, err := json.Marshal(fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Apply(message.Patch{Set: map[string]json.RawMessage{
			fmt.Sprintf("key%d", i%7): raw,
		}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// TestFormPatchesBetweenMatchesOldWalk is the differential: for every range a
// projection can ask for, including the degenerate and out-of-bounds ones, the
// view answers exactly what copy-then-walk answered.
func TestFormPatchesBetweenMatchesOldWalk(t *testing.T) {
	const n = 64
	f := formWithPatches(t, n)
	version := f.Read().Version

	for after := uint64(0); after <= version+2; after++ {
		for upTo := uint64(0); upTo <= version+2; upTo++ {
			want := oldPatchesThenWalk(f, after, upTo)
			got := f.PatchesBetween(after, upTo)
			if len(want) != len(got) {
				t.Fatalf("(%d,%d]: old walk gave %d patches, view gave %d",
					after, upTo, len(want), len(got))
			}
			for i := range want {
				if want[i].Version != got[i].Version {
					t.Fatalf("(%d,%d] at %d: old walk gave version %d, view gave %d",
						after, upTo, i, want[i].Version, got[i].Version)
				}
			}
		}
	}
}

// TestFormPatchesBetweenIsAViewNotACopy states the property the whole change
// rests on, and the guard that makes it safe to hand out.
func TestFormPatchesBetweenIsAViewNotACopy(t *testing.T) {
	f := formWithPatches(t, 32)
	published := f.state.Load().patches

	got := f.PatchesBetween(3, 6)
	if len(got) != 3 {
		t.Fatalf("want 3 patches, got %d", len(got))
	}
	if &got[0] != &published[3] {
		t.Fatal("the range was copied: it must alias the published array")
	}

	// The cap is the guard: appending to the answer must reallocate rather
	// than write into the slot the writer's next commit will use.
	if cap(got) != len(got) {
		t.Fatalf("range is not capped: len %d, cap %d", len(got), cap(got))
	}
	nextSlot := &published[6]
	got = append(got, VersionedPatch{Version: 999})
	if &got[0] == &published[3] {
		t.Fatal("append landed in the writer's array")
	}
	if nextSlot.Version == 999 {
		t.Fatal("append overwrote the writer's next patch")
	}
}

// TestFormPatchesBetweenCostIsWindowNotHistory is the regression alarm as a
// test rather than as a metric: the delta read must cost the delta, whatever
// is behind it. A counter of this shape outlives any benchmark, because it
// fails in CI instead of drifting in a report nobody reruns.
func TestFormPatchesBetweenCostIsWindowNotHistory(t *testing.T) {
	var last float64
	for _, history := range []int{100, 1000, 10000} {
		f := formWithPatches(t, history)
		version := f.Read().Version
		allocs := testing.AllocsPerRun(200, func() {
			if got := f.PatchesBetween(version-1, version); len(got) != 1 {
				t.Fatalf("history %d: want 1 patch, got %d", history, len(got))
			}
		})
		if allocs > 1 {
			t.Fatalf("history %d: a one-patch read allocated %.1f times; the view must not copy",
				history, allocs)
		}
		if last != 0 && allocs > last {
			t.Fatalf("history %d: allocations grew with history (%.1f -> %.1f)",
				history, last, allocs)
		}
		last = allocs
	}
}

// TestFormPatchesBetweenUnderConcurrentWrites drives the read against the
// writer, which is the arrangement the view legitimises: readers holding
// windows onto an array a writer is appending to. Meaningful under -race.
func TestFormPatchesBetweenUnderConcurrentWrites(t *testing.T) {
	f := formWithPatches(t, 8)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			raw, _ := json.Marshal(fmt.Sprintf("w%d", i))
			_, _ = f.Apply(message.Patch{Set: map[string]json.RawMessage{"hot": raw}}, 0)
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				v := f.Read().Version
				for _, p := range f.PatchesBetween(0, v) {
					if p.Version > v {
						t.Errorf("range leaked a patch past its upper bound: %d > %d", p.Version, v)
						return
					}
				}
			}
		}()
	}
	close(stop)
	wg.Wait()
}

// Trimming must not lose an answer: a range below the resident window is
// re-read from the log and must match what the window would have said.
func TestPatchesBelowTheWindowComeFromTheLog(t *testing.T) {
	SetPatchWindow(8)
	t.Cleanup(func() { SetPatchWindow(2048) })

	f := formWithPatches(t, 400)
	ps := f.state.Load().patches
	if len(ps) > 8+patchSlack {
		t.Fatalf("window not enforced: %d resident", len(ps))
	}
	if ps[0].Version <= 1 {
		t.Skip("nothing was trimmed; raise the patch count")
	}

	// A range entirely below the window.
	got := f.PatchesBetween(0, 5)
	if len(got) != 5 {
		t.Fatalf("want 5 patches from the log, got %d", len(got))
	}
	for i, p := range got {
		if p.Version != uint64(i+1) {
			t.Fatalf("at %d: version %d", i, p.Version)
		}
	}

	// A range straddling the boundary.
	straddle := f.PatchesBetween(ps[0].Version-3, ps[0].Version+1)
	if len(straddle) != 4 {
		t.Fatalf("straddling the window edge: want 4, got %d", len(straddle))
	}

	// And the hot path is still a view.
	v := f.Read().Version
	if hot := f.PatchesBetween(v-1, v); len(hot) != 1 {
		t.Fatalf("the window must still answer its own range: got %d", len(hot))
	}
}
