package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/perffix"
	"github.com/jack-work/figaro/internal/perfgate"
)

func main() {
	update := flag.Bool("update", false, "measure and rewrite the baseline instead of checking against it")
	count := flag.Int("count", 10, "runs of each benchmark")
	only := flag.String("only", "", "regexp over bench names to run")
	baseline := flag.String("baseline", "testdata/perf/baseline.json", "baseline file, relative to the repo root")
	keep := flag.Bool("keep", false, "keep the work directory with raw benchmark output")
	flag.Parse()
	os.Exit(run(*update, *count, *only, *baseline, *keep))
}

func run(update bool, count int, only, baselinePath string, keep bool) int {
	start := time.Now()
	root, err := output("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return fail("perf: not in a git checkout: %v", err)
	}
	if !filepath.IsAbs(baselinePath) {
		baselinePath = filepath.Join(root, baselinePath)
	}
	machine := machineClass()
	var bl perfgate.Baseline
	if !update {
		if bl, err = perfgate.ReadBaseline(baselinePath); err != nil {
			return fail("perf: %v (go run ./cmd/perfgate -update writes one)", err)
		}
		if bl.Machine != machine {
			return fail("perf: baseline is for %q, this machine is %q; measure here with -update first", bl.Machine, machine)
		}
	}
	var pick *regexp.Regexp
	if only != "" {
		if pick, err = regexp.Compile(only); err != nil {
			return fail("perf: -only: %v", err)
		}
	}
	var benches []perfgate.Bench
	for _, b := range perfgate.Benches {
		if pick == nil || pick.MatchString(b.Name) {
			benches = append(benches, b)
		}
	}

	work, err := os.MkdirTemp("", "perfgate-")
	if err != nil {
		return fail("perf: %v", err)
	}
	if keep {
		fmt.Fprintf(os.Stderr, "perf: work dir %s\n", work)
	} else {
		defer os.RemoveAll(work)
	}
	fixtures := filepath.Join(work, "fixtures")
	figaro := filepath.Join(work, "figaro")
	if _, err := output(root, "go", "build", "-o", figaro, "./cmd/figaro"); err != nil {
		return fail("perf: go build ./cmd/figaro: %v", err)
	}
	bins := map[string]string{}
	for _, b := range benches {
		if bins[b.Pkg] != "" {
			continue
		}
		bin := filepath.Join(work, strings.ReplaceAll(b.Pkg, "/", "_")+".test")
		if _, err := output(root, "go", "test", "-c", "-o", bin, "./"+b.Pkg); err != nil {
			return fail("perf: go test -c ./%s: %v", b.Pkg, err)
		}
		bins[b.Pkg] = bin
	}

	raw := map[string][]byte{}
	for _, b := range benches {
		key := b.Pkg + " " + b.Top() + " " + b.Benchtime
		if raw[key] != nil {
			continue
		}
		cmd := exec.Command(bins[b.Pkg], "-test.run", "^$", "-test.bench", "^"+b.Top()+"$",
			"-test.benchtime", b.Benchtime, "-test.count", fmt.Sprint(count), "-test.benchmem", "-test.timeout", "20m")
		cmd.Dir = filepath.Join(root, b.Pkg)
		cmd.Env = append(os.Environ(), perffix.Env+"="+fixtures, "FIGARO_SMOKE_BIN="+figaro)
		out, err := cmd.CombinedOutput()
		_ = os.WriteFile(filepath.Join(work, b.Top()+".txt"), out, 0o600)
		if err != nil {
			return fail("perf: %s in %s: %v\n%s", b.Top(), b.Pkg, err, tail(out, 30))
		}
		raw[key] = out
	}

	measured := map[string]perfgate.Stat{}
	for _, b := range benches {
		samples, err := perfgate.ParseBenchOutput(bytes.NewReader(raw[b.Pkg+" "+b.Top()+" "+b.Benchtime]), b.Func)
		if err != nil {
			return fail("perf: %v", err)
		}
		if len(samples) != count {
			return fail("perf: %s ran %d times, want %d", b.Func, len(samples), count)
		}
		measured[b.Name] = perfgate.Summarize(samples)
	}

	took := time.Since(start).Round(time.Second)
	if update {
		if prev, err := perfgate.ReadBaseline(baselinePath); err == nil && prev.Machine == machine && pick != nil {
			bl = prev
		}
		if bl.Benches == nil {
			bl.Benches = map[string]perfgate.Stat{}
		}
		commit, _ := output(root, "git", "rev-parse", "--short=12", "HEAD")
		bl.Machine, bl.Go, bl.Commit, bl.Count = machine, runtime.Version(), commit, count
		for name, st := range measured {
			bl.Benches[name] = st
		}
		if err := os.MkdirAll(filepath.Dir(baselinePath), 0o755); err != nil {
			return fail("perf: %v", err)
		}
		if err := perfgate.WriteBaseline(baselinePath, bl); err != nil {
			return fail("perf: %v", err)
		}
		fmt.Printf("perf: baseline %s rewritten from %d benches x %d runs in %s\n", baselinePath, len(measured), count, took)
		return 0
	}

	var verdicts []perfgate.Verdict
	for _, b := range benches {
		base, ok := bl.Benches[b.Name]
		if !ok {
			verdicts = append(verdicts, perfgate.Verdict{Bench: b, Missing: true})
			continue
		}
		verdicts = append(verdicts, perfgate.Compare(b, base, measured[b.Name]))
	}
	if perfgate.Report(os.Stdout, verdicts) {
		fmt.Printf("perf: no regression against %s (%d runs each, %s)\n", bl.Commit, count, took)
		return 0
	}
	fmt.Printf("perf: REGRESSION against %s (%d runs each, %s); if intended, go run ./cmd/perfgate -update and commit the baseline\n", bl.Commit, count, took)
	return 1
}

func machineClass() string {
	model := "unknown cpu"
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
				model = strings.TrimSpace(v)
				break
			}
		}
	}
	return fmt.Sprintf("%s x%d gomaxprocs %d %s/%s", model, runtime.NumCPU(), runtime.GOMAXPROCS(0), runtime.GOOS, runtime.GOARCH)
}

func output(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func tail(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	return 2
}
