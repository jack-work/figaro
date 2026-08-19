package store

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// durable-forms §12.7 says the fold must be TREE SURGERY, not marshal and
// unmarshal: allocation proportional to the patch, not to the bytes of the
// board being observed. It says so because the alternative is invisible on a
// small board and immediate on a large one, and "it has to be the design
// from the first line, because it is very hard to retrofit once the
// derivation speaks JSON internally."
//
// This measures the built thing against that claim. A libretto follows a
// source with `size` keys; each iteration patches ONE key and waits for the
// copy to carry it. If the fold marshalled the board, the per-fold cost
// would climb with size. If it applies the source's own patch to its own
// tree, it will not.
func benchLibrettoFold(b *testing.B, size int) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("fold", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		b.Fatal(err)
	}
	sourceID, err := be.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	big := make(map[string]string, size)
	for i := 0; i < size; i++ {
		big[fmt.Sprintf("key%d", i)] = fmt.Sprintf("value%d-%s", i, "padding padding padding")
	}
	if _, err := be.ApplyForm(sourceID, setPatch(big)); err != nil {
		b.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		want := fmt.Sprintf("v%d", i)
		if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{"moving": want})); err != nil {
			b.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			raw, ok := lib.State().Get("moving")
			if ok {
				var got string
				json.Unmarshal(raw, &got)
				if got == want {
					break
				}
			}
			if time.Now().After(deadline) {
				b.Fatal("the libretto never caught up")
			}
			time.Sleep(50 * time.Microsecond)
		}
	}
}

func BenchmarkLibrettoFoldSmallBoard(b *testing.B) { benchLibrettoFold(b, 10) }
func BenchmarkLibrettoFoldLargeBoard(b *testing.B) { benchLibrettoFold(b, 5000) }

// A BURST is where coalescing shows: the source takes N patches as fast as it
// can and the copy folds whatever is queued into one durable write. The
// number to watch is source patches per libretto record -- one means the
// fold is paying an fsync per source patch, which doubles a studied form's
// write cost.
func BenchmarkLibrettoFoldBurst(b *testing.B) { librettoBurst(b, 1) }

// Eight concurrent writers: the source's own group commit folds them into
// one fsync, so the libretto sees a RUN of events and can fold them into one
// record. This is where coalescing is supposed to earn its keep.
func BenchmarkLibrettoFoldBurstConcurrent(b *testing.B) { librettoBurst(b, 8) }

func librettoBurst(b *testing.B, writers int) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("burst", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		b.Fatal(err)
	}
	sourceID, err := be.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		b.Fatal(err)
	}
	startVersion := lib.Version()

	b.ReportAllocs()
	b.ResetTimer()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < b.N; i += writers {
				if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{
					fmt.Sprintf("moving%d", w): fmt.Sprintf("v%d", i),
				})); err != nil {
					return
				}
			}
		}(w)
	}
	wg.Wait()
	b.StopTimer()
	deadline := time.Now().Add(20 * time.Second)
	for lib.At() < uint64(b.N) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	records := float64(lib.Version() - startVersion)
	if records > 0 {
		b.ReportMetric(float64(b.N)/records, "source-patches/libretto-record")
	}
}

// THE FIFTY-OBSERVER STORM, which durable-forms §12.7 names as the case
// where the derivation's cost stops being theoretical: "50 x 12 MB per patch
// versus 50 x a few hundred bytes".
//
// One libretto per studied FORM is what answers it. Fifty observers share
// one copy, one fold and one goroutine, so a patch to the source is folded
// ONCE however many figaros are watching -- which is the whole reason the
// design reversed from one-libretto-per-figaro. This measures that the
// sharing is real: the per-patch cost must not move with the observer count.
func benchStorm(b *testing.B, observers int) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("storm", setPatch(map[string]string{"system.model": "m"}))
	if err != nil {
		b.Fatal(err)
	}
	sourceID, err := be.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < observers; i++ {
		watcher, err := be.CreateConversation(outfit)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := be.StudyForm(watcher, sourceID); err != nil {
			b.Fatal(err)
		}
	}
	lib, err := be.Libretto(sourceID)
	if err != nil {
		b.Fatal(err)
	}
	if got := lib.Refs(); got != observers {
		b.Fatalf("refs = %d, want %d observers", got, observers)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := be.ApplyForm(sourceID, setPatch(map[string]string{
			"moving": fmt.Sprintf("v%d", i),
		})); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	deadline := time.Now().Add(30 * time.Second)
	for lib.At() < uint64(b.N) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	open, obs := be.LibrettoStats()
	b.ReportMetric(float64(open), "librettos")
	b.ReportMetric(float64(obs), "observers")
}

func BenchmarkStorm1Observer(b *testing.B)   { benchStorm(b, 1) }
func BenchmarkStorm50Observers(b *testing.B) { benchStorm(b, 50) }
