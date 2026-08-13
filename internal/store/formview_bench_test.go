package store

// What the PROVIDER pays, per Send, to learn what a form did between two
// stamps.
//
// This is the benchmark the copy hid. `BenchmarkFormPatches10000` measured the
// old API honestly -- and the old API was the wrong question. A translate asks
// "what changed between the last stamp and this one", whose answer is one
// patch or none; the accessor answered it by handing over a copy of the whole
// history and letting the caller walk to the end. A tool loop is many Sends,
// and a studying aria pays it once per observed form per Send, so the slope in
// (history length x observed forms) was the real cost and no benchmark named
// it.
//
// The two shapes measured here are the two that happen in production:
//
//   Delta: the warm case. Ask for the last patch of a form with N behind it.
//   Whole: the cold case, and the first sighting of a studied form.
//
// The names are identical in the pre-change tree (see the copy in
// perfbase/, which implements the same two questions against FormPatches), so
// benchstat compares like with like.

import (
	"fmt"
	"sync"
	"testing"
)

func benchFormWithHistory(b *testing.B, patches int) (*XwalBackend, string) {
	b.Helper()
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("perf", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		b.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < patches; i++ {
		if _, err := be.ApplyForm(id, patchSet(map[string]string{
			fmt.Sprintf("key%d", i%100): fmt.Sprintf("value%d", i),
		})); err != nil {
			b.Fatal(err)
		}
	}
	return be, id
}

// deltaPerSend is the warm translate: the transitions between the previous
// stamp and the current one.
func deltaPerSend(b *testing.B, history int) {
	be, id := benchFormWithHistory(b, history)
	version, err := be.FormVersion(id)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps, err := be.FormPatchesBetween(id, version-1, version)
		if err != nil {
			b.Fatal(err)
		}
		if len(ps) != 1 {
			b.Fatalf("want 1 patch, got %d", len(ps))
		}
	}
}

// wholePerSend is the cold translate and the first sighting of a studied
// form: everything up to the stamp.
func wholePerSend(b *testing.B, history int) {
	be, id := benchFormWithHistory(b, history)
	version, err := be.FormVersion(id)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps, err := be.FormPatchesBetween(id, 0, version)
		if err != nil {
			b.Fatal(err)
		}
		if len(ps) == 0 {
			b.Fatal("want the history, got nothing")
		}
	}
}

func BenchmarkFormDeltaPerSend100(b *testing.B)   { deltaPerSend(b, 100) }
func BenchmarkFormDeltaPerSend1000(b *testing.B)  { deltaPerSend(b, 1000) }
func BenchmarkFormDeltaPerSend10000(b *testing.B) { deltaPerSend(b, 10000) }

func BenchmarkFormWholePerSend100(b *testing.B)   { wholePerSend(b, 100) }
func BenchmarkFormWholePerSend1000(b *testing.B)  { wholePerSend(b, 1000) }
func BenchmarkFormWholePerSend10000(b *testing.B) { wholePerSend(b, 10000) }

// A studying aria pays the accessor cost once per observed form per Send.
// Fifty is the storm harness's number, and the slope in it is what decides
// whether a figaro can watch many things or few.
func studiedSetPerSend(b *testing.B, observed, history int) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	ids := make([]string, observed)
	for f := range ids {
		id, _, err := be.CreateForm("", patchSet(map[string]string{"brief": "v0"}))
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < history; i++ {
			if _, err := be.ApplyForm(id, patchSet(map[string]string{
				"brief": fmt.Sprintf("v%d", i+1),
			})); err != nil {
				b.Fatal(err)
			}
		}
		ids[f] = id
	}
	versions := make([]uint64, observed)
	for i, id := range ids {
		v, err := be.FormVersion(id)
		if err != nil {
			b.Fatal(err)
		}
		versions[i] = v
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for f, id := range ids {
			if _, err := be.FormPatchesBetween(id, versions[f]-1, versions[f]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkStudiedSetPerSend8x500(b *testing.B)  { studiedSetPerSend(b, 8, 500) }
func BenchmarkStudiedSetPerSend50x500(b *testing.B) { studiedSetPerSend(b, 50, 500) }

// accessorRange is THE SEAM: the one place a benchmark or probe in this
// package asks "what did this form do between these two stamps". The
// pre-change tree implements the same function against FormPatches plus a
// forward cursor, so every measurement built on it compares like with like
// when the internals swap.
//
// The seam is the lesson of the chalkboard->form migration, where three
// one-line bodies let an entire before/after suite recompile unchanged: if
// call sites scatter the literal API, the AFTER is not comparable to the
// BEFORE and the exercise is theatre.
func accessorRange(be *XwalBackend, id string, after, upTo uint64) ([]VersionedPatch, error) {
	return be.FormPatchesBetween(id, after, upTo)
}

// What a form patch costs, and how it scales with concurrency. The number to
// watch is not ns/op alone but SYNCS PER PATCH: group commit is the reason a
// mandatory fsync is affordable, and if the batch stops batching this is
// where it shows.
func applyContended(b *testing.B, writers int) {
	be, id := benchFormWithHistory(b, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				if _, err := be.ApplyForm(id, patchSet(map[string]string{
					fmt.Sprintf("k%d", w): fmt.Sprintf("v%d", i),
				})); err != nil {
					b.Error(err)
				}
			}(w)
		}
		wg.Wait()
	}
	b.ReportMetric(float64(writers), "writers")
}

func BenchmarkFormApplyContended1(b *testing.B)   { applyContended(b, 1) }
func BenchmarkFormApplyContended8(b *testing.B)   { applyContended(b, 8) }
func BenchmarkFormApplyContended64(b *testing.B)  { applyContended(b, 64) }
func BenchmarkFormApplyContended256(b *testing.B) { applyContended(b, 256) }

// Independent forms must stay independent: one lock per form, one drainer
// per form, and nothing shared but the store.
func BenchmarkFormApplyManyForms(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	const forms = 16
	ids := make([]string, forms)
	for i := range ids {
		id, _, err := be.CreateForm("", patchSet(map[string]string{"seed": "0"}))
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = id
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for f := range ids {
			wg.Add(1)
			go func(f int) {
				defer wg.Done()
				if _, err := be.ApplyForm(ids[f], patchSet(map[string]string{
					"k": fmt.Sprintf("v%d", i),
				})); err != nil {
					b.Error(err)
				}
			}(f)
		}
		wg.Wait()
	}
}
