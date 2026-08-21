package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
)

type failingLog struct {
	MemFormLog
	failSync atomic.Bool
	slow     atomic.Bool
	syncs    atomic.Int64
}

func (l *failingLog) SyncThrough(uint64) error {
	l.syncs.Add(1)
	if l.slow.Load() {
		// A real fsync is milliseconds. Without standing in for that, a
		// memory log drains faster than writers can submit and every write
		// forms its own batch, which measures the harness rather than the
		// batching.
		time.Sleep(2 * time.Millisecond)
	}
	if l.failSync.Load() {
		return fmt.Errorf("disk is gone")
	}
	return nil
}

func kv(k, v string) message.Patch {
	raw, _ := json.Marshal(v)
	return message.Patch{Set: map[string]json.RawMessage{k: raw}}
}

// A failed sync rejects the patch and leaves the published state exactly
// where it stood. Nothing is applied before the sync, so there is nothing to
// roll back, which is the property the whole ordering exists for.
func TestFailedSyncRejectsAndPublishesNothing(t *testing.T) {
	log := &failingLog{}
	f, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.Apply(kv("a", "1"), 0); err != nil {
		t.Fatal(err)
	}
	before, beforeVer := f.Snapshot()

	log.failSync.Store(true)
	if _, err := f.Apply(kv("b", "2"), 0); err == nil {
		t.Fatal("a failed sync was reported as success")
	}

	after, afterVer := f.Snapshot()
	if afterVer != beforeVer {
		t.Fatalf("version moved on a failed sync: %d -> %d", beforeVer, afterVer)
	}
	if _, ok := after.Get("b"); ok {
		t.Fatal("a patch that never synced is visible")
	}
	if _, ok := before.Get("a"); !ok {
		t.Fatal("the prior state was disturbed")
	}
}

// One sync per BATCH, not per patch. This is the number that says group
// commit works, and without it every writer pays a full fsync.
func TestBatchSyncsOnce(t *testing.T) {
	log := &failingLog{}
	log.slow.Store(true)
	f, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const writers = 32
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := f.Apply(kv(fmt.Sprintf("k%d", i), "v"), 0); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	if n := log.syncs.Load(); n >= writers {
		t.Fatalf("%d syncs for %d concurrent writes: nothing batched", n, writers)
	}
	snap, _ := f.Snapshot()
	if snap.Len() != writers {
		t.Fatalf("lost writes: %d keys for %d writers", snap.Len(), writers)
	}
}

// Two compare-and-swap writers in ONE batch must behave exactly as they do in
// two batches: the first wins, the second sees the moved version. Merging the
// batch's semantics would make the guard meaningless.
func TestBatchPreservesIfVersion(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	// A version of zero means UNCONDITIONAL, so the guard needs a form that
	// has already moved once to be testable at all.
	if _, err := f.Apply(kv("seed", "0"), 0); err != nil {
		t.Fatal(err)
	}
	v0 := f.Read().Version
	var okCount, movedCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := f.Apply(kv("contested", fmt.Sprint(i)), v0); err != nil {
				movedCount.Add(1)
			} else {
				okCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if okCount.Load() != 1 || movedCount.Load() != 1 {
		t.Fatalf("want exactly one winner and one refusal, got %d and %d",
			okCount.Load(), movedCount.Load())
	}
}

// A no-op is answered, not swallowed. A waiter that never wakes on a patch
// that changed nothing is the bug an optimistic client cannot recover from.
func TestNoopIsAnswered(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	if _, err := f.Apply(kv("a", "1"), 0); err != nil {
		t.Fatal(err)
	}
	v := f.Read().Version
	version, applied, err := f.ApplyEffect(kv("a", "1"), 0)
	if err != nil {
		t.Fatalf("a redundant set errored: %v", err)
	}
	if version != v {
		t.Fatalf("a no-op moved the version: %d -> %d", v, version)
	}
	if !applied.IsEmpty() {
		t.Fatal("a no-op reported an applied patch")
	}
}

// A panicking sink must not take the writer with it.
func TestSinkPanicIsContained(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	f.OnCommit(func(uint64, message.Patch) { panic("sink") })
	var saw atomic.Int64
	f.OnCommit(func(uint64, message.Patch) { saw.Add(1) })

	if _, err := f.Apply(kv("a", "1"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Apply(kv("b", "2"), 0); err != nil {
		t.Fatalf("the writer died with a sink: %v", err)
	}
	if saw.Load() != 2 {
		t.Fatalf("a panicking sink starved its neighbour: %d of 2", saw.Load())
	}
}

// Assert refuses a removal of a key that is not there; Ensure reduces it
// away. Same event either way when the key IS there.
func TestRemovalIntent(t *testing.T) {
	f := NewMemForm()
	defer f.Close()
	if _, err := f.Apply(kv("here", "1"), 0); err != nil {
		t.Fatal(err)
	}
	v := f.Read().Version

	rm := message.Patch{Remove: []string{"absent"}}
	if _, _, err := f.ApplyEffectIntent(rm, 0, Ensure); err != nil {
		t.Fatalf("ensure must reduce an absent removal away: %v", err)
	}
	if _, _, err := f.ApplyEffectIntent(rm, 0, Assert); err == nil {
		t.Fatal("assert must refuse a removal of a key that is not there")
	}
	if f.Read().Version != v {
		t.Fatal("a refusal moved the version")
	}

	real := message.Patch{Remove: []string{"here"}}
	if _, applied, err := f.ApplyEffectIntent(real, 0, Assert); err != nil {
		t.Fatalf("assert must allow a removal that removes: %v", err)
	} else if len(applied.Remove) != 1 {
		t.Fatalf("want one removal, got %v", applied.Remove)
	}
}

// Submit without Await is what removes a parked goroutine per writer: the
// write lands, the caller never waits, and the ticket is there if it changes
// its mind.
func TestSubmitWithoutAwaitStillLands(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	var tickets []Ticket
	for i := 0; i < 50; i++ {
		tk, err := f.Submit(kv(fmt.Sprintf("k%d", i), "v"), 0, Ensure)
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, tk)
	}
	// Await only the last: the broadcast that answers it has answered them
	// all, because the drainer answers a batch at a time and in order.
	if _, _, err := f.Await(context.Background(), tickets[len(tickets)-1]); err != nil {
		t.Fatal(err)
	}
	snap, _ := f.Snapshot()
	if snap.Len() != 50 {
		t.Fatalf("want 50 keys, got %d", snap.Len())
	}
}

// A ticket awaited after the fact returns the same verdict, and a cancelled
// context stops waiting without disturbing the write.
func TestAwaitIsIdempotentAndCancellable(t *testing.T) {
	f := NewMemForm()
	defer f.Close()

	tk, err := f.Submit(kv("a", "1"), 0, Ensure)
	if err != nil {
		t.Fatal(err)
	}
	v1, _, err := f.Await(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	v2, _, err := f.Await(context.Background(), tk)
	if err != nil || v1 != v2 {
		t.Fatalf("awaiting twice must answer the same: %d, %d, %v", v1, v2, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.Await(ctx, Ticket{}); err == nil {
		t.Fatal("a ticket-less await must fail")
	}
}

// The sync covers RECORDS, and the metric must say so: a batch of many
// writes in which one changed anything is one record, and calling that a
// batch of many would make the group-commit alarm read backwards.
func TestSyncCountsRecordsNotWrites(t *testing.T) {
	log := &failingLog{}
	log.slow.Store(true)
	f, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.Apply(kv("k", "settled"), 0); err != nil {
		t.Fatal(err)
	}
	before := log.syncs.Load()

	// Twenty writers, all setting the value the board already holds: every
	// one reduces to nothing, so the batch writes no record at all.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Apply(kv("k", "settled"), 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if n := log.syncs.Load(); n != before {
		t.Fatalf("a batch that wrote no record synced %d times", n-before)
	}
}
