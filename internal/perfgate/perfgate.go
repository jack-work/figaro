package perfgate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

type Bench struct {
	Name      string
	Pkg       string
	Func      string
	Benchtime string
	Rel       float64
	FloorNS   float64
	AllocRel  float64
	AllocAbs  float64
}

type Sample struct {
	NS     float64
	Allocs float64
	HasAll bool
}

type Stat struct {
	P50NS     float64   `json:"p50_ns"`
	P95NS     float64   `json:"p95_ns"`
	AllocsOp  *float64  `json:"allocs_op,omitempty"`
	SamplesNS []float64 `json:"samples_ns"`
}

type Baseline struct {
	Machine string          `json:"machine"`
	Go      string          `json:"go"`
	Commit  string          `json:"commit"`
	Count   int             `json:"count"`
	Benches map[string]Stat `json:"benches"`
}

func ParseBenchOutput(r io.Reader, fn string) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || stripProcs(f[0]) != fn {
			continue
		}
		var s Sample
		seen := false
		for i := 2; i+1 < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", fn, f[i])
			}
			switch f[i+1] {
			case "ns/op":
				s.NS, seen = v, true
			case "allocs/op":
				s.Allocs, s.HasAll = v, true
			}
		}
		if !seen {
			return nil, fmt.Errorf("%s: no ns/op in %q", fn, sc.Text())
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

func stripProcs(name string) string {
	i := strings.LastIndexByte(name, '-')
	if i < 0 {
		return name
	}
	if _, err := strconv.Atoi(name[i+1:]); err != nil {
		return name
	}
	return name[:i]
}

func Quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

func Summarize(samples []Sample) Stat {
	ns := make([]float64, len(samples))
	var allocs []float64
	for i, s := range samples {
		ns[i] = s.NS
		if s.HasAll {
			allocs = append(allocs, s.Allocs)
		}
	}
	st := Stat{P50NS: Quantile(ns, .5), P95NS: Quantile(ns, .95), SamplesNS: ns}
	if len(allocs) > 0 {
		a := math.Round(Quantile(allocs, .5))
		st.AllocsOp = &a
	}
	return st
}

func MannWhitneyP(a, b []float64) float64 {
	n1, n2 := float64(len(a)), float64(len(b))
	if n1 == 0 || n2 == 0 {
		return 1
	}
	type obs struct {
		v float64
		g int
	}
	all := make([]obs, 0, len(a)+len(b))
	for _, v := range a {
		all = append(all, obs{v, 0})
	}
	for _, v := range b {
		all = append(all, obs{v, 1})
	}
	slices.SortFunc(all, func(x, y obs) int {
		switch {
		case x.v < y.v:
			return -1
		case x.v > y.v:
			return 1
		}
		return 0
	})
	var r1, ties float64
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && all[j].v == all[i].v {
			j++
		}
		rank := float64(i+j+1) / 2
		t := float64(j - i)
		ties += t*t*t - t
		for k := i; k < j; k++ {
			if all[k].g == 0 {
				r1 += rank
			}
		}
		i = j
	}
	u := r1 - n1*(n1+1)/2
	n := n1 + n2
	mu := n1 * n2 / 2
	sigma := math.Sqrt(n1 * n2 / 12 * ((n + 1) - ties/(n*(n-1))))
	if sigma == 0 {
		return 1
	}
	z := (math.Abs(u-mu) - 0.5) / sigma
	if z < 0 {
		z = 0
	}
	return math.Erfc(z / math.Sqrt2)
}

const Alpha = 0.05

type Verdict struct {
	Bench   Bench
	Base    Stat
	Cand    Stat
	P       float64
	Reasons []string
	Missing bool
}

func (v Verdict) Regressed() bool { return len(v.Reasons) > 0 }

func Compare(b Bench, base, cand Stat) Verdict {
	v := Verdict{Bench: b, Base: base, Cand: cand, P: MannWhitneyP(base.SamplesNS, cand.SamplesNS)}
	worse := func(baseNS, candNS, rel, floor float64) bool {
		return candNS > baseNS*(1+rel) && candNS-baseNS > floor
	}
	if worse(base.P50NS, cand.P50NS, b.Rel, b.FloorNS) && v.P < Alpha {
		v.Reasons = append(v.Reasons, fmt.Sprintf("p50 %s > %s (+%.0f%%, limit +%.0f%% and +%s, p=%.3f)",
			Dur(cand.P50NS), Dur(base.P50NS), 100*(cand.P50NS/base.P50NS-1), 100*b.Rel, Dur(b.FloorNS), v.P))
	}
	if worse(base.P95NS, cand.P95NS, 2*b.Rel, 2*b.FloorNS) && v.P < Alpha {
		v.Reasons = append(v.Reasons, fmt.Sprintf("p95 %s > %s (limit +%.0f%% and +%s)",
			Dur(cand.P95NS), Dur(base.P95NS), 200*b.Rel, Dur(2*b.FloorNS)))
	}
	if base.AllocsOp != nil && cand.AllocsOp != nil {
		limit := *base.AllocsOp*(1+b.AllocRel) + b.AllocAbs
		if *cand.AllocsOp > limit {
			v.Reasons = append(v.Reasons, fmt.Sprintf("allocs/op %.0f > %.0f (limit %.0f)", *cand.AllocsOp, *base.AllocsOp, limit))
		}
	}
	return v
}

func Dur(ns float64) string {
	switch {
	case math.Abs(ns) >= 1e9:
		return fmt.Sprintf("%.2fs", ns/1e9)
	case math.Abs(ns) >= 1e6:
		return fmt.Sprintf("%.1fms", ns/1e6)
	case math.Abs(ns) >= 1e3:
		return fmt.Sprintf("%.1fus", ns/1e3)
	}
	return fmt.Sprintf("%.0fns", ns)
}

func Report(w io.Writer, vs []Verdict) bool {
	ok := true
	fmt.Fprintf(w, "%-22s %9s %9s %7s %9s %s\n", "bench", "base p50", "p50", "delta", "p95", "allocs/op")
	for _, v := range vs {
		if v.Missing {
			ok = false
			fmt.Fprintf(w, "%-22s no baseline: run perfgate -update\n", v.Bench.Name)
			continue
		}
		allocs := "-"
		if v.Cand.AllocsOp != nil {
			allocs = fmt.Sprintf("%.0f", *v.Cand.AllocsOp)
			if v.Base.AllocsOp != nil && *v.Base.AllocsOp != *v.Cand.AllocsOp {
				allocs += fmt.Sprintf(" (%+.0f)", *v.Cand.AllocsOp-*v.Base.AllocsOp)
			}
		}
		mark := ""
		if v.Regressed() {
			ok = false
			mark = "  REGRESSION: " + strings.Join(v.Reasons, "; ")
		}
		fmt.Fprintf(w, "%-22s %9s %9s %+6.1f%% %9s %s%s\n", v.Bench.Name, Dur(v.Base.P50NS), Dur(v.Cand.P50NS),
			100*(v.Cand.P50NS/v.Base.P50NS-1), Dur(v.Cand.P95NS), allocs, mark)
	}
	return ok
}

func ReadBaseline(path string) (Baseline, error) {
	var bl Baseline
	raw, err := os.ReadFile(path)
	if err != nil {
		return bl, err
	}
	err = json.Unmarshal(raw, &bl)
	return bl, err
}

func WriteBaseline(path string, bl Baseline) error {
	for name, st := range bl.Benches {
		st.SamplesNS = roundAll(st.SamplesNS)
		st.P50NS, st.P95NS = math.Round(st.P50NS), math.Round(st.P95NS)
		bl.Benches[name] = st
	}
	raw, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func roundAll(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = math.Round(x)
	}
	return out
}

var Benches = []Bench{
	{Name: "form-cold-open", Pkg: "internal/store", Func: "BenchmarkGateFormColdOpen", Benchtime: "5x", Rel: .20, FloorNS: 10e6, AllocRel: .01, AllocAbs: 64},
	{Name: "global-list", Pkg: "internal/angelus", Func: "BenchmarkGateGlobalList", Benchtime: "20x", Rel: .25, FloorNS: 2e6, AllocRel: .02, AllocAbs: 64},
	{Name: "fork-wake-live", Pkg: "internal/angelus", Func: "BenchmarkForkWake_LiveHead", Benchtime: "50x", Rel: .25, FloorNS: .5e6, AllocRel: .02, AllocAbs: 32},
	{Name: "fork-wake-trimmed", Pkg: "internal/angelus", Func: "BenchmarkForkWake_TrimmedWindow", Benchtime: "20x", Rel: .30, FloorNS: 4e6, AllocRel: .02, AllocAbs: 64},
	{Name: "fork-wake-dormant", Pkg: "internal/angelus", Func: "BenchmarkForkWake_DormantReadBefore", Benchtime: "20x", Rel: .25, FloorNS: 2e6, AllocRel: .02, AllocAbs: 64},
	{Name: "pit-open-l", Pkg: "internal/cli", Func: "BenchmarkGatePitOpen/l", Benchtime: "5x", Rel: .30, FloorNS: 3e6},
	{Name: "pit-open-L", Pkg: "internal/cli", Func: "BenchmarkGatePitOpen/L", Benchtime: "5x", Rel: .30, FloorNS: 3e6},
	{Name: "pit-open-colon-ls", Pkg: "internal/cli", Func: "BenchmarkGatePitOpen/colon-ls", Benchtime: "5x", Rel: .30, FloorNS: 3e6},
}

func (b Bench) Top() string {
	top, _, _ := strings.Cut(b.Func, "/")
	return top
}
