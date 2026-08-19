# Stage 10 contention baseline, 300bd36b, and one open question

Aria 3a9225b1, 2026-08-18. Panel C, `-cpu 16`, count=5, machine quiet, taken
under the bench lock. **Per-lock attribution only: totals are inadmissible**
(the canary showed a total FALL from 125.11s to 98.79s when a lock was ADDED).

## The distribution, and it is violently Pareto

| benchmark | mutex delay | iters | top attribution |
|---|---|---|---|
| CachedLogReadFromParallel | **134.23 s** | 1.83M | `MemLog.ReadFrom` 97.95%, `MemLog.Append` 1.15% |
| ForestRangeParallel | **78.45 s** | 898K | `forest.Range` 99.85% |
| FormApplyContended64 | 325.96 ms | ~180 | runtime-internal (`fmt.Sprintf`, 3.68%) |
| Storm50Observers | 4.30 ms | 216 | runtime-internal (`_LostContendedRuntimeLock`) |
| FormApplyContended8 | 3.00 ms | 178 | runtime-internal (`json.Marshal`) |
| LibrettoFoldBurstConcurrent | 2.28 ms | 765 | runtime-internal (`_LostContendedRuntimeLock`) |
| CachedLogReadWhileAppending | 1.49 ms | 1.0e9 | **VOID: dead load, see below** |

RE-TAKE REQUIRED: `CachedLogReadFromParallel` has been renamed
`CachedLogReadBelowWindowParallel` on the branch (5eb5fcb8) now that its
fall-through is admitted, and a tail-relative variant added. This baseline
must be re-taken against that commit before stage 10 quotes it.

**Two benchmarks hold 212 seconds. The other five hold 336 milliseconds
between them** -- a ratio of roughly 630:1.

My pre-registered prediction was "contention is Pareto (1-2 locks own most of
it)". On this evidence that is CONFIRMED, and by a wider margin than I
expected. But two caveats keep it from being a stage-10 result yet:

1. **Neither of the two is production-shaped.** `CachedLogReadFromParallel`
   drives a writer in a tight loop, and 97.9% of its reads fall BELOW the
   window into the locked inner log -- it is measuring the fall-through, not
   the published view (see the-516ns-read.md). `ForestRangeParallel` builds a
   synthetic loader. The production-shaped workload is deferred until stages
   2-4 settle who writes, per 091d162e.
2. **Four of the five "negligible" results are real; one is unresolved.**
   FormApplyContended8/64, Storm50Observers and LibrettoFoldBurstConcurrent
   each did ~1.2 seconds of genuine work (178-765 iterations at milliseconds
   apiece) and showed microseconds of delay. Those are honest low-contention
   readings, and their top entries are runtime-internal locks rather than
   ours.

## THE OPEN QUESTION, recorded rather than guessed at

`BenchmarkCachedLogReadWhileAppending` ran **1,000,000,000 iterations at
0.4235 ns/op** -- the Go benchmark iteration cap, at a per-op cost below a
single memory access.

There are two readings and they have opposite consequences:

- **It is real.** The body is `c.PeekTail()`, and if that is an inlined
  `atomic.Pointer.Load` it compiles to a plain MOV on x86-64. ~1.3 cycles is
  plausible, and the number would then be a genuine measurement of a lock-free
  tail read -- the best evidence in the tree for the published-view design.
- **It is hollow.** The result is discarded except for the `ok` check, so if
  the load can be hoisted out of the loop the benchmark measures an empty
  loop, and its 1.49 ms of "no contention" means nothing at all. That is the
  silent zero in its purest form: a benchmark that reports a lock-free path by
  not exercising any path.

## RESOLVED, 2026-08-18: IT IS A SILENT ZERO. And my first resolution was wrong.

**FIRST ANSWER, WRONG:** I reported it real on two arms -- PeekTail answers in
60ns with writeMu held (LOCK-FREE, and this arm stands), and "64 distinct
tails across 64 appends" (NOT HOISTED). **The second arm was invalid.** It put
`e.LT` into a map, CONSUMING the entry, while the benchmark writes
`if _, ok := c.PeekTail(); !ok`, which DISCARDS it. I tested an expression
nobody runs.

**CORRECT ANSWER**, found by 091d162e and reproduced here independently at
3,000,000x, count=2:

    discarded (what the benchmark does)    0.5344 / 0.5127 ns
    consumed into a package sink          10.19  / 10.01  ns
    FigaroLT only                          5.791 /  6.150 ns

**19x.** The compiler proves the discarded load dead and deletes it. The
benchmark has never measured the published view: it measured an empty loop
while reporting the best-looking number in the tree for the mechanism this
campaign cares about. The honest cost it was hiding is ~10 ns, because the
Entry carries a `message.Message` with slices.

PeekTail IS lock-free -- that part was never in doubt and arm 1 proves it. What
was false is that the 0.42 ns number measured it.

THE LESSON, and it is mine: **to prove a benchmark has not been optimised
away, you must exercise the benchmark's own expression.** A rewrite that
consumes the value cannot detect elimination of one that discards it. Arm 2
now times both shapes and fails when the gap can only be elimination; it is
RED on this commit, on purpose, because the defect is real and its fix belongs
to stage 0.

The original reasoning is kept below, because the useful part is that the
question was asked at all.

**I did not resolve it immediately, deliberately.** The store benchmarks of the clean
A/A floor run were live when I found it, and compiling a probe would have
contaminated a floor already forty minutes in the making. It is resolved the
same way everything else in this campaign was: the zero-lock probe on
`PeekTail`, plus a check that the loop cannot be hoisted (assign to a package
sink, compare the ns/op). Two minutes of work, after the floor.

It was in the Panel C list precisely so it could not be quietly dropped, and
that is what brought it back for the two minutes it needed.
