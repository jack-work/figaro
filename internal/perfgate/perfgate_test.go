package perfgate

import (
	"math"
	"strings"
	"testing"
)

func TestParseReadsOnlyTheNamedBenchAcrossProcSuffixes(t *testing.T) {
	out := `goos: linux
BenchmarkGatePitOpen/l-16  	       5	   7682000 ns/op
BenchmarkGatePitOpen/L-12  	       5	   7939000 ns/op
BenchmarkGatePitOpen/l-12  	       5	   8363000 ns/op	 120 B/op	 7 allocs/op
PASS`
	got, err := ParseBenchOutput(strings.NewReader(out), "BenchmarkGatePitOpen/l")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].NS != 7682000 || got[0].HasAll || got[1].Allocs != 7 || !got[1].HasAll {
		t.Fatalf("parsed %+v", got)
	}
}

func stat(ns []float64, allocs float64) Stat {
	s := make([]Sample, len(ns))
	for i, v := range ns {
		s[i] = Sample{NS: v, Allocs: allocs, HasAll: allocs >= 0}
	}
	return Summarize(s)
}

func spread(center, width float64) []float64 {
	out := make([]float64, 10)
	for i := range out {
		out[i] = center + width*(float64(i)-4.5)/4.5
	}
	return out
}

func TestASlowdownPastBothLimitsFails(t *testing.T) {
	b := Bench{Name: "x", Rel: .2, FloorNS: 1e6}
	v := Compare(b, stat(spread(10e6, 1e6), 100), stat(spread(15e6, 1e6), 100))
	if !v.Regressed() {
		t.Fatalf("+50%% and +5ms passed: %+v", v)
	}
}

// The absolute floor keeps a tiny bench's relative jitter from failing the gate.
func TestASlowdownUnderTheFloorPasses(t *testing.T) {
	b := Bench{Name: "x", Rel: .2, FloorNS: 1e6}
	v := Compare(b, stat(spread(1e6, .1e6), 100), stat(spread(1.6e6, .1e6), 100))
	if v.Regressed() {
		t.Fatalf("+60%% but only +0.6ms failed: %v", v.Reasons)
	}
}

func TestOverlappingSamplesPass(t *testing.T) {
	b := Bench{Name: "x", Rel: .1, FloorNS: 0}
	base := []float64{10, 30, 10, 30, 10, 30, 10, 30, 10, 30}
	cand := []float64{12, 31, 12, 31, 12, 31, 12, 31, 12, 31}
	v := Compare(b, stat(base, -1), stat(cand, -1))
	if v.Regressed() {
		t.Fatalf("noise read as a regression: %v (p=%.3f)", v.Reasons, v.P)
	}
}

func TestMoreAllocationsFailEvenWhenFaster(t *testing.T) {
	b := Bench{Name: "x", Rel: .2, FloorNS: 1e6, AllocRel: .01, AllocAbs: 10}
	if v := Compare(b, stat(spread(10e6, 1e6), 1000), stat(spread(9e6, 1e6), 1015)); v.Regressed() {
		t.Fatalf("15 allocs inside 1%%+10 failed: %v", v.Reasons)
	}
	if v := Compare(b, stat(spread(10e6, 1e6), 1000), stat(spread(9e6, 1e6), 1030)); !v.Regressed() {
		t.Fatal("30 more allocs/op passed")
	}
}

func TestMannWhitneySeparatesDisjointSamples(t *testing.T) {
	if p := MannWhitneyP(spread(10, 1), spread(20, 1)); p > .001 {
		t.Fatalf("disjoint samples p=%.4f", p)
	}
	if p := MannWhitneyP(spread(10, 1), spread(10, 1)); p < .9 {
		t.Fatalf("identical samples p=%.4f", p)
	}
}

func TestQuantileInterpolates(t *testing.T) {
	if q := Quantile([]float64{4, 1, 3, 2}, .5); q != 2.5 {
		t.Fatalf("median %v", q)
	}
	if q := Quantile(nil, .5); !math.IsNaN(q) {
		t.Fatalf("empty median %v", q)
	}
}

func TestEveryBenchHasLimits(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range Benches {
		if seen[b.Name] || b.Rel <= 0 || b.FloorNS <= 0 || b.Benchtime == "" || !strings.HasPrefix(b.Func, "Benchmark") {
			t.Errorf("bench %+v", b)
		}
		seen[b.Name] = true
	}
}
