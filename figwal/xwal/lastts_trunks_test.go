package xwal

import (
	"path/filepath"
	"testing"
)

// Trunks.LastTS answers per-trunk recency without the caller holding a
// handle: the newest stamp across ALL the trunk's channels, zero for
// pre-timestamp trunks, and independent per trunk.
func TestTrunksLastTS(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1_000)
	f, err := createTrunks(filepath.Join(t.TempDir(), "f"), clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	a, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	clk.set(2_000)
	if _, _, err := f.Append(a, 0, []byte(`"a"`), nil); err != nil {
		t.Fatal(err)
	}
	clk.set(9_000)
	patch, _ := MapSetPatch([]string{"k"}, []byte(`1`))
	if _, err := f.AppendChannel(string(b), "chalkboard", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.LastTS(a); got != 2_000 {
		t.Errorf("LastTS(a) = %d, want 2000", got)
	}
	if got := f.LastTS(b); got != 9_000 {
		t.Errorf("LastTS(b) = %d, want 9000 (the unkeyed channel counts)", got)
	}
	if got := f.LastTS("no-such-trunk"); got != 0 {
		t.Errorf("LastTS(unknown) = %d, want 0", got)
	}
}

// The cold path is a bounded tail probe — no Head, no Open: a fresh
// Trunks (fresh registry) answers LastTS correctly from segment tails
// alone, and a later append through a real handle advances the same
// retained counter.
func TestLastTSColdProbeThenAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	clk := &fakeClock{}
	clk.set(1_000)
	f, err := createTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	clk.set(4_000)
	if _, _, err := f.Append(a, 0, []byte(`"x"`), nil); err != nil {
		t.Fatal(err)
	}
	// Seal to disk so the probe has segments to read.
	x, err := f.Head(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.SyncCoherent(); err != nil {
		t.Fatal(err)
	}
	x.Close()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	g, err := openTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, g)
	if got := g.LastTS(a); got != 4_000 {
		t.Fatalf("cold probe LastTS = %d, want 4000", got)
	}
	clk.set(9_000)
	if _, _, err := g.Append(a, 0, []byte(`"y"`), nil); err != nil {
		t.Fatal(err)
	}
	if got := g.LastTS(a); got != 9_000 {
		t.Fatalf("LastTS after append = %d, want 9000 (append and probe share the counter)", got)
	}
}

// The warm path allocates nothing: the counter is retained in the
// registry, so a listing over N nodes is N map lookups and N atomic
// loads, not N borrows.
func TestLastTSWarmPathAllocatesNothing(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1_000)
	f, err := createTrunks(filepath.Join(t.TempDir(), "f"), clk.cfgWith(unkeyedCfg()))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	a, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	clk.set(2_000)
	if _, _, err := f.Append(a, 0, []byte(`"x"`), nil); err != nil {
		t.Fatal(err)
	}
	if got := f.LastTS(a); got != 2_000 {
		t.Fatalf("warm LastTS = %d", got)
	}
	allocs := testing.AllocsPerRun(200, func() {
		if f.LastTS(a) != 2_000 {
			t.Fatal("value changed")
		}
	})
	if allocs > 0 {
		t.Errorf("warm LastTS allocates %.1f objects/op, want 0", allocs)
	}
}

func BenchmarkLastTS(b *testing.B) {
	clk := &fakeClock{}
	clk.set(1_000)
	dir := filepath.Join(b.TempDir(), "f")
	f, err := createTrunks(dir, clk.cfgWith(unkeyedCfg()))
	if err != nil {
		b.Fatal(err)
	}
	a, err := f.SpawnUnderRoot()
	if err != nil {
		b.Fatal(err)
	}
	for i := range 1000 {
		clk.set(int64(2_000 + i))
		if _, _, err := f.Append(a, 0, []byte(`"x"`), nil); err != nil {
			b.Fatal(err)
		}
	}
	x, err := f.Head(a)
	if err != nil {
		b.Fatal(err)
	}
	if err := x.SyncCoherent(); err != nil {
		b.Fatal(err)
	}
	x.Close()

	b.Run("Warm", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if f.LastTS(a) == 0 {
				b.Fatal("zero")
			}
		}
	})
	b.Run("ColdProbe", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if f.probeLastTS(f.head(a)) == 0 {
				b.Fatal("zero")
			}
		}
	})
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
}
