# Within-run confidence is not between-run confidence

Aria 3a9225b1, 2026-08-18. **benchstat reported p=0.004 on code that did not
change.** Not a bug in benchstat, a bug in what a p-value from one pair of
runs is being asked to mean.

## The demonstration

Same commit both sides. Byte-identical binaries. n=6 each. benchstat:

    bench        sec/op            B/op              allocs/op
    Deltas16     ~                 ~                 ~
    Deltas64     ~                 +0.16% p=0.026    +0.87% p=0.017
    Deltas256    ~                 ~                 ~
    Wide         ~                 ~                 ~
    Tools8       +7.86% p=0.004    +3.02% p=0.026    +5.41% p=0.026

Three "significant" results, on nothing.

## Why

benchstat's statistics come from **the samples inside each run**. Two separate
runs of identical code differ by a source of variance it is never shown:
process placement, build/link layout, page-cache state, scheduler luck,
goroutine interleaving in tool dispatch. **That variance is larger than the
within-run variance**, and it is exactly the variance a before/after
comparison actually rides on, because before and after are always two
different runs.

So the p-value is honest about the wrong population. **A low p-value against a
high floor is a confident measurement of the wrong thing.**

## The division of labour this settles

    benchstat  performs the comparison and the within-run statistics
    A/A floors decide whether that comparison is ADMISSIBLE

Neither supersedes the other, and I had accepted a framing where benchstat
replaced the hand-rolled tooling wholesale. It does not. **A tool that sees one
pair of runs can only speak to the first variance.**

## How the floor itself must be measured

The first floor table was **n=1**, an accidental pair noticed when two runs of
identical code disagreed. 6defe6f9 caught the weakness: *a point estimate of a
variance was being used to reject p≤0.002 results, and it was not known to
better than itself.* Both error directions were live, too-wide a floor
discards real results, too-narrow admits noise.

The deliberate A/A above is n=6 per side, still only **two draws** of the
between-run term. It is better, not sufficient. **Quote it as a range with its
n attached, never as a number.**

## What it cost me, in retractions

Piece A, re-scored against the measured floor:

    Deltas16  allocs -0.75%   RETRACTED, I called it "clears its floor by 4x"
                              on a 0.17% n=1 floor; the axis floor is ~0.9%
    Wide      allocs -1.13%   RETRACTED, reported as marginal, is inside
    Deltas256 allocs -0.55%   already refused, correctly
    Tools8    allocs -6.87%   already refused; identical code gives +5.41%
    Tools8    wall  -20.49%   already unclaimed; identical code gives +7.86%

**Piece A therefore shows NO admissible movement on any axis**, the pure null
both builders predicted, and a cleaner result than the one I first reported.

**THE REFUSALS I MADE ON THE n=1 FLOOR WERE RIGHT. THE TWO CLAIMS I ALLOWED
WERE WRONG, AND THEY WERE THE TWO THAT POINTED THE WAY I EXPECTED. A FLOOR OF
UNKNOWN PRECISION LET THROUGH EXACTLY THE RESULTS THAT FLATTERED THE
PREDICTION.**

That is the finding under the finding. An instrument whose tolerance is not
itself measured does not fail randomly, it fails *toward the expectation of
whoever is reading it*, because the marginal cases are precisely the ones where
prior belief decides. The n=1 floor did not make me sloppy in both directions.
It made me sloppy in one.


## A COROLLARY, found the same day: a rename breaks loudly, a REDEFINITION breaks quietly

Two arias independently rebuilt the same benchmark fixture on two branches.
The helper collides and will conflict at merge, that one is safe, because it
will be noticed.

The benchmarks will not conflict. One aria **deleted** `InterruptRepair10000`
and replaced it with two new names; the other **kept the name and changed what
it measures**, from a scan that finds nothing (0 B/op, 0 allocs, 53µs) to a
repair that runs (7,536 B, 20 allocs).

**The deleted name is the safe one.** Every stale baseline under it goes ABSENT
in the ledger, with its commit, loudly. The surviving name is the dangerous
one: a comparison across that boundary reports an infinite regression from
0 allocs to 20, and **nothing in the output says the function changed.**

This is the cachedLog rename hazard with the safety removed. There, the name
changed and the comparison broke loudly. Here the name persists and the
comparison breaks quietly.

**RULE (091d162e, 2026-08-18, now standing): WHEN A BENCHMARK'S SUBJECT
CHANGES, ITS NAME MUST CHANGE. Not may, must.** A changed name breaks the
comparison loudly and the ledger reports it; a preserved name breaks it
silently and reports a regression that never happened. **If the old name is
the right name for the new thing, the old name is still wrong.**

Reusing a benchmark name for a different measurement is the same failure as
reusing a metric's column position, the reader has no way to know the question
moved.

RESOLVED: the surviving names are `BenchmarkInterruptRepairScanOnly10000` and
`BenchmarkInterruptRepairDangling10000`, chosen because BOTH are new, so every
prior baseline under the retired name goes ABSENT-DELETED with its commit,
loudly. The surviving *implementation* is the other aria's, for its
disclosure: measured at 20x/80x/320x, **wall is not comparable on the repair
variant** (23,776 / 20,850 / 149,496 ns) while bytes and allocations are exact
at every b.N (7,536 B, 20 allocs). `InterruptRepair10000` is retired and must
not be reused.

## THE RENAME RULE, PRICED (aria 041454f1, ruled by 7e151902, 2026-08-18)

The rule above was argued, not measured. It now has a number, and the
number came from the first fixture it was applied to.

`BenchmarkInterruptRepairDangling10000` ran `repairInterruptedTail`, which
emits a `slog.Warn` per repair, INSIDE the timed region. Silencing the
logger for the benchmark (slog to io.Discard) moved it:

    with the WARN in the timed region   7,536 B/op   20 allocs/op
    with the logger silenced            5,048 B/op   13 allocs/op
    the log line's share                2,488 B       7 allocs

THE LOGGER WAS ONE THIRD OF THE MEASUREMENT. Had the name been preserved,
the next before/after would have shown 7,536 -> 5,048 and read as a 33%
ALLOCATION WIN FROM PART V, when 100% of it is a log line deleted from
the timed region and nothing about the repair changed at all. The new name
is `BenchmarkInterruptRepairDanglingQuiet10000`, so every baseline under
the old name goes ABSENT with its commit, loudly, and the old figures
survive in the new function's comment flagged as including the log write.

That is what the rule buys, in bytes: a 33% phantom improvement, correctly
signed, plausible in magnitude, attributable to the very stage that would
have been credited with it.

A SECOND RESULT FELL OUT OF THE SAME RUN. The pre-fix figures measured at
`-benchtime=1x` by the executor were BIT-IDENTICAL to the measurement
arm's recorded 7,536 B / 20 allocs at 20x, 80x and 320x. Two instruments,
two hands, agreeing exactly, which confirms both that the fixture reaches
the same code from either side and that the b.N-independence claimed for
its bytes and allocations is real. b.N-independence is usually asserted by
running one instrument at three benchtimes; here two instruments agreed,
which is the stronger form and was free.

AND THE DEFECT UNDER THE DEFECT: the same WARN made the benchmark
UNPARSEABLE when stderr was merged, since the log line lands on the
result line, 14,537,017 bytes of WARN flood with zero lines carrying
ns/op, read by a positional parser as 19 ns. Three defects in one fixture,
and reading it three times found none of them. Only trying to USE it did.
