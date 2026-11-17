# The perf gate

`go run ./cmd/perfgate` runs a fixed set of benchmarks on fixtures it builds
itself, never the live store, and fails when one is slower or allocates more
than `testdata/perf/baseline.json` says it did. The release gate runs it alone
on the machine, in heavy.sh's bench class.

## The benches

| name | where | what one op is |
|---|---|---|
| form-cold-open | `internal/store` `BenchmarkGateFormColdOpen` | a fresh backend opening the #122 board (3,250 leaves, 13,200 patches, one 8 MiB segment) to its state |
| global-list | `internal/angelus` `BenchmarkGateGlobalList` | `figaro.list --global` over 2,000 conversations and 120 role forms (#130), right after an idle sweep |
| fork-wake-live | `internal/angelus` `BenchmarkForkWake_LiveHead` | head fork of a 250-turn figaro and the child's wake |
| fork-wake-trimmed | `internal/angelus` `BenchmarkForkWake_TrimmedWindow` | the same past the 2,048-patch window |
| fork-wake-dormant | `internal/angelus` `BenchmarkForkWake_DormantReadBefore` | waking a dormant fork and its first transcript page |
| pit-open-l, -L, -colon-ls | `internal/cli` `BenchmarkGatePitOpen` | a real `figaro listen` on a pty over a 200-figaro store: from the key that opens the pit (`l`, `L`, the Enter of `:ls`) to the first frame showing it, read from the pager's `key.paint` mark. ns/op is the median open of a run |

Fixtures are built once per gate run under `FIGARO_PERF_FIXTURES` (a temp dir
on /tmp) and shared by every run of a bench. Without that variable each bench
builds its own, so `go test -bench GateFormColdOpen ./internal/store` works
on its own.

## What fails

Each bench runs 10 times (`-count`). For each, against the baseline's samples:

- time: p50 is worse than the baseline's by more than the bench's relative
  limit AND by more than its absolute floor, AND a Mann-Whitney U test says
  the two sample sets differ (p < 0.05). p95 is held to twice both limits.
- allocs/op (median): more than baseline x (1 + AllocRel) + AllocAbs. The pit
  benches cross a process boundary and carry no allocs.

The limits live in `internal/perfgate` (`Benches`), so changing one is a
reviewed code change. The baseline records the machine class (CPU, cores,
GOMAXPROCS); on another machine the gate refuses to compare rather than pass.

## Output

At most ten lines: one per bench (base p50, p50, delta, p95, allocs/op, and
the reason when it regressed), then a verdict. Exit 0 clean, 1 on a
regression, 2 when it could not measure. `-keep` keeps the raw `go test`
output; `-only <regexp>` runs some benches.

## Moving the baseline on purpose

When a change is meant to cost time or allocations, or after a speedup you
want held:

```
HEAVY_CLASS=bench ~/.local/state/impresario/heavy.sh go run ./cmd/perfgate -update
git add testdata/perf/baseline.json
```

Commit the baseline with the change that moved it, so the diff shows the new
numbers in review. `-update -only <regexp>` rewrites only those benches.
