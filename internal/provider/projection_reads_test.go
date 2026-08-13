package provider_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// countingForm records what the projection actually asked the store for.
// The counter is the artifact, not the timing: a duration decays with the
// machine, a read count states the shape and fails when the shape changes.
type countingForm struct {
	patches []message.Patch
	calls   int
	spanned int // total versions covered by the ranges asked for
}

func (f *countingForm) PatchesBetween(after, upTo uint64) []message.Patch {
	f.calls++
	if upTo > uint64(len(f.patches)) {
		upTo = uint64(len(f.patches))
	}
	if after >= upTo {
		return nil
	}
	f.spanned += int(upTo - after)
	return f.patches[after:upTo]
}

// projectOnce builds a log of n entries, each advancing the board by one
// patch, and pre-populates the translation cache for the first `cached` of
// them. Returns the accessor so the caller can read the counters.
func projectOnce(t testing.TB, n, cached, observed int) (*countingForm, provider.ProjectionStats) {
	t.Helper()

	board := make([]message.Patch, n)
	for i := range board {
		board[i] = message.Patch{Set: map[string]json.RawMessage{
			"mode": json.RawMessage(fmt.Sprintf(`"v%d"`, i)),
		}}
	}
	acc := &countingForm{patches: board}

	studies := map[string]provider.Form{}
	studyAccs := map[string]*countingForm{}
	for i := 0; i < observed; i++ {
		id := fmt.Sprintf("@obs%02d", i)
		a := &countingForm{patches: board}
		studies[id], studyAccs[id] = a, a
	}

	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	const fingerprint = "fp"
	for i := 0; i < n; i++ {
		versions := map[string]uint64{}
		for id := range studies {
			versions[id] = uint64(i + 1)
		}
		e, err := log.Append(store.Entry[message.Message]{
			Payload:            message.Message{Role: message.RoleInput},
			FormChannelVersion: uint64(i + 1),
			StudyVersions:      versions,
		})
		if err != nil {
			t.Fatal(err)
		}
		if i < cached {
			if _, err := cache.Append(store.Entry[[]json.RawMessage]{
				FigaroLT:    e.LT,
				Payload:     []json.RawMessage{json.RawMessage(`{"cached":true}`)},
				Fingerprint: fingerprint,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	_, stats, err := provider.ProjectIncrementally(provider.ProjectionConfig[int]{
		Log:         log,
		Cache:       cache,
		Form:        acc,
		Studies:     studies,
		Fingerprint: fingerprint,
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
		Append: func(s int, _ []json.RawMessage, _ uint64) int { return s + 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range studyAccs {
		acc.calls += a.calls
	}
	return acc, stats
}

// A record whose bytes are cached costs no read, at any layer. This is the
// whole change, stated as an assertion rather than as a benchmark.
func TestCachedRecordsCostNoReads(t *testing.T) {
	acc, stats := projectOnce(t, 100, 100, 8)
	if stats.Cached != 100 || stats.Encoded != 0 {
		t.Fatalf("want 100 cached and 0 encoded, got %d and %d", stats.Cached, stats.Encoded)
	}
	if acc.calls != 0 {
		t.Fatalf("a fully cached pass read the board %d times; it must read nothing", acc.calls)
	}
}

// The reads a partly cached pass does make are bounded by the misses, not by
// the length of the log. The board catch-up over the cached prefix is ONE
// range, whatever the prefix length.
func TestReadsScaleWithMissesNotLength(t *testing.T) {
	short, _ := projectOnce(t, 100, 90, 0)
	long, _ := projectOnce(t, 1000, 990, 0)

	if short.calls != long.calls {
		t.Fatalf("reads tracked log length: %d at 100 entries, %d at 1000, with 10 misses either way",
			short.calls, long.calls)
	}
	// One catch-up over the skipped prefix, then one per miss.
	if want := 1 + 10; short.calls != want {
		t.Fatalf("want %d reads for 10 misses, got %d", want, short.calls)
	}
}

// Studied forms are read only for records that are actually encoded. They
// feed nothing but the rendering, so a cache hit must not touch them: that
// cost was one read per observed form per record, and fifty observers was
// fifty-one reads to produce nothing.
func TestObservedFormsAreNotReadForCachedRecords(t *testing.T) {
	none, _ := projectOnce(t, 100, 100, 50)
	if none.calls != 0 {
		t.Fatalf("50 observed forms cost %d reads over a fully cached pass", none.calls)
	}
}

// The board still has to be correct at the first uncached record, which is
// what the catch-up buys and what a naive cache-first ordering breaks.
func TestSnapshotIsCurrentAtTheFirstMiss(t *testing.T) {
	board := []message.Patch{
		{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"old"`)}},
		{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"new"`)}},
	}
	acc := &countingForm{patches: board}

	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	const fingerprint = "fp"
	for i := 0; i < 2; i++ {
		e, err := log.Append(store.Entry[message.Message]{
			Payload:            message.Message{Role: message.RoleInput},
			FormChannelVersion: uint64(i + 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := cache.Append(store.Entry[[]json.RawMessage]{
				FigaroLT: e.LT, Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: fingerprint,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	var seen form.Snapshot
	if _, _, err := provider.ProjectIncrementally(provider.ProjectionConfig[int]{
		Log: log, Cache: cache, Form: acc, Fingerprint: fingerprint,
		Encode: func(_ message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			seen = snap
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
		Append: func(s int, _ []json.RawMessage, _ uint64) int { return s + 1 },
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := seen.Get("mode")
	if !ok || string(got) != `"old"` {
		t.Fatalf("the encode after a cached record saw mode=%s; the skipped patch was never folded", got)
	}
}

func benchProject(b *testing.B, n, cached, observed int) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		projectOnce(b, n, cached, observed)
	}
}

// The shape a fingerprint bump or a rejected watermark produces: a cold walk
// over a log whose per-LT bytes are still valid.
func BenchmarkColdWalkWarmCache(b *testing.B)          { benchProject(b, 500, 500, 0) }
func BenchmarkColdWalkWarmCache8Observed(b *testing.B) { benchProject(b, 500, 500, 8) }
func BenchmarkColdWalkColdCache(b *testing.B)          { benchProject(b, 500, 0, 8) }
