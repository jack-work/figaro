# Two ways an instrument lies: reading the wrong number, and not reaching the code

**THE WRONG NUMBER SITS AHEAD OF THE MISSING ONE.** A wrong number survives
every check that a missing number fails: it is stable across runs, reproducible,
plausible in magnitude, and it agrees with itself forever. **A missing number
announces itself eventually; a wrong one never does.**

## SPECIES II: the instrument reads the wrong number

`go test` output **is not a fixed-width table.** `b.ReportMetric` shifts every
column after it, so **any positional parser silently reads the wrong quantity**
the day someone adds a custom metric.

`BenchmarkCatchUp/WarmDeltaEncode` calls `b.ReportMetric(2, "messages/op")`.
A reachability run read field 5 as `B/op`, got the literal `2`, and **compared a
hardcoded constant against itself across a sabotage**, reporting `delta +0,
NOT REACHED`. The verdict happened to survive re-measurement on real bytes
(206,705 → 206,705); the evidence for it *could not have failed*.

**The fix is general: read the field BEFORE the label, never the field at an
index.**

### Instance ten: a check whose SUCCESS and whose ABSENCE are identical

6defe6f9 nearly reported a gate green off **a log file that did not exist.**
The check was:

    grep -Ev "^ok|no test files" | head

which prints nothing when every test passes, **and prints nothing when the
file is absent.** The two outcomes are byte-identical, and the absent one was
read as the passing one.

This is the same species as the parser reading a hardcoded constant, in a
shell's clothes, and it generalises past both: **"no output" is the default of
almost every Unix tool, so "everything is fine" and "I never ran" produce the
same bytes.** Its gates now assert the file exists and COUNT ok-lines before
believing a verdict, a positive assertion rather than an absence of
complaint.

**Four of the ten are now "the instrument could not have failed"**, which is
the sub-species worth watching for: not a wrong answer, but a check with no
failing state reachable from where it was pointed.

### Instance nine: the same bug in the gate's PRIMARY signal, dropping instead of misreading

`compare.py`, the tool gating every before/after in this campaign, required
`" B/op"` to follow `" ns/op"` **immediately**. On any benchmark calling
`b.ReportMetric` it matched nothing for bytes or allocations, printed `-`, and
the summary counted it as **"allocation numbers moved in 0"**.

The awk MISREAD. This DROPPED. **Same species, better manners, and the politer
failure is the more dangerous one:** a benchmark whose allocations *could not
be seen* was scored as one whose allocations *did not move*. "Moved in 0 of 8"
was the sentence being quoted upward as evidence that the primary signal was
sound.

It happened to be true only because no panel benchmark calls `ReportMetric`,
**luck about which benchmarks were listed, not about the parser.**

**Standing split**: `benchstat` (documented format, named units) where it fits,
because that removes the class rather than patching it a third time.
`compare.py` only for the report shapes benchstat does not produce, the floors
file, the ABSENT ledger, per-benchmark bands.

Beside it, from the same pass: a guard whose own failure path was broken, the
reachability trap fired before `SINKFILE` was assigned and died with
`unbound variable` instead of its real message. **A guard that cannot report on
the day it matters is not a guard.**

## SPECIES I: the instrument does not reach the code, ahead of noise

Aria 3a9225b1, 2026-08-18. **This project's dominant benchmark failure mode is
not measurement noise. It is the instrument never touching the code it claims
to measure.** Five instances in two days, five distinct mechanisms, and every
one of them reported a plausible number that a reasonable person would have
quoted.

Noise, by comparison, is a solved problem: the A/A floor measured a median
benchmark stable to 0.9%, and per-benchmark floors handle the rest. Nobody was
ever going to be badly misled by noise. Everybody was repeatedly, nearly
misled by reachability.

## The five, with their distinct mechanisms

**1. STOPS DOING THE WORK**, caught by a guard, before this arm existed.
`TestEmitFrameBenchmarkActuallyComposes` exists because a frame benchmark could
be optimised into emitting nothing while still reporting nanoseconds. Four of
that test's own assertions were wrong before they were right. *Mechanism: the
work is removed after the benchmark is written.*

**2. NEVER DID IT**, `BenchmarkCachedLogReadWhileAppending`, 1,000,000,000
iterations at 0.4235 ns/op. The body is `if _, ok := c.PeekTail(); !ok`, which
DISCARDS the entry, so the compiler proves the load dead and deletes it.
Discarded 0.53 ns, consumed 10.19 ns: **19x**. It reported the best-looking
number in the tree for the exact mechanism the campaign cared about, and the
honest cost it hid was ~10 ns. *Mechanism: dead-code elimination of the
measured expression.*

**3. MEASURES THE WRONG PATH**, `BenchmarkCachedLogReadFromParallel`, source
of the cited "516 ns lock-free read". It read at a FIXED coordinate the writer
trimmed past, so **97.9%** of its reads fell through the published view into a
mutex-guarded inner log (mutex profile: 97.95%, agreeing to 0.05 points). The
benchmark whose comment claimed it measured "the absence of acquisitions on
the hot read path" measured their presence. *Mechanism: the fixture drifts out
from under the coordinate.*

**4. NO FIXTURE FOR THE CHANGED CODE**, stage 1 (one fold).
`BenchmarkProjectMessages_10msgs/_100msgs` pass an empty `form.Snapshot{}` and
carry ZERO patches; the changed `renderPatchBlocks` returns early unless
`len(patches) > 0`. The changed lines cannot execute. *Mechanism: the fixture
never enters the conditional branch that was changed.*

**5. RIGHT PACKAGE, WRONG ENTRY POINT**, stage 5 (one in-flight turn).
`BenchmarkEmitFrameStreamingPartial` drives `gov.Feed` + `composeTurn` +
`emitDelta`. The change is `a.turn.asm = asmMsg` inside `driveOneRound`. The
removed copy was per BUS EVENT; the benchmark simulates streaming WITHOUT the
bus. `composeTurn`, `emitDelta` and `regionMessages` contain zero references to
`turnState`; NO benchmark in `internal/figaro` calls `driveOneRound` at all.
*Mechanism: the package is right, the call chain is not.*

## 6. A DIFFERENT MECHANISM: reaches the code a VARIABLE number of times

Instances 1-5 are all "the instrument does not reach the code". **Instance 6 is
not.** It reaches it perfectly, and a different number of times on every run.

`BenchmarkRoundLoop*`, built by this arm to close the hole instances 4 and 5
exposed, was wrong twice:

  - **v1** reused one agent across iterations, so every iteration appended into
    the SAME assistant message. Per-op cost: **229 KB at `-benchtime 20x`,
    8.6 MB at b.N≈5000, on identical code.** It was measuring its own
    iteration count.
  - **v2** reset the TURN each iteration and still grew (473 KB / 1.4 MB /
    4.8 MB at 50x / 200x / 800x), because resetting the turn does not reset the
    CONVERSATION: every round appends to the log and the region read gets
    longer.
  - **v3** rebuilds the agent under `StopTimer`: 179,913 / 179,640 / 179,179 B
    across a 16x range of b.N.

Both broken versions produced stable, plausible, repeatable numbers.

**STANDING POLICY:** *a benchmark whose per-op cost depends on b.N cannot be
compared across commits, because b.N is chosen by the timer and differs
between runs.* Check it by running the same benchmark at three benchtimes and
requiring B/op to hold. It costs one minute and it would otherwise have
silently poisoned every stage comparison from here to stage 10.

## What they have in common

None of them looked broken. Each produced a stable, repeatable, plausible
number with a tight variance. Instances 2 and 3 had been quoted in design
documents. Instances 4 and 5 would have been reported as "no change", and
"no change" from an instrument that cannot see the change is indistinguishable
from "no change" from a correct one, **which is exactly why the failure
survives review.**

The A/A floor cannot detect any of them: an instrument that measures nothing
measures it very consistently.

## A CASE WHERE THE RIGHT ANSWER WAS "BUILD NOTHING" (091d162e, 2026-08-18)

Species II was found while asking whether `BenchmarkCatchUp` could price the
deletion of `acceptAssistantProjection`. It cannot: a megabyte retained per
call moved `WarmDeltaEncode/1000` by **exactly zero bytes**. The obvious next
move, build a send-path benchmark, was **ruled against**, and the reasoning
belongs here because "measure it" is not always the answer:

  - the COST of the deletion is bounded by inspection at one CACHED fold per
    turn; a bespoke instrument to confirm a small, already-reasoned bound is a
    poor trade against building the thing itself;
  - the RISK is not cost at all. It is the warm path **silently falling cold**,
    which is a CORRECTNESS failure no benchmark can see;
  - the round-loop instrument cannot substitute: it drives a SCRIPTED provider
    and never reaches a real `acceptAssistantProjection`. Checked before
    ruling, not hoped.

So the gate became a **hazard test, not a number**: cold process, warm disk
state, assert the cached projection is ACCEPTED rather than rebuilt, canaried
by breaking the watermark bookkeeping and proving the test goes red.

**A measurement arm's most useful output is sometimes "this cannot be measured
cheaply, and here is what to prove instead."**

## STANDING PREFERENCE: where the question is "how many times", COUNT it

Twice earned in one day. 6defe6f9's `stats.Cached != 1` priced a deletion
better than the benchmark that was declined, exact, machine-independent,
immune to a 4.5% floor, and permanently checkable. The delta seam's
one-segment bound will be asserted the same way.

**Timing answers "how expensive". Counting answers "how often". Most of this
campaign's real questions have been the second kind wearing the first kind's
clothes**, the fork seeds (SHARED vs MINTED, not bytes), the hop gate
(fall-throughs, not milliseconds), `countingLog`, and now the cached fold.

## THE GATE, standing from stage 2 onward (091d162e, 2026-08-18)

No before/after is admissible without a **REACHABILITY PROOF**: evidence that
the benchmark EXECUTES THE CHANGED LINES. The cheapest sufficient form is the
canary discipline turned from correctness to reachability,

> **BREAK THE CHANGED LINE ON PURPOSE AND SHOW THE BENCHMARK MOVES.**

A panic, a sleep, a doubled allocation; anything unmistakable. If deliberately
breaking the code under test does not move the number, the benchmark does not
measure it, and that is knowable in **two minutes before** the real run rather
than by reasoning after it. Acceptable alternatives where cheaper: line
coverage of the changed lines under `-bench`, or a counter on the changed path
asserted non-zero.

What is NOT acceptable is running first and reasoning about reachability
afterwards. **That is how all five arrived.**

## Why this was found

Not by being careful on a good day. By turning the instruments on the
instruments: a fixed named list that made absence loud, an ABSENT/NO-RESULT
split that refused to let a silent benchmark vanish, canaries that had to be
BUILT and RUN and reverted, and per-benchmark floors that made "no change"
mean something specific enough to be doubted.

Two of the six were the executor's, two were the placer's, and **two were this
arm's own**, instance 6 being the measuring device itself, wrong in two
different ways before it was ever used. A gate that only ever found other
people's errors would have been the seventh instance.

Two of the five were caught in this arm's OWN work, a canary that tested an
expression nobody runs, and a provider list built to fix instance 4 that was
blind for the same reason. **The method has to be applied to the person
applying it**, or it finds only other people's errors.

## The gate's first two runs, including on itself

**Run 1, the tool failed itself.** `reachability.sh` read the first line of
`anthropic.go` to find the package name and got `Package` from the doc comment
`// Package anthropic ...`. The sabotage did not compile, and the script
reported **INCONCLUSIVE, PROVES NOTHING** rather than a verdict. That is the
only failure a proof-tool may have: it must never convert its own breakage
into an answer about the code.

**Run 2, it worked, on the patch-carrying fixture built for stages 8/9.**
Sabotaging the two functions stage 1 changed moved B/op from 9,200 to
11,543,541, **+11.5 MB**. The fixture provably executes the changed code.

That closes the loop 091d162e asked for: the fixture was validated twice, by
two independent standards.

  - **It detects nothing when nothing changed.** Across stage 1 (byte-identical
    encoder output) allocations were IDENTICAL and ns moved at most 2.2%. A
    fixture whose first job is to report a null, and does, can be trusted to
    report a non-null later.
  - **It reaches the code.** Proven by sabotage, not by argument.

A fixture with only the first property is the failure this note is about: an
instrument that reports "no change" perfectly, forever, because it cannot see.

## THE PTY ANALOGUE, and two instances of it in one day

Instances 1-10 are about benchmarks and gates. The same disease has a pty form,
and 091d162e hit both halves of it on 2026-08-18.

**A test that watches the wrong side of the boundary.** `Ctrl-C` never stopped
the turn: `inputInterrupt` cancelled the CLIENT's context and returned
`keyStop`, and nothing in `internal/cli` ever sent `figaro.interrupt`. The
daemon was never told; the turn ran on. `TestSmoke_ExitKeysWork` sends `C-c`
mid-stream and asserts **the process exits**, which it did, all along, by the
broken path. **A test that watches the client cannot see a bug whose shape is
"the client leaves and the daemon does not stop."** Fixed at 2d5dd424.

**A vacuity guard satisfied by its own prompt.** The first guard written for it
was `strings.Contains(p.visible(), "bash")`, and the prompt contained the word
"bash", which the pane echoes on keypress. It **passed in 6.49 seconds having
asserted that a turn which never started was not running.** The harness
documents that exact trap at `bodyLines()`. The guard now asks the same source
the assertion asks, and currently SKIPS rather than lying.

Both are the note's thesis in a different medium: the instrument reported on
something other than the thing under test, and reported it in a plausible,
stable, repeatable way.

## INSTANCE ELEVEN: two empty files, compared, and reported as agreement

Aria 9ed3f561, 2026-08-18, in its first hour and in its own harness. The claim
under test was that a build-tagged counting hook costs the DEFAULT build
nothing. The check compiled `internal/form` with and without the hooks under
`-gcflags=-S`, extracted the three hooked functions with

    awk '/^TEXT/{p=(...)} p'

and diffed the results. It printed **IDENTICAL OPCODE HISTOGRAM**.

The extraction matched nothing. `go build -gcflags=-S` writes the symbol name
on its own line, `foo.Snapshot.Apply STEXT size=517 ...`, and only the
INSTRUCTION lines contain the string `TEXT` in the column the pattern was
anchored to. Both sides produced ZERO lines, `diff` was satisfied, and the
tool announced the conclusion the author expected.

Same species as instance ten, and the same sub-species as four of the first
ten: **not a wrong answer, but a check with no failing state reachable from
where it was pointed.** It differs from instance ten only in that the empty
thing was a filter's output rather than a file that did not exist, which is
to say, not at all.

**What replaced it, and the two properties worth copying.** The check now
compares the compiler's own `size=` field for EVERY symbol in the package
(222 of them), and it asserts the extraction is non-empty BEFORE it is
allowed to conclude anything. Then it was **sabotaged**: `countApply` made to
increment a global, `Snapshot.Apply` grew 517 → 534 bytes, and the check went
red. A size comparison that has never been shown to differ is not evidence
that two builds agree.

The first guard written for the non-emptiness assertion demanded more than 100
symbols and fired at 92, a threshold picked by guess, failing loudly on a
correct extraction. **That is the right direction for a guard to fail**, and it
cost one minute to widen; the version that fails silently costs a campaign.

## A DIFFERENT SPECIES: THE ORACLE WHOSE SUBJECT IS INVARIANT UNDER THE ERROR

Found by aria 041454f1, 2026-08-18, ruled and recorded by d921742d (role
@980dc16c). Instances 1-10 are instruments that do not REACH the code, or
that read the WRONG NUMBER. This one reaches the code, reads the right
number, and is still blind, because the property it compares CANNOT MOVE
in one of the two directions the error can take.

THE SETTING. The delta seam's stage 2 assembles a form snapshot from a
segment HEADER (everything before this segment, already folded) plus the
segment's own records. The classic guard is an equivalence oracle:
fold-from-header must equal fold-from-zero at every LT. The obvious canary
is an off-by-one in the header.

THE MEASUREMENT, canaried in BOTH directions, built and run and reverted,
over a 15-record fixture swept at every (segBase, lt) pair, 136 pairs:

    header SKIPS a record (one behind)   caught everywhere, immediately
    header is ONE RECORD AHEAD           caught in 13 of 136 pairs,
                                         ALL of them lt == segBase,
                                         and 0 OF THE 120 with lt > segBase

WHY. Form patches are IDEMPOTENT: re-applying a record the header already
holds is a no-op, so a header that has run one record too far produces
byte-identical output at every LT past the boundary. The 13 that were
caught are the degenerate ones where nothing else had yet been applied.

SO AN EQUIVALENCE ORACLE IS STRUCTURALLY BLIND TO HALF OF THE BOUNDARY
ERROR, and no fixture improves it: the blindness is a property of the
operation, not of the corpus. The direction it cannot see is the dangerous
one, a boundary that reads TOO MUCH is exactly how a fold silently
includes state it should not.

WHAT COVERS IT: A COUNT, AND IT MUST BE AN EQUALITY. "At most one
segment's patches", the bound the design first specified, IS SATISFIED
by a header one record ahead. The assertion is therefore sharpened to an
exact count: folds == lt − segBase + 1 on a cold memo, == lt − lastMemoLT
on a warm one. That is the only instrument that fails in the invisible
direction, and it costs nothing more than the bound did.

GENERAL FORM, worth carrying past this stage: WHEN THE OPERATION UNDER
TEST IS IDEMPOTENT, COMMUTATIVE OR ABSORBING, AN OUTPUT COMPARISON CANNOT
SEE ERRORS THAT THE ALGEBRA ERASES. Ask what the operation is invariant
under, and instrument THAT, usually by counting how many times it
happened, which the algebra cannot hide.

BESIDE IT, FROM THE SAME RUN: the first classification of those 136 pairs
reported "12 failures where lt > segBase". It was an awk splitting the
subtest name and leaving "(0.00s)" glued to the field, so the comparison
was string-against-garbage; the true figure is 0 of 120. Instance ten
again, in the hands of someone who had read the note that morning, and
caught BEFORE the number was reported rather than after. Two workers found
that same species in their own work within an hour of each other tonight.
The note works when it is applied to the person applying it, and only
then.

### AND ONE LEVEL UP: AN INSTRUMENT WHOSE REFERENCE COMES FROM THE THING
### UNDER TEST

Same evening, same boundary, and it corrects the fix written two sections
above. d921742d ruled the one-segment assertion up from a BOUND to an
EXACT COUNT, folds == lt − segBase + 1, on the grounds that a bound is
satisfied by a header one record ahead. Aria 9ed3f561 falsified that on a
reference model, before any code existed to be judged by it.

THE EXACT COUNT IS BLIND TO AHEAD-BY-ONE FOR THE SAME REASON THE ORACLE
IS: both the expected and the actual count are computed from THE HEADER'S
OWN DECLARED BASE. A header holding one record too many that still
declares base b makes the fold apply records[b..lt], exactly the expected
number, and the surplus re-applies idempotently, so the value matches
too. The equality passes; the boundary is wrong.

    AN INSTRUMENT WHOSE REFERENCE IS DERIVED FROM THE THING UNDER TEST
    CANNOT SEE AN ERROR IN THE REFERENCE.

That is the general form, and it is worth more than the instance. Both
prior fixes, count instead of compare, exact instead of bounded, were
improvements in the RIGHT DIRECTION that never touched the blind spot,
because each new instrument read its expectation off the same declaration.

WHAT SEES IT is one comparison per segment and no counting at all: THE
HEADER AGAINST A FOLD FROM ZERO AT ITS DECLARED BASE. Measured on the
model, ahead-by-one caught in 4 of 4 segments and behind-by-one in 3 of 4
(segment zero's header is empty under either clamp, stated rather than
rounded up). The earlier oracle's 13-of-136 catches were this check
reached BY ACCIDENT, at the one LT where the two coincide.

SO THE BOUNDARY OWES THREE INSTRUMENTS, each covering what the others
structurally cannot: header identity (ahead-by-one), exact fold count (a
memo that re-folds, a bound merely met), value oracle (skips and wrong
values). Drop one and a specific error class goes invisible.

BESIDE IT, THE THIRD BLIND CHECK OF ONE EVENING, and it is the sharpest.
The ahead-by-one comparison was first written with fmt.Sprint on two
form.Snapshot values, a struct carrying a tree POINTER, so it compared
ADDRESSES, reported a difference that had nothing to do with the data,
AND REPORTED IT IN THE DIRECTION THAT FLATTERED THE RULING IT WAS TESTING.
Caught by its author, fixed, and it then said the opposite.

Three blind checks in one evening, all three found by the arm that built
them, none of them claimed on. Add the executor's awk leaving "(0.00s)"
glued to a field and reporting 12 failures that were 0 of 120, also caught
before it was reported. Four in one evening, from people who had read this
note that morning. The note does not prevent the disease. It shortens the
time between committing it and catching it, which is the only thing a
method can honestly promise.

### A REPORTING RULE THE SAME EVENING: THE DENOMINATOR IS THE APPLICABLE
### CASES, ENUMERATED

Aria 041454f1, ruled standing by d921742d. Canarying the header-identity
check meant sabotaging the boundary at every base in a 16-position sweep
and counting where the check fired. The honest figure is 13 OF 13
APPLICABLE, not 13 of 16.

Two of the fixture's records are semantically-equal re-sets that
`ptree.Set` DROPS, so a header off by one of those is LITERALLY THE SAME
HEADER; the third is the clamp at the sweep's edge, where the sabotage
could not perturb anything. AN ERROR WITH NO EFFECT IS NOT AN ERROR ANY
INSTRUMENT CAN SEE, and scoring those as misses understates an instrument
that caught everything it could.

THE RULE: a canary's denominator is the cases where the sabotage could
PERTURB THE SUBJECT, and the inapplicable ones are ENUMERATED WITH THEIR
REASON, never silently dropped. The enumeration is what separates this
from the same arithmetic used to flatter a weak instrument.

### AND ONE MORE, FROM A SLIP THAT LANDED RIGHT

The same aria wrote "Gate: EXIT=0, 41 ok" into a commit message BEFORE
that gate had finished. It finished green, 43 ok, EXIT=0, asserted off a
log file whose existence was checked, so the claim was true. It was true
BY TIMING, not by process: asserted first, verified after, which is the
shape of every claim this campaign has had to withdraw.

THE RULE: the claim is written AFTER the verification, never before, even
when you are certain. Where a commit message must carry a gate result, the
message is AMENDED once the gate reports rather than predicted ahead of
it. A slip that happens to land right is the only kind nobody notices,
which is exactly why it was worth disclosing.

### THE SEARCH IS AN INSTRUMENT, AND NOBODY EVER RE-RUNS IT

Aria 7e151902, 2026-08-18, closing the Ctrl-C question by CANARY rather
than by argument. The clearest instance in this note, because the false
belief survived three honest corrections.

THE CLAIM, as told to Gluck: Ctrl-C never reached the daemon;
`inputInterrupt` cancelled the client's own context and left, so the turn
ran on. FIXED at 2d5dd424 by sending `figaro.interrupt` explicitly.

THE CORRECTIONS, each honest and each an improvement:
  1. "the turn ran on with nobody watching" -> UNPROVEN, because the pty
     case passes on the broken code.
  2. the registry lead ("list -j says dormant for an agent that left the
     live registry") -> EXCLUDED by inspection: Unbind never touches
     r.figaros and both reclaim paths refuse a non-idle agent.
  3. a durable-log instrument built, because only that can distinguish
     "stopped by the RPC" from "stopped somehow".

THE CANARY THEN REFUSED TO FIRE. Fixed build PASS 30.12s; build with
`inputInterrupt` reverted to `in.cancel(); return keyStop`, PASS 30.12s.
Identical to the centisecond, which was treated as a SUSPECT first (two
arms agreeing that closely are more often one binary than one bug) and
checked: the harness rebuilds from the repo root into a fresh TempDir, the
canary body was in the file it built, the binaries differed.

THE MECHANISM: `internal/cli/stream.go`'s `case <-ctx.Done():` arm ALREADY
CALLS `fcli.Interrupt`. The daemon was always told. THE CLAIM WAS FALSE,
not overstated.

WHY THREE PASSES MISSED IT: the founding evidence was a grep for
`MethodInterrupt` across `internal/cli`, which returned one hit, a
method-name list. The real call goes through the CLIENT WRAPPER,
`fcli.Interrupt`, which does not contain that identifier. Every later
correction refined the CONCLUSION and none re-examined the SEARCH THAT
FOUNDED IT.

    A SEARCH IS AN INSTRUMENT. It has the same failure modes as a
    benchmark, it can fail to reach the code, it can read the wrong
    thing, and its silence is indistinguishable from its success. It is
    almost never re-run when the conclusion it produced is revised, and
    "the grep returned one hit" is a measurement that nobody treats as
    one.

WHAT SURVIVES: 2d5dd424 is still right, on narrower and honest grounds,
explicit intent rather than a side effect of context cancellation, waiting
for turn.done rather than racing the exit, a second press as an escape
hatch. And the pty case is KEPT, because the property it pins is a
DISJUNCTION worth guarding: the daemon learns of the interrupt BY SOME
ROUTE, provably, after the client is gone. Two independent paths satisfy
it today; the test is the only thing that will notice when the last of
them is removed.

### CODA: A FAILURE MESSAGE IS AN INSTRUMENT TOO

Same evening. A fork-hazard test was written to assert that the naive
pairing of `HeaderAt` with `SegmentBaseIndexes` is WRONG below a fork
base, and its failure message instructed whoever landed the new call to
INVERT it. But adding a call does not change what the two old functions
answer about, so the pairing stays wrong, the test stays green, and the
instruction could never be triggered by the event it named.

Its author's own diagnosis, and it is the right one: **that is the failure
mode where the DOCUMENTATION is the instrument that does not reach the
code.** A message nobody can make fire is a message that will be obeyed
out of context, here, by inverting a true assertion into a false one to
make the instruction come true.

The assertion was kept and the MESSAGE was amended: the pairing is wrong
and will remain wrong, this is the standing reason the new call exists,
and anyone who makes this test fail has changed one of those two functions
and owes the design that depends on it a look. Assert the fact, not the
wish, and make the failure message describe the fact too.

### THE SHELL VARIANT, THIRD SIGHTING IN ONE EVENING: A STATUS THAT WENT
### THROUGH A PIPE

Aria ba221ff1, 2026-08-18. The check was:

    nix develop -c go build ./... 2>&1 | tail -5 && echo BUILD_OK

It printed BUILD_OK over a FAILED build. `&&` reads the exit status of
the LAST stage of the pipeline, and `tail` always succeeds. The failure
was caught only because the compiler's error text happened to sit visibly
above the word BUILD_OK, which is luck about output volume, not method.
A build that failed QUIETLY would have printed BUILD_OK over nothing.

    A STATUS THAT PASSES THROUGH A PIPE IS A MEASUREMENT OF THE LAST
    STAGE, NOT OF THE WORK.

STANDING: no `&& echo OK` after a pipe. Use `PIPESTATUS`, or run the
command unpiped and inspect its code, or write to a file and assert the
file's CONTENTS, the positive assertion, as with counting ok-lines rather
than grepping for the absence of complaint.

Three shell-shaped instances in one evening, from three different arias:
a grep whose success and whose absence printed identical bytes; backticks
inside a double-quoted argument silently deleting a word from a report;
and this. None of them is exotic. All three are the default behaviour of
tools everyone uses hourly, which is exactly why they keep arriving.

### AND THE THIRD OF THE FAMILY IN ONE EVENING: A STATUS READ FROM
### SOMETHING THAT NO LONGER EXISTS

Aria ba221ff1, 2026-08-18. The gate was run as a transient systemd unit
with `--collect`, then polled:

    systemctl --user show fig-pin-gate -p ActiveState,Result --value

It printed `inactive` / `success`, FOR A UNIT THAT NO LONGER EXISTED.
`--collect` had already removed it, and `systemctl show` prints DEFAULTS
for a unit it cannot find. The log file said FAIL. Trusting the status
field would have reported a green gate over a red one.

    A STATUS READ FROM SOMETHING THAT MAY NOT EXIST IS A DEFAULT, NOT A
    MEASUREMENT.

Three sightings in one evening, and in ALL THREE the ARTIFACT was right
while the STATUS was wrong: a pipeline returning `tail`'s exit code over a
failed build; a grep whose success and whose absence printed identical
bytes; and this. The standing answer is the same every time, ASSERT THE
ARTIFACT: the file exists, and it contains this many ok lines. A positive
assertion about a thing that was produced, never an inference from a field
that has a default.

## THE SUMMARY SENTENCE FOR THE WHOLE FAMILY

Found 2026-08-18 across four arias in one evening, and adopted by d921742d as
the compression of everything above:

> **THE INSTRUMENT ANSWERED, AND THE ANSWER WAS ABOUT SOMETHING NARROWER THAN
> THE QUESTION.**

It covers every instance in this note. A benchmark field read BY POSITION
answers about the column, not the metric. A `systemctl show` on a `--collect`ed
unit that no longer exists answers with DEFAULTS, "inactive/success" for a run
that failed, because it is describing a unit it cannot find. `figaro ls -a`
answers about the SCOPE you are attended to, not the census you asked for. A
grep answers about the text, not the call graph. Two empty files compared
answer about nothing, and agree. A fold count computed from the header's own
declared base answers about that base, not about whether it is right.

None of these is a wrong answer to the question asked. Each is a correct answer
to a NARROWER question, wearing the wider question's clothes, which is why
they survive review, agree with themselves, and fail toward whatever the reader
already believes.

### TO REPRODUCE A NARROW INTERLEAVING, REDUCE THE PARALLELISM

Aria 6ec565b5, 2026-08-18, hunting a lost update whose window is one
goroutine descheduled between a durable write returning and an in-memory
publish, microseconds wide.

    N in {8, 32, 64, 256} x 10 runs, -race, box at loadavg 27-34
                                        40 runs, ZERO failures
    GOMAXPROCS=1, N=8                   RED 2 of 10
    GOMAXPROCS=1, N=64                  ZERO of 10

MORE CONCURRENCY MADE IT LESS LIKELY, NOT MORE. Raising N spreads one
narrow window across more goroutines, each of which gets less of it;
pinning GOMAXPROCS forces the interleaving the window actually needs.

Had only N been swept, the report would have read "not reproducible at
N=256", which sounds like evidence of absence and is evidence of a WRONG
LEVER. That is this note's thesis in a scheduler's clothing: the
instrument ran, produced a stable and plausible result, and was measuring
something other than the question.

BESIDE IT: the same aria first tried to raise the failure rate by spawning
sixteen spin loops to fake system load. That moves the LOADAVG NUMBER, not
the preemption rate inside the test process, the wrong variable, found by
measuring rather than by argument, and killed on the spot.

## INSTANCE TWELVE: a token that LOOKED like a commit and was 2 MiB

Aria 9ed3f561, 2026-08-18, auditing other people's gate logs at the bearer's
request, and the audit's own first pass is the instance.

The question was "does this gate log record the commit it ran at?" The check
grepped for the first hex-looking token:

    grep -oE '\b[0-9a-f]{7,40}\b' "$f" | head -1

and reported `commit-stamp=127a128` for one log, `123c123` for another, and
`2097152` for a third. **None is an object.** `git cat-file -t` rejects all
three, and **2097152 is 2 MiB, the segment size, printed inside an error
string.** Two logs would have been credited with provenance they do not have,
by an audit whose entire purpose was provenance.

**The fix is the general one and it is not "a better regex": VALIDATE AGAINST
THE OBJECT DATABASE, NEVER PATTERN-MATCH.** A thing that looks like a commit is
not a commit; only the repository can say. This is the same correction as
reading a benchmark field BY LABEL rather than by position, ask the authority,
not the shape.

## THE STANDING RULE IT PRODUCED (@980dc16c, 2026-08-18)

> **A GATE LOG THAT DOES NOT NAME ITS TREE IS INADMISSIBLE AS EVIDENCE.**

Not *wrong*, **unfalsifiable**, which is worse, because it agrees with
whatever the reader already believes. Applied RETROACTIVELY to all eight logs
on this box. What that does and does not mean, stated so nobody has to guess:
it does **not** make tonight's verified claims false; those rest on
reproductions, canaries and re-verification, **not on the logs**. It means no
unstamped log may be cited from here.

It was earned: `gate-quiet.log` (19:18, green, 41 ok) was quoted in support of
a tree committed at **20:06**. The log was not lying. It could not be checked.

**The shape that makes the rule operative**, because a rule nobody can run is a
preference: `scripts/measure/gate.sh` stamps HEAD, dirty count, go version and
loadavg **before any test output and again at the end**, and
`scripts/measure/checkgate.sh` REFUSES a log with no stamp, with a token that
is not an object, whose BEGIN and END stamps disagree, that carries no
terminator, or that names a tree other than the one it is offered for.

**BOTH STAMPS, AND THIS IS THE ADDITION FROM THE GATING SIDE:** a stamp written
only at START describes the tree the gate BEGAN on. A gate takes minutes; an
executor editing during it produces a log that is **stamped, green, and lying**,
the failure the stamp exists to prevent, wearing the stamp's own clothes.

**The red corpus already existed.** All eight unstamped logs are REFUSED; a
freshly stamped one is ADMISSIBLE; and each refusal clause has its own canary,
tree moved, token not an object, terminator stripped, wrong tree expected, all
four fire, and the green log is still green afterwards. A canary that was never
run is a branch, not a check.

## INSTANCE THIRTEEN: a command that SUCCEEDED is not an artifact that is CORRECT

Aria 9ed3f561, 2026-08-18. **Three separate times in one evening, a git command
exited zero and produced a wrong artifact**, and every one was caught by
checking the artifact rather than the exit code.

  1. **A cherry-pick that silently did nothing.** `git cherry-pick -q <a> <b>`,
     `-q` is not a flag it takes. It printed usage, and the `&&` chain carried
     on into `format-patch`, which exported **the recipient's own last two
     commits** under my cover letter.
  2. **A patch naming the wrong commits.** `git format-patch -2 <rev>` takes the
     revision as the range END, so it exported the two commits ending at my
     first, not the two after it. The executor caught that one, it read the
     Subject lines out of the file before applying, and refused to hand-type the
     assertion in.
  3. **A patch duplicating its own hunks.** `format-patch` on one commit emitted
     the diff for a file TWICE: 251 lines where `git show` gives 130, one tree
     entry per path, no `format.*` config anywhere. A patch carrying a hunk
     twice cannot apply, and would have failed in the recipient's hands looking
     like a conflict with THEIR work rather than a defect in my export.
     **UNDIAGNOSED, DELIBERATELY** (@980dc16c): chasing git's internals costs
     more than the universal check buys. Recorded with symptoms so the next
     aria starts from a sighting rather than from disbelief.

**This is the status-versus-artifact rule ONE LEVEL OUT, and that is what makes
it a new costume rather than a repeat.** In the systemd case the status field
LIED about the work, `inactive/success` for a `--collect`ed unit that no longer
existed. Here **the exit code told the truth about a process that produced the
wrong thing.** Nothing lied. The command really did succeed; success was simply
never a statement about the artifact.

**THE STANDING RULE (@980dc16c, 2026-08-18):** every cross-aria code handoff is
`git apply --check`ed, or cherry-picked into a scratch worktree, **against the
RECIPIENT'S ACTUAL HEAD** before it is sent. Not the sender's head, not "it
exported cleanly". A cheap universal check makes the cause irrelevant, which is
the same trade as a fingerprint bump making a stale-bytes detector unnecessary.

It has now paid for itself three times in one evening, and the third time it
caught a defect nobody has explained.

## INSTANCE FOURTEEN: the defect I had just REFUSED, rebuilt by me, in a corner I was not watching

Aria 9ed3f561, 2026-08-18, about ninety minutes after refusing it by name.

**Refused, explicitly, as shape 2:** a finalizer cannot cheaply track a slice's
BACKING ARRAY. Attach a sentinel pointer and the pointer can die while the array
lives, so the object is counted as **collected while it is still pinned**, a
FALSE NEGATIVE, the direction that flatters. I wrote that to the role bearer as
my reason for leaving shape 2 open rather than shipping coverage for it.

**Then I built exactly that**, in a probe asking a different question: does the
projection retain what the ENCODER produced? The probe tracked by attaching a
finalizer to a `*[]byte` and handing the projection a `json.RawMessage` that
**shared the backing array**. Pointer dies immediately; array is retained; the
finalizer runs; the probe reports **0 of 200 held**. Its output would have been
"the instrument does not reach the defect", a measured finding, in the
direction that would have made the bearer WITHDRAW A REQUIREMENT.

Caught by reasoning about the mechanism before sending, not by the test failing.
**The test passed. It was designed to pass.**

**WHY IT IS ITS OWN SPECIES:** every other instance here was a failure mode
nobody had recognised yet. This one had been recognised, named, written down,
and used as the stated reason for a decision **by the same person, the same
evening**, and it still walked back in wearing different clothes, because I was
looking for it in "shape 2, patch subslices" and it arrived in "encoded output".

**A known failure mode does not stay recognised when it changes costume.** The
recognition attaches to the SITUATION it was learned in, not to the MECHANISM.
That is the form that survives review by authors who genuinely know better,
because knowing better is exactly what makes you stop checking.

The general defence is the one this note keeps arriving at from new directions:
**do not ask whether the instrument looks right; ask what its parts answer
ABOUT.** A finalizer answers about the object it is attached to. It never
answered about the array, in either corner.

## INSTANCE FIFTEEN: EMBEDDING HANDS YOU A SILENT BYPASS FOR FREE

Aria 9ed3f561, 2026-08-18. **Nothing was miswritten.** The wrapper is correct
Go, correct in intent, and the language quietly routed around it.

A counting wrapper embeds the interface it decorates and overrides the methods
it wants to count:

    type countingEntries struct {
        store.Log[message.Message]
        walked int
    }
    func (l *countingEntries) Read() []Entry[T] { l.walked += …; return l.Log.Read() }

`store.TailAfter` takes an **optional fast path**: if the log implements
`tailAfterLog`, it calls `TailAfter` and never calls `Read` at all. **Embedding
PROMOTES the base's `TailAfter` through the wrapper**, so the wrapper advertises
the fast path it does not implement, the promoted method runs on the BASE, and
the override is never consulted.

On a log WITHOUT the fast path (`MemLog`) the counter reports the whole log,
the fallback materialises everything before slicing. On a log WITH it (a real
`cachedLog`) **the same counter reports ZERO while the projection iterates
thousands.** The instrument is loudest on the fixture that matters least and
silent on the one that matters most.

**This is species I, the instrument does not reach the code, with the type
system doing the concealing.** Every other instance in this note involved
something written wrongly. Here the mistake is invisible at the call site, at
the definition, and in review: you must know that the *consumer* probes for an
optional interface, and that embedding satisfies that probe on your behalf.

**THE GENERAL RULE: a decorator over an interface with OPTIONAL FAST PATHS must
implement EVERY method the consumer may probe for, or it is a decorator only on
the paths nobody optimised.** Grep the consumer for type assertions before
trusting any wrapper. And it cost a wrong finding on the way in: the first
version reported *"the warm start is already walking with length"*, a headline
about production drawn from a fixture artifact.

Also adopted from the same pass, as the standard shape for read-cost questions:
count **MATERIALISED** (what the log produced to answer) and **HANDED** (what
the consumer received) **separately**. They differed by two orders of magnitude
on a fixture nobody would call unusual.

## INSTANCE SIXTEEN: THE AXIS RETIRED, AND THE COUNTER KEPT REPORTING

Aria 9ed3f561, 2026-08-18. **A new species**: not an instrument that fails to
reach the code, and not one that reads the wrong number, **one whose question
has been ANSWERED and which goes on answering it.**

The merge join was cut to remove cache lookups. The instrument counted lookups
per turn, and it worked: 51/101/201/401 → **0** at every length, exactly as
predicted. Then the counter kept running, reporting **0 in both columns, at
every length, forever.**

**A retired axis reads exactly like a clean result.** Zero lookups is the
success condition AND the reading of an instrument with nothing left to see. Had
nobody asked what REPLACED the lookups, the table would have shown a large win
and no cost, permanently, and every later reading of that axis would have
"confirmed" it.

What replaced them was a **forward walk over the translation channel**, counted
by nothing. Extending the counter to it, and re-running the SAME instrument at
the previous commit, so the comparison was fixture-identical in both directions,
produced:

                         WARM                          COLD
    before        lookups=1  cacheWalked=0     lookups=N+1  cacheWalked=0
    after         lookups=0  cacheWalked=N     lookups=0    cacheWalked=N

The cold path is the intended win. **The warm path, every live turn, went
from O(1) to O(N) in conversation length**, and no one was counting the axis it
moved to.

**THE RULE: WHEN A COUNTER GOES TO ZERO BECAUSE THE CHANGE SUCCEEDED, IT IS NO
LONGER AN INSTRUMENT. Ask what replaced the thing it was counting, and count
that instead, before the zero is quoted as evidence of anything.** A number
that can no longer vary is a constant wearing a measurement's clothes.

**And every falsifier described the side being FIXED.** The executor registered
entries-handed and decodes, both on the IR side. This arm registered "each
entry visited at most once", which the regression SATISFIES. The cost moved to
the side that was merely being *traversed*. **Visiting each entry once is not
the same as visiting any at all on a warm turn.** Nobody was careless: the
pre-registrations were all about the change and none about its neighbourhood.

## THE REVERSE OF INSTANCE THIRTEEN: A DEFENCE AGAINST SILENCE DOES NOT COVER NOISE

Same aria, same evening, one hour after proposing the rule it broke.

Instance thirteen's armour is *assert the artifact, never the status field*,
built for a command that **succeeds while producing the wrong thing**. Then:

    error: The following untracked working tree files would be overwritten
    by checkout: … Aborting

git failed **loudly**, printed exactly what was wrong, and the next step assumed
it had worked, producing a message that stamped a rehearsal at a commit the
worktree had never reached. The artifact-checking discipline does not fire here,
because the failure announced itself and was simply not read.

**The remedy is duller than any other rule in this note: READ THE OUTPUT OF THE
STEP YOU ARE ABOUT TO DEPEND ON.** Silence needs instrumentation; noise needs
attention, and attention is the thing a long chain of verified steps quietly
spends.

## THE PATTERN UNDER ALL OF THEM, which is about people (@980dc16c, 2026-08-18)

Three in one evening, each a *familiar rule in an unfamiliar costume*:

  - this arm rebuilt its own **refused** false negative an hour later, in a
    different corner (instance fourteen);
  - the role bearer found its own gate rule weaker than it thought, two hours
    after making it (the dirty-count scalar);
  - this arm broke the **rehearsal-stamp rule within the hour of proposing it**.

**A RULE IS NOT INTERNALISED BY BEING WRITTEN, OR EVEN BY BEING PROPOSED.**
Recognition attaches to the situation it was learned in. That is the finding,
not the lapses, which are only its evidence.
