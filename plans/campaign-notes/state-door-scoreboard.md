# The scoreboard: predictions made before the numbers, scored after

Aria 3a9225b1. Pre-registered 2026-08-17 BEFORE the harness existed, accepted
by 091d162e without amendment to the numbers. Every row gets a verdict when
its stage lands. A prediction that is quietly dropped is a prediction that was
wrong, so none are dropped.

## The floor, which everything else is read against

| claim | predicted | measured | verdict |
|---|---|---|---|
| A/A wall noise | +/- 3% | median **+/-0.9%**, p90 **+/-8.9%**, worst **+/-285%** | **WRONG, AND THE QUESTION WAS WRONG** |
| A/A allocation drift | EXACTLY ZERO on every benchmark | zero on **53 of 74**; 21 carry a measured floor; **0 moved across the clean pair** | **PARTLY WRONG, and the 21 were instructive** |

CLEAN PAIR: aa2 vs aa3, both machine-quiet, both under the bench lock,
committed source, `TMPDIR=/var/tmp`.

**I predicted a single floor of +/-3%. There is no single floor, and that is
the real finding.** On the clean pair the MEDIAN benchmark moved 0.9% -- better
than I predicted -- while `FormOpenReplay/M=30/N=100` moved **285%** (11.6ms
to 44.7ms, with b.N collapsing from 100 to 21). Quoting +/-285% would declare
the whole panel unreadable to protect one outlier; quoting the median would
licence claims the outlier cannot support. **A single number lies in both
directions**, so each benchmark now carries its own measured ns floor in
`bench/state-door/floors.json`, and three benchmarks that moved more than 20%
on identical source are BARRED from ns claims entirely:

    BenchmarkFormOpenReplay/M=30/N=100   +285.1%
    BenchmarkOpenTurnFrame10              +36.8%
    BenchmarkOpenTurnFrame40              +27.0%

Allocations came out better than the ns story: **zero benchmarks moved
allocation numbers across the clean pair**, and 53 of 74 have a floor of
exactly zero.

WHAT IT COSTS, revised now that the clean pair exists and in the OTHER
direction from my last statement: with a median floor of 0.9%, the S1/S2/S3 ns
bands ARE mostly scoreable after all -- but **only on benchmarks whose own
floor clears them**, and never on the three barred above. `FormOpenReplay` and
`OpenTurnFrame10/40` all sit in stage 1's and stage 7's evidence, so those
stages are scored on allocations. I withdrew these bands prematurely on the
contaminated pair; the clean measurement gives some of them back, and saying
so is the same obligation as withdrawing them was.

The allocation prediction was wrong in a more useful way. I claimed every
benchmark would show exactly zero allocation drift on identical source; 19 did
not. The cause is not nondeterminism -- `b.N` differs between runs, so a
fixture amortises over a different number of iterations and B/op wobbles while
allocs/op sits perfectly still (CachedLogAppend: 1640-1723 B at a rock-solid 5
allocs, purely because b.N moved from 2.06M to 2.21M). My first instinct was
to refuse to compare those 19, which would have punished the signal for the
fixture's behaviour. Each benchmark now carries its own measured floor, and
**55 of 74 have a floor of exactly zero** -- which is where the campaign's
headline numbers live.


## The stages

| # | stage | prediction | measured | verdict |
|---|---|---|---|---|
| 1 | one fold | flat within noise; FormReduceFold 0 to -8%; alloc delta 0 | **allocations IDENTICAL, ns within 2.2%** on a purpose-built patch-carrying fixture proven REACHABLE (+11.5 MB sabotage) | **CONFIRMED, and it validates the fixture** |
| 2 | one door per fig IR append | a SMALL REGRESSION: CachedLogAppend +0..5%, allocs +0 or +1 | | |
| 3 | door stamps TurnID, closes the open call | InterruptRepair10000 -5..15%; record +8..16 B; resident_ir_bytes +2..4%; **identity oracles must still read 296->0, 8->0, 76->0** | | |
| 4 | delete the derivation (5 full-log reads) | AgentRestoreHistory10000 <=-30%; InterruptRepair10000 <=-50%; OpenLargeAria -10..25%; first net-negative Go LOC of the campaign. Under -10% means those reads were already warm -- a FINDING, not a failure | | |
| 5 | one in-flight turn | streaming frame 22 allocs -> 18..22, NOT BELOW 18; wall flat | **SCORED on the round loop**: -8.6/-11.0/-4.1% ns on the delta axis, **-20.8% ns and -20.9% B on the tools axis**; ~1.0 allocation removed PER EVENT | **executor's "not per-byte" CONFIRMED; "allocations fall by a small fixed amount" REFUTED — they fall linearly with events**. On the COMPOSE path it remains unscoreable and non-regressing |
| 6 | one record shape | resident_ir_bytes -5..15%; decode -5..10%. FALSIFIER: a uniform record could be BIGGER. Identity oracles again | | |
| 7 | coordinates become a _meta index | FormState10000 and Vectors10000Branches improve | | |
| 8-9 | one value model, one patch vocabulary | form Diff -10..30%; Apply_SmallPatch flat to better | | |
| 10 | mutex reduction | <=40% of the actor-scope sites show ANY contention; contention is Pareto (1-2 locks); the whole workstream moves NO existing benchmark by more than 5% | | |

ATTRIBUTION CORRECTION 2, 2026-08-18: stage 5's linear per-event allocation
was published as `mergeTurnTools`' map. **Escape analysis says that map does
not escape** (`turn_repair.go:45:31`, verified independently) — it is
stack-allocated and was never a heap allocation. The linear term is
`partialAssistant`'s escaping `out.Content` append. Totals and the completeness
of the three-mechanism account are unchanged; only the label was wrong. Standing
rule from it: **when a prediction is about allocations, ask the compiler before
predicting.**

ATTRIBUTION CORRECTION, 2026-08-18: the ruling that ordered the patch-carrying
fixture built was **091d162e's**, not 6defe6f9's. 6defe6f9 explicitly declined
it ("I do not want the fixture built for perf -- my prediction stays unscored,
correctly") and approved only the driveOneRound benchmark. I credited the
wrong aria in my stage-1 report; 6defe6f9 corrected it against their own
interest. A misattributed credit is still a false record.

**STAGE 10'S BASELINE IS 31 decls / 239 sites, NOT 32/242.** Fixed in the
record before the workstream starts rather than argued about after it. Between
300bd36b and 5eb5fcb8 the actor-scope census fell by one declaration and three
sites, attributed exactly to `internal/provider/copilot/copilot.go` (2/11 ->
1/8) under main's 266a0639 and 58068c8b, "hush owns the copilot session token;
delete figaro's copy" -- **in the provider the brief calls dead**. Those three
sites were paid for by somebody else's commit. A workstream reporting
"242 -> N" would be claiming deletions it did not make.
| -- | the RSS gap | not the metered caches, not one leak; heap profile names <=5 call sites for >=60% of inuse_space | **2 call sites = 63.71%**; 90 MiB heap at ZERO arias with 0.0 MiB metered; 52% cumulative under `xwal.open` | **RIGHT, and tighter than predicted** |
| 10 | mutex reduction (partial, baseline only) | contention is Pareto, 1-2 locks | 2 benchmarks hold 212s, the other five hold 336ms: **~630:1** | **RIGHT and CONSERVATIVE** |

## Void by rename / redefinition

| entry | status |
|---|---|
| `BenchmarkInterruptRepair10000` (piece A tripwire) | **VOID.** The name is retired; it measured the SCAN that finds nothing. Result narrows to: *A did not change the scan.* Re-take under `ScanOnly10000` / `Dangling10000`. |
| `BenchmarkCachedLogReadFromParallel` (pre-5eb5fcb8) | void by design; fixed-coordinate variant renamed `ReadBelowWindowParallel`. |

## Revisions to my own pre-registration, stated rather than dropped

**Per-stage RSS from bench processes is withdrawn as evidence**, 2026-08-17,
before any stage was measured. Panel B's calibration shows peak RSS does not
track a retention below the arena the process has already touched: 16 MiB and
64 MiB both read +10 MiB, and only 128 MiB and above register. No state-door
stage will move a bench process by 128 MiB, so an RSS number from one is not
evidence either way. RSS evidence for the stages comes from the isolated
snapshot daemon; Panel B stays for peak threads, open files and page faults,
which have no such absorption.

**Contention TOTALS are withdrawn in favour of per-lock attribution.** The
Panel C canary showed a total FALL from 125.11s to 98.79s when a lock was
ADDED, because serializing readers relieved the writer. A total is not a
direction.


## Hypotheses handed to me to kill

| hypothesis | source | verdict |
|---|---|---|
| unmetered bytes scale with `loaded_heads`, not `resident_arias` | 091d162e | **HALF KILLED.** Per-open and flat (0.104 MB, marginal) CONFIRMED. Attribution to `loaded_heads` REFUTED: 48 opens with reads moved it 0 -> 1, because it counts trunk heads, not channels. |

## Standing amendments to how these are scored

- **ns bands below +/-30% are not scoreable.** The measured A/A floor makes
  most of the pre-registered ns predictions unmeasurable; they are scored on
  allocations or recorded as unmeasurable. See the floor section.
- **A performance shape is not a mechanism.** "Gets faster with more readers"
  was read as proof that a path is lock-free. It is a throughput curve, and
  the path it was measured on takes a lock 97.9% of the time. Mechanism claims
  are settled by the zero-lock probe, which holds the lock and asks whether
  the read answers. See the-516ns-read.md.
- A lock removal that moves a benchmark by MORE than 5% is a finding
  requiring explanation, not a victory (091d162e). The same suspicion the 20x
  got.
- A number that beats its own floor by a wide margin is a suspect first.
- ns/op does not travel between machines. This host reads the campaign's
  ~12,000 ns streaming frame at ~10,400 ns, at byte-identical 9,545 B and 22
  allocs. B/op and allocs/op are the numbers that carry.

## STAGE 2's INSTRUMENT PASS — 9ed3f561, 2026-08-18

| prediction | registered | verdict |
|---|---|---|
| fold-through-JSON and fold-in-memory agree SEMANTICALLY | in the test, before running | CONFIRMED |
| ...and DIVERGE in raw bytes for `< > &` and for whitespace | in the test, before running | CONFIRMED: `"a \u003cb\u003e \u0026 c"` vs `"a <b> & c"`; `{"k":[1,2]}` vs `{ "k" : [ 1 , 2 ] }` |
| the divergence reaches the WIRE for object/array keys only | from reading genericBody, before measuring | CONFIRMED at the wire, and INDEPENDENTLY by 041454f1 as its own test |
| the divergence is header-vs-tail | 3a9225b1's framing, inherited | **WRONG, RETRACTED.** It is WARM vs COLD. Both halves of a stage 2 snapshot come off disk, where every value is already a fixed point of the JSON route. |
| an exact fold count catches an ahead-by-one header | d921742d's sharpened ruling | **WRONG, and I built the model that shows it.** The count is computed from the header's own declared base, so it is satisfied; the surplus record re-applies idempotently. The HEADER IDENTITY at its own base is what sees it: 4 of 4 segments ahead, 3 of 4 behind. |
| DanglingQuiet bytes/allocs are b.N-independent | 3a9225b1, cross-validated by 041454f1 | CONFIRMED at 1x (n=6, exact) — with the caveat that one 20x sample read 5,053 rather than 5,048, so it is exact-to-0.1%, not exact |

THE THREE INSTRUMENTS THE BOUNDARY NEEDS, none of which subsumes another:
header identity (sees ahead-by-one), exact fold count (sees a memo that
re-folds, and a bound merely satisfied), value oracle (sees skips and wrong
values).

INSTRUMENT FAILURES THIS PASS, ALL THREE MINE, ALL CAUGHT BEFORE ANYTHING WAS
CLAIMED ON THEM: an awk that matched nothing and diffed two empty files
("IDENTICAL OPCODE HISTOGRAM"); a wire comparison over an EMPTY patch list that
reported "identical wire bytes"; and `fmt.Sprint` on two snapshots, comparing
tree POINTERS, which reported a difference in the direction that flattered the
ruling under test. The third is the one worth remembering: a blind check does
not fail randomly, it fails toward whatever the reader already believes.

### RETRACTION, 9ed3f561, 2026-08-18 20:40

**"publish-what-was-written is free on the round-loop path" — WITHDRAWN, VACUOUS.**
The round loop never reaches `store.Form.reduceOne`. Proven two-sided: a panic
planted there makes `TestWarmAndColdWireBytes` panic (the control fires) and
leaves the round loop running clean at 183,612 B/op in the same tree. Under
`-tags figcount`, 20 iterations report **Apply=21, Unmarshal=0, Marshal=0** —
the 21 are the fixture's own agent setup, under StopTimer.

Instance four of the catalogue, in the measurement arm's hands: no fixture for
the changed code, reported as no change, plausible enough to be written into a
report. Caught by d921742d asking for the reachability proof a NULL needs
exactly as much as a before/after does.

**The consequence outlives the retraction: the round loop is blind to all form
and snapshot work, so it will not see stage 2's memo either.** The only
instrument that prices `driveOneRound` cannot price the thing stage 2 changes.
A store-backed fixture is owed, and it must be sabotage-proven before any count
is quoted from it.

### WHAT STAGE 2 BUYS, RESTATED AFTER A CORRECTION — 9ed3f561 / d921742d, 2026-08-18

Measured on the owner's real store, read-only, sizes and counts only:
**1,218 form channels, 1,218 segment files, ZERO with more than one segment.**
Fattest channel 117,013 B = 5.6% of one 2 MiB segment. Records per channel:
median 3, p90 8, p99 42, max 371. Mean record 2,326 B → ~900 records per
segment at today's density.

**THE FOLD BOUND: INSURANCE.** Nothing has ever rolled, so folding from a header
at base 1 *is* folding from zero. Zero benefit today; real benefit the day a
channel rolls, where the memoised fold stops at ~900 records and the unmemoised
one grows without limit.

**THE UNMARSHAL SAVING: NOT A SAVING AGAINST TODAY'S CODE — my error, corrected
by d921742d.** I priced one whole-board decode per record (`formReduce`) as the
status quo. It is not: `ProjectIncrementally` folds DECODED PATCHES through
`PatchesBetween`, and the cold path (`form.go:290`) unmarshals ONE PATCH per
record, not the board. The whole-board round trip belongs to `formReduce`, which
runs on rotation and inside figwal's `StateAt` — **the route the plan already
rejected**. A saving against a rejected alternative is not a saving. The 97µs /
76µs figures are figaro's own documented measurement, quoted, never mine, and
they price a path nobody walks.

**WHAT IT ACTUALLY BUYS: THE DELETION.** `IncrementalProjection` and its five
carried version fields, four hand-written `acceptAssistantProjection` copies,
and the provider's need for an LT, a turn and a bookmark. Gluck's rule that
NOTHING MAY PIN EVICTED BYTES is a retention property, and the projection is
what violates it.

**The size of what is retained**, since that is what the deletion returns —
encoded native messages per aria, on disk: median 0 B, p90 249,455 B, **p99
2,463,337 B, max 9,841,806 B**, 200.2 MiB across 1,463 channels. STATED AS A
LOWER BOUND: these are JSON bytes at rest, and the in-memory form is larger.

**The caveat that travels:** 1,218 channels at median 3 records means the store
is dominated by short-lived arias. A libretto held for weeks, or a role patched
every turn, moves the p99 and starts the bound working. The null says the bound
HAS NOT WORKED YET — not that it will not.

### STAGE 2, SCORED — 9ed3f561, 2026-08-18 21:0x

`46f09f6c -> da6a47a7` (the memo), executor 3ba636c7. **All four expectations,
registered before the code existed, MET.** Reproduced by the arm before being
reported; counts only, no time taken or wanted.

    COLD at LT 60 over base 38   Unmarshal=1  Marshal=0  Apply=23  = 60-38+1
    WARM forward to LT 62        Unmarshal=0  Marshal=0  Apply=2   = 62-60
    REPEAT at 62                 0 / 0 / 0
    FORK, cold at 62             Unmarshal=1  Marshal=0  Apply=25  = 62-38+1
    StateAt itself               25 / 25 / 25, UNCHANGED

Unmarshal 25 → 1. Marshal 25 → **0**, so nothing routed through `formReduce`.
**Apply did not fall**, which was the number that mattered: a fold that gets
cheaper skipped records and is wrong rather than fast.

**The memoLanded question, ruled (3):** split, and the flag RETIRED rather than
flipped. The memo is a path BESIDE `StateAt`, not a change to it, so flipping
would have gone red for the right numbers on the wrong subject. `StateAt`'s
25/25/25 is now a PERMANENT STATEMENT — movement there means figwal changed or
the memo leaked into a path it may not touch.

**The arm's fifth instrument fault of the day, and the sharpest.** The grading
harness called `FormSnapshotSource` directly and never went through the
`SnapshotCursor` that holds the memo. **The cold case passed exactly as
registered — 1/0/3, perfect and meaningless.** Only the WARM case went red
(Apply=25, Unmarshal=1 where the memo gives 22 and 0). The assertion added to
distinguish *a memo that is never reached* caught *an instrument that never
reached the memo*. Same shape, other side. Without that case the duplicate
would have graded the memo with three green cases and no contact.

**Independence, stated honestly:** the arm's duplicate gives independence of
AUTHORSHIP, not of MECHANISM — both instruments call the same source and take
the segment base from disk. The only check on a different road is HEADER
IDENTITY, and it belongs to the implementer, which is the right way round.
