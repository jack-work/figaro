# BASELINE — chalkboard performance BEFORE the persistent-tree migration

Branch `chalk/bench`, forked from `main` at **fc29357**; measurements taken
at **d17cdac** (the benchmark commits themselves; no non-test code on this
branch differs from main).

Everything below is measured output from this machine. Nothing is estimated
or paraphrased. Re-run the same commands on the refactored tree to get the
AFTER; `benchstat bench-before.txt bench-after.txt` for the honest diff.

## Machine / toolchain

```
$ go version
go version go1.26.4-X:nodwarf5 linux/amd64

$ lscpu | head -6
Architecture:                            x86_64
CPU(s):                                  16
Vendor ID:                               AuthenticAMD
Model name:                              AMD Ryzen 7 5800X 8-Core Processor
```

`benchstat` is **not on PATH** on this machine (`command -v benchstat` →
nothing) and nothing was installed globally to change that. Get it ad hoc
with `nix run nixpkgs#go-tools -- benchstat …` or
`go run golang.org/x/perf/cmd/benchstat@latest …`. The committed
`bench-before.txt` is in the standard `go test -bench` format, so any
benchstat build will read it.

## What was measured

| deliverable | file | what |
|---|---|---|
| Go microbenchmarks | `internal/chalkboard/chalkboard_bench_test.go` | Clone / Apply / Diff / Get / Lookup / Render / JSON, over three fixtures |
| fixtures | `internal/chalkboard/bench_fixtures_test.go` + `testdata/board-default.json` | `default` (real loadout board), `large` (5k keys ~2KB), `huge` (50k keys) |
| the seam | `internal/chalkboard/bench_seam_test.go` | the ONE place a Snapshot is built from a map |
| replay | `internal/store/chalkboard_replay_bench_test.go` | `chalkboardReduce` fold + cold `ChalkboardState` open |
| end-to-end | `scripts/chalkbench.sh` | real CLI against an isolated daemon, zero LLM round-trips |

The `default` fixture is a verbatim capture of this user's real
default-loadout board (37 keys / 15,776 bytes: 26 skill envelopes,
`system.credo`, the `system.*` scalars). No key looked like a credential, so
nothing was dropped. Capture procedure:
`internal/chalkboard/testdata/board-default.provenance.md`.

## How to reproduce (and how to produce the AFTER)

```sh
# Go benchmarks -> bench-before.txt (this file's numbers)
./scripts/chalkbench-go.sh bench-before.txt        # 4m03s wall here
# after the swap, on the integrated branch:
./scripts/chalkbench-go.sh bench-after.txt
benchstat bench-before.txt bench-after.txt

# end-to-end CLI timing (isolated daemon, no tokens) -> table on stdout
./scripts/chalkbench.sh -k 50 -i 1000 | tee chalkbench-e2e-after.txt
```

`chalkbench-go.sh` runs each fixture in its own process on purpose: the
fixtures are cached for the life of a process, and a single combined
`go test -bench=. -count=10` leaves ~60MB of `large`+`huge` boards resident,
whose GC tax inflates the cheap `default` numbers about 2x (measured:
`Clone/default` 3.7µs split vs 31.7µs combined). Per-fixture processes it is.
Defaults: `COUNT=10 BENCHTIME=300ms` for `internal/chalkboard`,
`-benchtime=1x -count=5` for the store replay grid (one M=5000/N=2000 fold
already takes 13 seconds).

Timing split of the 4m03s: `internal/chalkboard` ≈ 2m30s, `internal/store`
≈ 1m33s.

## Headline numbers (medians of the committed samples)

Full sample sets: `bench-before.txt`. Medians computed over the 10 (chalkboard)
or 5 (store) samples.

### internal/chalkboard — `go test -bench` , `-benchmem`

| benchmark | default (37 keys, 15KB) | large (5,004 keys, ~10MB) | huge (50,000 keys, ~4MB) |
|---|---|---|---|
| `Clone` | **3.740 µs** — 18,216 B, 40 allocs | **2.018 ms** — 12,122,883 B, 5,021 allocs | **4.758 ms** — 8,750,118 B, 50,129 allocs |
| `Apply_SmallPatch` (2 sets + 1 remove) | 1.511 µs — 2,728 B | 205.046 µs — 393,536 B | 2.777 ms — 3,148,288 B |
| `Diff` identical | 1.155 µs — 0 B | 172.123 µs — 0 B | 2.147 ms — 0 B |
| `Diff` one-key-different | 1.397 µs — 400 B | 225.983 µs — 400 B | 3.231 ms — 400 B |
| `Diff` all-different | 4.311 µs — 5,304 B | 590.601 µs — 781,288 B | 7.421 ms — 6,290,792 B |
| `Get` | 10.3 ns — 0 allocs | 11.3 ns | 11.5 ns |
| `Render` (5-entry patch) | 7.229 µs | 433.733 µs | — |
| `MarshalJSON` | 76.367 µs | 50.247 ms | 42.244 ms |
| `UnmarshalJSON` | 99.051 µs | 60.393 ms | 48.288 ms |
| `JSONRoundTrip` | 170.906 µs | 112.843 ms | 92.368 ms |

Fixture-independent:

```
BenchmarkLookup-16                             203.1 ns      176 B/op    3 allocs/op
BenchmarkSnapshot_Diff_Small/10keys_1diff-16   554.2 ns      400 B/op    2 allocs/op
BenchmarkSnapshot_Diff_Small/50keys_1diff-16   1.768 µs      400 B/op    2 allocs/op
BenchmarkSnapshot_Diff_Small/50keys_5diff-16   1.808 µs      400 B/op    2 allocs/op
BenchmarkRender_DefaultTemplates_5entries-16   8.232 µs     4003 B/op   87 allocs/op
```

**Read these three lines first:**

- `Clone` on the *real* board costs **3.7µs and 18KB of garbage, per call** —
  and it is called per `figaro.chalkboard` RPC, per turn, and inside
  `Agent.chalkboardString`. On the synthetic large aria it is **2.0ms and
  12MB**. Target after the swap: O(1), zero allocations.
- `Diff` of two *identical* boards costs **1.155µs / 172µs / 2.1ms** — the
  full O(n) walk even though nothing changed. Pointer-identity pruning should
  make the identical case ~free and the one-key case O(log n).
- `Diff` one-key-different is barely cheaper than all-different on the
  `default` board (1.4µs vs 4.3µs) because both walk every key.

### internal/store — replay

`chalkboardReduce` unmarshals the whole board, applies one patch, re-marshals
the whole board — **per record**. figwal folds it over sealed records on
segment open (`xwal.reducibleFold`, also used by `fork.go`). The per-record
cost therefore scales with board size, and the fold is O(N·M):

```
BenchmarkChalkboardReduceFold/M=30/N=100-16          3.632 ms      30.8 µs/record
BenchmarkChalkboardReduceFold/M=30/N=2000-16        71.927 ms      35.8 µs/record
BenchmarkChalkboardReduceFold/M=500/N=100-16        58.588 ms     582.8 µs/record
BenchmarkChalkboardReduceFold/M=500/N=2000-16     1198.117 ms     613.9 µs/record
BenchmarkChalkboardReduceFold/M=5000/N=100-16      607.451 ms    6094.9 µs/record
BenchmarkChalkboardReduceFold/M=5000/N=2000-16   13304.597 ms    7346.6 µs/record
```

Per-record cost goes **30.8µs → 582.8µs → 6.1ms** as the board goes
30 → 500 → 5,000 keys: dead linear in M, exactly as predicted. A single fold
of 2,000 records over a 5,000-key board takes **13.3 seconds** and allocates
**6.5 GB**.

The cold-open path is *not* the same code — `loadChalkboardLocked` folds the
records with plain map writes and never round-trips JSON — and it shows:

```
BenchmarkChalkboardOpenReplay/M=30/N=100-16          7.412 ms
BenchmarkChalkboardOpenReplay/M=30/N=2000-16        36.608 ms
BenchmarkChalkboardOpenReplay/M=500/N=100-16        13.456 ms
BenchmarkChalkboardOpenReplay/M=500/N=2000-16       39.016 ms
BenchmarkChalkboardOpenReplay/M=5000/N=100-16       85.462 ms
BenchmarkChalkboardOpenReplay/M=5000/N=2000-16      98.351 ms
```

Pre-existing store benchmarks, same run, for context:

```
BenchmarkChalkboardState10000-16    324.828 ms    43,686,088 B/op   810,232 allocs/op
BenchmarkChalkboardPatches10000-16  302.666 ms    43,975,680 B/op   810,094 allocs/op
```

### End-to-end CLI (`scripts/chalkbench.sh -k 50 -i 1000`)

Isolated daemon (`FIGARO_RUNTIME_DIR`/`FIGARO_STATE_DIR` in a temp dir, real
config + skills inherited), real `figaro` binary built from this worktree,
**zero LLM round-trips** — `set`/`unset`/`loadout`/`state` never start a turn.
Verbatim output, also committed as `chalkbench-e2e-before.txt`:

```
chalkbench — isolated daemon, no LLM round-trips
  repo:     /home/gluck/dev/figaro-qua/chalk-bench (d17cdac)
  loadout:  opus5
  board:    37 keys / 15936 bytes  ->  1037 keys / 2082826 bytes after inflate
  store:    /tmp/chalkbench.hF0LAZZ5/state (removed on exit)

operation                                                 n   total ms      ms/op
---------------------------------------------------- ------ ---------- ----------
new --loadout opus5 (daemon cold start incl.)             1         73      73.00
list -j (RPC + process overhead baseline)                50        608      12.16
set (small board)                                        50        603      12.06
state -j (small board)                                   50        628      12.56
loadout opus5 re-apply (small board)                      1         13      13.00
unset (small board)                                       50        611      12.22
inflate: 1000 sets of 2048B                            1000      12589      12.59
set (large board)                                        50        636      12.72
state -j (large board)                                   50       3679      73.58
loadout opus5 re-apply (large board)                      1         14      14.00
unset (large board)                                       50        637      12.74
state -j after daemon restart (cold replay)               1        145     145.00
```

Reading it honestly:

- **~12.2 ms is floor**, not chalkboard work: `list -j` does no board work and
  costs the same as `set`. That is process spawn + connect + RPC. Any
  chalkboard win hides under it for writes.
- `set` is flat across board size (12.06 → 12.72 ms at 130x the bytes), so
  the write path is not where the pain is at CLI granularity.
- **`state -j` is the signal: 12.56 ms → 73.58 ms** when the board grows from
  16KB to 2MB. That read is `Clone` + `MarshalJSON` + a 2MB socket write; the
  swap removes the Clone but not the marshal or the transfer, so expect a
  partial win here, not a collapse to 12ms. Do not over-claim it.
- Cold replay of the whole aria after a daemon restart: **145 ms**.

## Notes for the integrator

1. **The seam is `internal/chalkboard/bench_seam_test.go`.** Three one-line
   bodies (`buildBoard`, `boardGet`, `boardLen`) plus `unmarshalBoard`. After
   the swap, edit those bodies to `chalkboard.FromMap(m)`, `s.Get(key)`,
   `s.Len()`. Nothing else in the benchmark suite touches Snapshot internals.
   `internal/store/chalkboard_replay_bench_test.go` has one more:
   `replaySnapLen`.
2. The old `chalkboard_bench_test.go` benchmarks (`Snapshot_Diff_*`,
   `Render_DefaultTemplates_5entries`) were **kept**, rewritten through the
   seam, so history stays comparable.
3. Re-run with the **same scripts and the same flags**, or benchstat will be
   comparing different experiments.
4. If `board-default.json` is ever re-captured, `bench-before.txt` is void.
