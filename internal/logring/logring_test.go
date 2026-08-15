package logring

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func newTestRing(capacity int, keep Keep) (*Ring, *bytes.Buffer) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return New(inner, capacity, keep), &buf
}

// Retention must never replace emission. A ring that swallowed log lines
// would lose them exactly when the process dies, which is when you want them.
func TestEverythingIsStillEmitted(t *testing.T) {
	ring, out := newTestRing(8, AtLeast(slog.LevelWarn))
	log := slog.New(ring)

	log.Debug("quiet")
	log.Info("ordinary")
	log.Warn("trouble")

	for _, want := range []string{"quiet", "ordinary", "trouble"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%q never reached the inner handler", want)
		}
	}
	if got := ring.Recent(0, nil); len(got) != 1 || got[0].Msg != "trouble" {
		t.Errorf("retained %v, want only the warning", got)
	}
}

// The default policy is why the ring is free: nothing is copied until
// something goes wrong.
func TestQuietOperationRetainsNothing(t *testing.T) {
	ring, _ := newTestRing(8, nil) // nil => AtLeast(WARN)
	log := slog.New(ring)
	for i := 0; i < 1000; i++ {
		log.Info("log opened", "dir", "/some/path")
	}
	if got := ring.Recent(0, nil); len(got) != 0 {
		t.Errorf("retained %d records in healthy operation, want 0", len(got))
	}
}

// A subsystem opts one INFO stream in by message, without lowering the bar
// for everything else. This is how a SUCCESSFUL provider round-trip survives
// to be compared against the one that was refused.
func TestMessageOptInKeepsAnInfoStream(t *testing.T) {
	ring, _ := newTestRing(16, Any(AtLeast(slog.LevelWarn), WithMessage("provider round-trip")))
	log := slog.New(ring)

	log.Info("log opened", "dir", "/x")
	log.Info("provider round-trip", "status", 200, "aria", "94f0752b")
	log.Warn("something else")

	got := ring.Recent(0, func(e Entry) bool { return e.Msg == "provider round-trip" })
	if len(got) != 1 {
		t.Fatalf("retained %d round-trips, want 1", len(got))
	}
	if got[0].Attrs["aria"] != "94f0752b" {
		t.Errorf("attrs lost: %v", got[0].Attrs)
	}
	if got[0].Attrs["status"] != int64(200) {
		t.Errorf("status = %#v, want int64(200)", got[0].Attrs["status"])
	}
}

// Attributes must be COPIED. A Handler may not retain a Record: the
// attributes past the inline few live in a slice the record does not own, and
// slog reuses it. Retaining without copying reads as corruption later.
func TestAttrsSurviveRecordReuse(t *testing.T) {
	ring, _ := newTestRing(16, AtLeast(slog.LevelInfo))
	log := slog.New(ring)

	// More attrs than a Record holds inline, twice, to force the spill path.
	for i := 0; i < 2; i++ {
		log.Info("wide", "a", i, "b", i, "c", i, "d", i, "e", i, "f", i, "g", i)
	}
	got := ring.Recent(0, nil)
	if len(got) != 2 {
		t.Fatalf("retained %d", len(got))
	}
	if got[0].Attrs["g"] != int64(0) || got[1].Attrs["g"] != int64(1) {
		t.Errorf("spilled attrs did not survive: %v / %v", got[0].Attrs, got[1].Attrs)
	}
}

// WithAttrs/WithGroup produce derived handlers; they must share one buffer, or
// a subsystem that scopes its logger retains into a ring nobody can read.
func TestDerivedHandlersShareTheRing(t *testing.T) {
	ring, _ := newTestRing(16, AtLeast(slog.LevelWarn))
	base := slog.New(ring)
	scoped := base.With("aria", "94f0752b")

	base.Warn("from base")
	scoped.Warn("from scoped")

	got := ring.Recent(0, nil)
	if len(got) != 2 {
		t.Fatalf("retained %d, want 2 - derived handlers must share the store", len(got))
	}
	var found bool
	for _, e := range got {
		if e.Msg == "from scoped" && e.Attrs["aria"] == "94f0752b" {
			found = true
		}
	}
	if !found {
		t.Errorf("WithAttrs prefix not retained: %v", got)
	}
}

func TestRingWrapsAndReportsOldestFirst(t *testing.T) {
	ring, _ := newTestRing(4, AtLeast(slog.LevelInfo))
	log := slog.New(ring)
	for i := 0; i < 10; i++ {
		log.Info("n", "i", i)
	}
	got := ring.Recent(0, nil)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("out of order at %d: %v", i, got)
		}
	}
	if got[len(got)-1].Attrs["i"] != int64(9) {
		t.Errorf("newest = %v, want i=9", got[len(got)-1].Attrs)
	}
	// Seq is monotonic across the ring's whole life: the gap between the
	// oldest retained seq and 1 is what retention dropped.
	if got[0].Seq != 7 {
		t.Errorf("oldest seq = %d, want 7", got[0].Seq)
	}
	if n := len(ring.Recent(2, nil)); n != 2 {
		t.Errorf("Recent(2) = %d", n)
	}
}

func TestStatsReportTheBound(t *testing.T) {
	ring, _ := newTestRing(4, AtLeast(slog.LevelInfo))
	log := slog.New(ring)
	for i := 0; i < 6; i++ {
		log.Info("n")
	}
	retained, capacity, seq := ring.Stats()
	if retained != 4 || capacity != 4 || seq != 6 {
		t.Errorf("Stats() = %d/%d seq=%d, want 4/4 seq=6", retained, capacity, seq)
	}
}

// slog handlers are called from every goroutine in the process. The race
// detector makes this test worth its runtime.
func TestConcurrentUse(t *testing.T) {
	ring, _ := newTestRing(64, AtLeast(slog.LevelInfo))
	log := slog.New(ring)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				log.Info("concurrent", "g", i, "j", j)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			_ = ring.Recent(10, nil)
			_, _, _ = ring.Stats()
		}
	}()
	wg.Wait()
	if n := len(ring.Recent(0, nil)); n != 64 {
		t.Errorf("retained %d, want the capacity 64", n)
	}
}

func TestEnabledDefersToInner(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	ring := New(inner, 8, AtLeast(slog.LevelDebug))
	if ring.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("ring must not enable a level the inner handler rejects")
	}
	if !ring.Enabled(context.Background(), slog.LevelError) {
		t.Error("ring must pass through an enabled level")
	}
}
