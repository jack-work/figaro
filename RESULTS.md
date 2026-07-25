# RESULTS — chalkboard persistent-tree migration, measured

Branch `chalk/integrate`. Everything below is measured output from this
machine at the commits named. Nothing is estimated, nothing is paraphrased,
and the regressions are in the same tables as the wins.

```
$ go version
go version go1.26.4-X:nodwarf5 linux/amd64
AMD Ryzen 7 5800X 8-Core Processor (16 threads)
```

BEFORE = `bench-before.txt`, measured by the BENCH worker on `main` @ `fc29357`
(see `BASELINE.md`). AFTER = `bench-after.txt`, same script, same flags, same
fixtures, measured at `000b023`. Comparison produced with
`go run golang.org/x/perf/cmd/benchstat@latest` (benchstat is not on PATH here
and nothing was installed globally).

```sh
./scripts/chalkbench-go.sh bench-after.txt
go run golang.org/x/perf/cmd/benchstat@latest bench-before.txt bench-after.txt
./scripts/chalkbench.sh -k 50 -i 1000 | tee chalkbench-e2e-after.txt
```

---

## 1. The short version

| axis | verdict |
|---|---|
| `Clone` (per RPC, per turn, per metadata read) | **gone** — identity function, 2.0 ms → 0.65 ns on a 5,000-key board |
| `Diff` of an unchanged board | **gone** — 2.15 ms → 6.5 ns on 50,000 keys |
| `Diff` of a real derived board (1 key changed) | **3.2 ms → 282 ns** on 50,000 keys |
| `Apply` a small patch to a big board | **2.78 ms → 2.6 µs** on 50,000 keys |
| cold aria chalkboard replay (`loadChalkboardLocked`) | **56–73% faster** |
| the RPC data race | **fixed**, race-detector clean, 3/3 runs + full suite + e2e |
| spurious `<system-reminder>`s on `figaro loadout` | **fixed**, 25 per re-apply on real data → 0 |
| `Get` of one key | **slower**: 11 ns → 19/50/79 ns (37/5k/50k keys) — O(log n) vs a hash |
| JSON marshal/unmarshal of a whole board | **~2x slower**, structural; see §4 |
| WAL reducer fold (`chalkboardReduce`) | **20–35% slower**; see §4 |
| `state -j` of a synthetic 2 MB board, end to end | **73.6 ms → 103.6 ms**; see §5 |

Four axes won by orders of magnitude. Three regressed, all three on the
JSON-serialisation axis, all three explained below and none of them on a path
that runs per turn at real board sizes.

---

## 2. Microbenchmarks — `internal/chalkboard`

`benchstat bench-before.txt bench-after.txt`, sec/op, n=10:

```
                                    │  bench-before.txt   │            bench-after.txt             │
                                    │       sec/op        │    sec/op      vs base                 │
Lookup-16                                    203.1n ±  1%    226.3n ±  1%   +11.45% (p=0.000 n=10)
Snapshot_Diff_Small/10keys_1diff-16          554.2n ±  4%    245.9n ±  2%   -55.63% (p=0.000 n=10)
Snapshot_Diff_Small/50keys_1diff-16         1768.5n ±  2%    789.8n ±  1%   -55.34% (p=0.000 n=10)
Snapshot_Diff_Small/50keys_5diff-16         1807.5n ±  2%    831.1n ±  2%   -54.02% (p=0.000 n=10)
Render_DefaultTemplates_5entries-16          8.232µ ±  6%    8.378µ ±  1%         ~ (p=0.529 n=10)
Clone/default-16                         3739.5000n ±  2%   0.6449n ±  1%   -99.98% (p=0.000 n=10)
Apply_SmallPatch/default-16                  1.511µ ±  1%    1.904µ ±  1%   +26.01% (p=0.000 n=10)
Diff/default/identical-16                 1154.500n ± 19%    6.450n ±  1%   -99.44% (p=0.000 n=10)
Diff/default/one-key-16                     1397.0n ± 11%    807.5n ±  2%   -42.19% (p=0.000 n=10)
Diff/default/all-different-16                4.311µ ±  1%    3.412µ ±  1%   -20.85% (p=0.000 n=10)
Get/default-16                               10.29n ±  2%    19.05n ±  1%   +85.09% (p=0.000 n=10)
Render/default-16                            7.230µ ±  3%    7.458µ ±  2%    +3.15% (p=0.011 n=10)
MarshalJSON/default-16                       76.37µ ±  4%   151.74µ ±  4%   +98.70% (p=0.000 n=10)
UnmarshalJSON/default-16                     99.05µ ±  2%   186.50µ ±  1%   +88.29% (p=0.000 n=10)
JSONRoundTrip/default-16                     170.9µ ±  3%    336.0µ ±  1%   +96.60% (p=0.000 n=10)
Clone/large-16                        2018336.0000n ±  8%   0.6482n ±  0%  -100.00% (p=0.000 n=10)
Apply_SmallPatch/large-16                  205.046µ ±  3%    2.178µ ±  2%   -98.94% (p=0.000 n=10)
Diff/large/identical-16                 172123.000n ±  0%    6.503n ±  1%  -100.00% (p=0.000 n=10)
Diff/large/one-key-16                        226.0µ ±  1%    466.8µ ± 16%  +106.58% (p=0.000 n=10)
Diff/large/all-different-16                  590.6µ ±  3%    950.3µ ±  6%   +60.91% (p=0.000 n=10)
Get/large-16                                 11.30n ±  6%    50.15n ±  2%  +343.56% (p=0.000 n=10)
Render/large-16                              433.7µ ±  5%    418.6µ ±  2%         ~ (p=0.105 n=10)
MarshalJSON/large-16                         50.25m ±  2%   100.43m ±  6%   +99.88% (p=0.000 n=10)
UnmarshalJSON/large-16                       60.39m ±  3%   122.52m ±  5%  +102.87% (p=0.000 n=10)
JSONRoundTrip/large-16                       112.8m ±  1%    234.9m ±  5%  +108.18% (p=0.000 n=10)
Clone/huge-16                         4757985.5000n ± 13%   0.6474n ±  0%  -100.00% (p=0.000 n=10)
Apply_SmallPatch/huge-16                  2777.357µ ±  2%    2.638µ ±  2%   -99.91% (p=0.000 n=10)
Diff/huge/identical-16                 2147344.000n ±  1%    6.492n ±  1%  -100.00% (p=0.000 n=10)
Diff/huge/one-key-16                         3.231m ±  1%    1.045m ± 13%   -67.66% (p=0.000 n=10)
Diff/huge/all-different-16                   7.421m ±  6%   23.703m ±  8%  +219.39% (p=0.000 n=10)
Get/huge-16                                  11.51n ±  1%    78.90n ±  4%  +585.45% (p=0.000 n=10)
MarshalJSON/huge-16                          42.24m ±  1%    77.39m ±  5%   +83.19% (p=0.000 n=10)
UnmarshalJSON/huge-16                        48.29m ±  4%    98.10m ±  2%  +103.15% (p=0.000 n=10)
JSONRoundTrip/huge-16                        92.37m ±  2%   173.76m ±  3%   +88.11% (p=0.000 n=10)
DiffDerived/default/1-key-16                                 184.4n ±  1%
DiffDerived/default/5-key-16                                 292.7n ±  1%
DiffDerived/large/1-key-16                                   230.4n ±  1%
DiffDerived/large/5-key-16                                   676.4n ±  1%
DiffDerived/huge/1-key-16                                    282.1n ±  2%
DiffDerived/huge/5-key-16                                    907.6n ±  2%
geomean                                      83.98µ          6.945µ         -87.43%
```

Fixtures: `default` = 37 keys / 15,948 B, a verbatim capture of the real
default (`opus5`) loadout board; `large` = 5,004 keys / ~10 MB; `huge` =
50,000 keys / ~4 MB.

### `Diff/*/one-key` and `Diff/huge/all-different` deserve a paragraph

`BenchmarkDiff` builds `next` by `buildBoard(f.mutated(n))` — a **fresh map**.
Before the swap that was the only thing there was to measure. After the swap it
measures two *structurally unrelated* trees holding near-identical content:
zero sharing, so zero pointer-identity pruning, which is exactly the O(n log n)
worst case the TREE worker documented. That is why `Diff/large/one-key` reads
+107% and `Diff/huge/all-different` +219%.

No `Diff` in figaro does that. `turn.go`'s context combine, `ApplyLoadout` and
`Render`'s `prev` all compare a board with its own `Apply`-descendant. That case
is `BenchmarkDiffDerived`, added in this branch because the suite was measuring
the wrong thing after the swap:

| board | before (whole-map diff, 1 key changed) | after (derived, 1 key) | ratio |
|---|---|---|---|
| default, 37 keys | 1.397 µs | **184 ns** | 7.6x faster |
| large, 5,004 keys | 226.0 µs | **230 ns** | 982x faster |
| huge, 50,000 keys | 3.231 ms | **282 ns** | 11,458x faster |

Both benchmarks are kept: one is the worst case, one is the real case.

---

## 3. Store — replay and the reducer, `internal/store`

sec/record (the metric that matters; the fold is O(records)):

```
                                      │ bench-before.txt │           bench-after.txt           │
                                      │    sec/record    │  sec/record   vs base               │
ChalkboardReduceFold/M=30/N=100-16          36.32µ ± ∞ ¹   39.31µ ± ∞ ¹        ~ (p=0.310 n=5)
ChalkboardReduceFold/M=30/N=2000-16         35.96µ ± ∞ ¹   44.90µ ± ∞ ¹  +24.84% (p=0.008 n=5)
ChalkboardReduceFold/M=500/N=100-16         585.9µ ± ∞ ¹   700.2µ ± ∞ ¹  +19.52% (p=0.008 n=5)
ChalkboardReduceFold/M=500/N=2000-16        599.1µ ± ∞ ¹   720.1µ ± ∞ ¹  +20.21% (p=0.008 n=5)
ChalkboardReduceFold/M=5000/N=100-16        6.075m ± ∞ ¹   8.205m ± ∞ ¹  +35.07% (p=0.008 n=5)
ChalkboardReduceFold/M=5000/N=2000-16       6.652m ± ∞ ¹   7.958m ± ∞ ¹        ~ (p=0.690 n=5)
ChalkboardOpenReplay/M=30/N=100-16          74.12µ ± ∞ ¹   20.19µ ± ∞ ¹  -72.77% (p=0.008 n=5)
ChalkboardOpenReplay/M=30/N=2000-16        18.304µ ± ∞ ¹   6.288µ ± ∞ ¹  -65.65% (p=0.008 n=5)
ChalkboardOpenReplay/M=500/N=100-16        134.56µ ± ∞ ¹   41.66µ ± ∞ ¹  -69.04% (p=0.008 n=5)
ChalkboardOpenReplay/M=500/N=2000-16       19.508µ ± ∞ ¹   8.013µ ± ∞ ¹  -58.92% (p=0.008 n=5)
ChalkboardOpenReplay/M=5000/N=100-16        854.6µ ± ∞ ¹   291.2µ ± ∞ ¹  -65.93% (p=0.008 n=5)
ChalkboardOpenReplay/M=5000/N=2000-16       49.18µ ± ∞ ¹   21.43µ ± ∞ ¹  -56.42% (p=0.008 n=5)
geomean                                     193.6µ         125.6µ        -35.14%
```

Whole-benchmark wall clock, same run:

```
ChalkboardOpenReplay/M=5000/N=2000-16       98.35m → 42.86m   -56.42%
ChalkboardState10000-16                     324.8m → 129.4m   -60.16%
ChalkboardPatches10000-16                   302.7m → 130.0m   -57.05%
```

**`ChalkboardOpenReplay` is the ADDENDUM ruling-3 path** — the one the accessor
migration knowingly regressed (`state = state.Apply(p)` per WAL record, a full
map copy each time) and the one the tree had to earn back. It is now **56–73%
faster than main**, not merely recovered. `ChalkboardState10000` /
`ChalkboardPatches10000` (the pre-existing store benchmarks) are ~60% faster.

**`ChalkboardReduceFold` is 20–35% slower and I am not going to dress that up.**
It was **270% slower** at first; §4 explains what I fixed and what is left.

---

## 4. The honest regression: JSON in, JSON out

Three separate effects, measured individually.

### 4a. Eager canonicalisation was a loss — fixed

`NewValue` originally parsed and re-encoded every value to compute its
canonical form, so *decoding a board canonicalised the whole board* even though
`Equal` is only ever asked about the few keys a patch touches. First AFTER run
(kept at `/tmp/chalk-fleet/bench-after-naive.txt`):

| | main | eager canon | lazy canon (shipped) |
|---|---|---|---|
| `ReduceFold` M=5000/N=100, per record | 6.075 ms | 22.59 ms | **8.205 ms** |
| `UnmarshalJSON/default` | 99.05 µs | 376.0 µs | **186.5 µs** |

Fixed in `000b023`: the canonical form is memoised lazily in a shared box
(`sync.Once`, so concurrent readers of a published snapshot are safe), and
`Equal` short-circuits on identical raw bytes first. Same semantics, verified by
TREE's property/fuzz suite unchanged.

### 4b. `encoding/json` charges twice for custom JSON hooks — structural

A `Snapshot` is a struct now, so it needs `MarshalJSON`/`UnmarshalJSON`, and
`encoding/json` **re-scans a marshaler's output** before emitting it and
**pre-scans a value** before handing it to an unmarshaler. Measured on the real
15 KB board:

```
BenchmarkProbeMapMarshal-16               74683 ns/op   19437 B/op   76 allocs/op   # main's map
BenchmarkProbeSnapMarshalJSONDirect-16    76207 ns/op   22249 B/op   80 allocs/op   # s.MarshalJSON()
BenchmarkProbeSnapViaEncodingJSON-16     151710 ns/op   38794 B/op   82 allocs/op   # json.Marshal(s)

BenchmarkProbeUnmarshalMap-16             97273 ns/op   22466 B/op  125 allocs/op   # main's map
BenchmarkProbeUnmarshalSnapDirect-16     104068 ns/op   28992 B/op  200 allocs/op   # s.UnmarshalJSON(b)
BenchmarkProbeUnmarshalSnapViaJSON-16    187920 ns/op   29176 B/op  204 allocs/op   # json.Unmarshal(b,&s)
```

So: **the tree costs 2–7%; the double scan costs 90–100%.** `MarshalJSON` is at
parity with the map it replaced. This is a tax on having a custom codec at all,
identical for any implementation of it, and it is why `MarshalJSON/*` and
`UnmarshalJSON/*` read ~+90% in §2 (those benchmarks call `json.Marshal`).

Mitigation: the two paths where the tax is hot — `store.chalkboardReduce` and
`chalkboard.State.Open`/`Save` — call `MarshalJSON`/`UnmarshalJSON` directly
and say why in a comment. `TestSnapshotDirectCodecMatchesEncodingJSON` pins that
the direct and indirect spellings emit the same bytes, so the shortcut can be
reverted for taste at a known cost and never for correctness.

### 4c. What remains on the reducer, and why I stopped

After 4a + 4b + bulk tree construction, `ReduceFold` is 20–35% slower than
main. `chalkboardReduce` is a pure `[]byte -> []byte` fold: it materialises a
board, applies one patch, serialises it, and throws the board away. **A
persistent tree cannot win there** — it can only pay for `n` nodes where a map
paid for `n` buckets. The remaining gap is exactly that plus 4b's residue.

The fix is to stop materialising per record — WAL checkpoints, or a stateful
reducer that keeps the tree between records and exploits structural sharing.
DESIGN.md defers both, deliberately, and I did not smuggle them in. For scale:
this reducer runs on **segment rollover and fork**, not on aria open (BENCH
surprise #1); the aria-open path is `ChalkboardOpenReplay`, which is 56–73%
*faster*.

### 4d. `Get` and `Lookup`

`Get` is 11 ns → 19 ns (37 keys), 50 ns (5,004), 79 ns (50,000): O(log n)
pointer chases against a hash lookup. Exactly the trade BENCH predicted
(surprise #6) and worth it — a 50,000-key `Get` at 79 ns is noise beside the
4.76 ms `Clone` per read that it replaces. `Lookup` (+11%) is `Get` plus a
`json.Unmarshal`, so the same story diluted.

`Apply_SmallPatch/default` is +26% (1.51 µs → 1.90 µs): on a 37-key board, path
copying three keys plus wrapping two values costs more than copying a 37-entry
map. It inverts by `large` (-99%) and stays inverted.

---

## 5. End to end, real CLI, isolated daemon, zero tokens

`./scripts/chalkbench.sh -k 50 -i 1000`, before at `d17cdac`, after at
`bfadafd`. Both runs on a quiet machine (load average < 5; a run taken at load
average 22 inflated every row 4x and was discarded — the fleet's other arias
were working).

| operation | before ms/op | after ms/op |
|---|---|---|
| `new --loadout opus5` (incl. daemon cold start) | 73.00 | 73.00 |
| `list -j` (process + RPC floor) | 12.16 | 12.12 |
| `set` (16 KB board) | 12.06 | 11.88 |
| `state -j` (16 KB board) | 12.56 | 13.08 |
| `loadout` re-apply (16 KB board) | 13.00 | 14.00 |
| `unset` (16 KB board) | 12.22 | 11.94 |
| inflate: 1000 × `set` of 2 KB | 12.59 | 12.16 |
| `set` (2 MB board) | 12.72 | 12.64 |
| **`state -j` (2 MB board)** | **73.58** | **103.56** |
| `unset` (2 MB board) | 12.74 | 12.96 |
| `state -j` after daemon restart (cold) | 145.00 | 180.00 |

At the **real** board size every row is inside the ~12.1 ms CLI floor (process
spawn + connect + RPC), exactly as BENCH predicted (surprise #5). Nothing got
faster there because nothing could: the floor dominates.

The two 2 MB rows are §4b, twice: the server marshals 2 MB through
`json.Marshal(ChalkboardResponse)` (re-scan) and the CLI decodes it into a
`Snapshot` (pre-scan). Reproducible across three runs (99.7 / 100.6 / 103.6 ms).
It could be halved by typing `rpc.ChalkboardResponse.Snapshot` as
`json.RawMessage`, which would keep the wire bytes identical but push decoding
onto every consumer; I judged a 30 ms regression on a synthetic 1,037-key ×
2 KB board not worth that churn, and left the typed field ACCESSORS introduced.
Real boards are 16 KB, where the same effect is 0.5 ms inside a 12 ms floor.

Cold replay (n=1, 145 → 180 ms) is the same 2 MB effect on the restart path;
the *fold* underneath it is 56–73% faster (§3).

---

## 6. The race — fixed, with the only oracle that counts

ADDENDUM ruling 8: `-race` is the oracle, "the daemon didn't crash" is not.

STRESS's acceptance criteria, verbatim, all four:

**(1) Repro test clean under `-race`** — on main: 11 `WARNING: DATA RACE`, 5/5
runs. Here, 3 consecutive runs:

```
$ CHALK_RACE_REPRO=1 go test -race -count=1 \
    -run 'TestChalkboardRPCRaceRepro|TestChalkboardStateRaceRepro' ./internal/figaro/
run 1 races: 0 result: ok  	github.com/jack-work/figaro/internal/figaro	3.288s
run 2 races: 0 result: ok  	github.com/jack-work/figaro/internal/figaro	3.289s
run 3 races: 0 result: ok  	github.com/jack-work/figaro/internal/figaro	3.290s
```

**(2) `go test -race ./...`** — clean, whole repo, re-run at the final commit.

**(3) `go build ./... && go vet ./... && go test ./...`** — clean.

**(4) The stress harness, both modes, exit 0:**

```
$ STRESS_WRITERS=4 STRESS_READERS=6 STRESS_DURATION=30 ./scripts/chalkstress.sh
  ✓ angelus 2780765 still alive
  ✓ every worker command exited 0
  ✓ writer 0..3: all 24 keys present with own values
  ✓ no leaked scratch keys (set/unset pairs all landed)
  ✓ persisted state matches the fresh read (96 stress keys)
[chalkstress] writer rounds: 132  |  state reads: 6672  |  final keys: 127
[chalkstress] PASS

$ STRESS_RACE=1 STRESS_WRITERS=4 STRESS_READERS=6 STRESS_DURATION=30 ./scripts/chalkstress.sh
  ✓ angelus 2938279 still alive
  ✓ every worker command exited 0
  ✓ writer 0..3: all 24 keys present with own values
  ✓ no leaked scratch keys (set/unset pairs all landed)
  ✓ persisted state matches the fresh read (96 stress keys)
  ✓ race-instrumented daemon reported no data races
[chalkstress] writer rounds: 4  |  state reads: 144  |  final keys: 127
[chalkstress] PASS   (REAL_EXIT=0)
```

Race mode needed a one-line harness fix (`ef3e6f8`): `nraces=$(grep -c … ||
echo 0)` yields the two-line string `"0\n0"` when grep finds nothing, which
made the following `[ -eq ]` a syntax error and reported a spurious failure.
Unreachable on main, where the count was never zero.

All four unsynchronized readers named in ADDENDUM ruling 7 are covered, because
the fix is at the `State` level: `MethodChalkboard`, `Agent.ApplyLoadout`,
`Agent.chalkboardString`, `Agent.chalkboardInt`. `State.dirty` is published in
the same atomic value, so it cannot race `Save` either.

---

## 7. Backward compatibility — the acceptance gate

The user has 126 live conversations (218 store nodes). Method: copy the **live
aria store twice**, point a binary built from `main@fc29357` at one copy and one
built from this branch at the other, and diff `figaro state -j` for **every**
node. The live store was only ever read; both daemons ran in isolated
`FIGARO_RUNTIME_DIR`/`FIGARO_STATE_DIR` temp roots.

```
== copying live store (read-only source) ==
45M	/tmp/chalk-integ/backcompat/old/state/arias
== enumerating arias (old binary) ==
arias: old=218 new=218
  OK: same aria id set

compared 218 arias; mismatches=0; empty-boards=36
total bytes compared: 2732003
BACKCOMPAT: PASS
```

181 non-empty boards, 6,807 keys, 2.73 MB of chalkboard JSON, **byte-identical
between the old and new binaries**. The 36 empty ones are loadout/null nodes
whose ids the CLI rejects (`invalid char '@'`) — identical output from both
binaries, pre-existing behaviour.

This matters more than it looks: the chalkboard channel's on-disk reducer state
records are content-hashed (`{"_hash":"cabbe1cf…","_idx":0,<flat board>}`), so a
single changed byte in `MarshalJSON` would not be cosmetic. It would be a hash
mismatch on someone's aria.

Script: `/tmp/chalk-fleet/backcompat.sh`.

### Live daemon exercise (also isolated)

`/tmp/chalk-fleet/exercise.sh` — 9 steps against a real daemon on the real
`opus5` loadout, no prompts, no turns, no tokens:

```
  ✓ real loadout landed (37 keys)
  ✓ scalar round-trips        ✓ object value round-trips    ✓ non-ASCII round-trips
  tricky raw bytes over the wire: "<a>&</a>"
  ✓ unset removes the key
  ✓ reordered rewrite kept the original bytes ({"b":2,"a":1})
  ✓ re-applying the loadout changed nothing
  ✓ board reloaded byte-identically after daemon restart   (241 keys)
  ✓ state -j is a valid flat object
  ✓ list reflects the second aria's mantra
  ✓ two cold replays agree
EXERCISE: PASS
```

---

## 8. The one sanctioned behaviour change, caught in the wild

DESIGN.md predicted that a value changing only in JSON key order fires a
spurious `<system-reminder>`. It does, on this user's real loadout, on **every**
`figaro loadout` re-apply. Measured with the binary built from `main@fc29357`:

```
$ figaro loadout --id <aria> opus5
loadout "opus5" applied (25 keys):
  skills.claude-session-ask
  skills.brave
  … 23 more skills.* keys …

-- raw byte diff (before vs after re-apply):
   DIFFERENT bytes (9097 differing byte positions)
-- canonical diff (keys sorted recursively):
   CANONICALLY IDENTICAL -> the 25 reported keys were semantically equal
```

25 skill envelopes rewritten, persisted as a patch record, and announced to the
agent — for nothing. After this branch:

```
loadout "opus5": no changes (chalkboard already matches)
```

That required one real fix beyond the swap (`bfadafd`): `Agent.ApplyLoadout` and
`turn.go`'s `additivePatch` decided "did this key change?" with `bytes.Equal`
against the stored value. Since a semantically-equal `Set` into the tree keeps
the *original* bytes, a byte comparison never converges — it would have reported
the same 25 keys forever, one reminder each, on every re-apply. Both now ask the
chalkboard itself (`current.Apply(candidate).Diff(current)`), which is both
correct and O(k log n).

No other rendered body changes for any input. `Render` and the templates were
not touched; `BenchmarkRender/*` is flat (+3.15% default, ~ large).

Script: `/tmp/chalk-fleet/prove_spurious.sh`.

---

## 9. What is still worth doing

Deliberately out of scope this pass; carried forward, not forgotten.

1. **WAL checkpoints / a stateful chalkboard reducer.** The one place the tree
   loses (§4c). A reducer that keeps its tree between records would turn the
   20–35% loss into a large win, and checkpoints would stop the fold from being
   O(records × board) at all.
2. **LT-keyed root retention + `At(lt)` history.** The `version` field on
   `Snapshot` is the handle it wants. Structural sharing already makes keeping
   old roots nearly free; nothing consumes them yet.
3. **`rpc.ChalkboardResponse.Snapshot` as `json.RawMessage`**, to halve the
   double-scan tax on multi-MB boards (§5). Wire-compatible; costs every
   consumer an explicit decode.
4. **Lazy `Value` boxing.** One small allocation per value for the canon box;
   a slab allocation in the bulk-build path would collapse `n` of them into one.
5. The cruft ADDENDUM ruling 5 froze: `Merge`'s `removeString` mutating its
   argument's backing array, four duplicated `readCredo`/`systemBlocks`
   implementations across providers, `EnvironmentSnapshot`'s misleading name,
   and 7 files that are `gofmt -l` dirty on `main`.
