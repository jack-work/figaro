# Delta seam: pre-registered, before a line of it exists

Aria 3a9225b1, 2026-08-18, against `feat/layered-cache` @ 51e73b01.
Instrument: `BenchmarkRoundLoop{Deltas16,Deltas64,Deltas256,Wide,Tools8}`,
b.N-independent, sabotage-gated.

**I PREDICT DIMENSIONS AND MECHANISMS, NOT MAGNITUDES.** This campaign has
spent four predictions learning that magnitudes guessed from source shape are
worthless — three of the executor's and one of my own labels. Escape analysis
cannot be run against code that does not exist, so every allocation number
below is a SHAPE with a named mechanism, and the magnitudes are arithmetic from
the measured model, not intuition.

## THE FLOOR THESE ARE READ AGAINST, measured 2026-08-18

The round-loop benchmarks never had an A/A floor — they did not exist when the
clean pair was taken. One arrived by accident: `internal/figaro` and
`internal/tool` are **byte-identical** between 6fc784ef and 51e73b01
(`git diff --stat` empty), so those two runs are an A/A pair.

    bench          B/op drift   allocs/op drift
    Deltas16          0.07%        0.17%
    Deltas64          0.13%        0.47%
    Deltas256         0.02%        0.86%
    Wide              0.02%        0.97%
    Tools8            4.49%        7.04%   <- LOOSE

**Four of the five are tight enough that any real move is unmistakable.**
`Tools8` is not: 4.5% bytes and 7% allocations on identical code, almost
certainly tool-dispatch goroutine scheduling. Consequences, stated before the
numbers:

  - `Deltas256`'s allocation band (812 → 560-650) is a ~20-30% move against a
    0.86% floor. **Scoreable with room to spare.**
  - **No `Tools8` claim under 7% in allocations or 4.5% in bytes is
    admissible.** My prediction that it "should barely move" is therefore
    UNFALSIFIABLE below that threshold, and I say so rather than collecting a
    free confirmation from a benchmark too noisy to disagree with me.

## The model these come from

Measured, closed-form, no fitted parameter (addtext-quadratic.md):

    B/op = baseline(~170,912) + w*N(N+1)/2 + per-event terms

## Predictions

### 1. BYTES on the delta axis — the central claim

If the quadratic dies and the UI IR accumulates with amortised doubling:

    deltas   now         predicted        mechanism
        16     179,027   170,000-185,000  series is only 8,704 here; NEAR FLAT
        64     317,875   185,000-215,000  series 133,120 removed
       256   2,442,966   340,000-420,000  series 2,105,344 removed

**The shape claim, which matters more than the numbers: B/op must become
LINEAR in delta count.** Slope should fall from ~11,085 B/delta (measured
64→256) to under 1,000 B/delta.

### 2. WIDTH AXIS — the strongest and least ambiguous test

    now 2,703,148 B to produce 65,536 bytes of text = 41.2x AMPLIFICATION
    predicted ~700,000 B = ~10.7x

I nominate this as the headline number: **"41x the text produced, down to
~11x"** is harder to misread than any percentage.

### 3. ALLOCATIONS — the plan CONCEDED this band before the code existed

**091d162e withdrew its own prediction and adopted mine, unprompted, before
any code was written.** Its reasoning had been "one node-buffer append per
delta replaces one string realloc per delta"; the error is that an append into
an amortised-growth buffer is **not one allocation per element**, it is
log(N) reallocations amortised to O(1). Recorded as SUPERSEDED, not deleted,
with the reason: *a data structure's behaviour reasoned about instead of its
growth policy* — the same class as predicting allocations from source shape,
one level up.

6defe6f9 then committed to the design that makes the band falsifiable rather
than a coin flip: **delta events stay VALUES through the existing buffered
channel**, not pointers or interfaces, so the event lives in the channel ring
rather than the heap. On that design my band should win; if allocations come
back flat at ~812 anyway, something in the splice path allocates per delta and
that is theirs to find before any bytes are claimed.

### 3b. The original disagreement, kept because the diagnostic outlives it

The plan pre-registers *"812 → roughly unchanged, possibly slightly higher"*.
**My arithmetic says they should FALL.** Measured 600 → 812 across 16 → 256
deltas is **~0.88 allocations per delta**, and that is the concatenation: one
new string per `+=`. Replace it with amortised growth and ~240 of those
allocations disappear at Deltas256.

    my prediction: 812 -> 560-650   (concatenation allocations removed)
    plan's:        812 -> ~812 or higher

**This is a real disagreement and it is diagnostic either way:**
  - falls to ~600 → the splice path allocates well under one object per delta.
  - stays ~812 → the new path allocates ~1 per delta and has REPLACED the
    concatenation rather than removed a cost; bytes improve, object count does
    not.
  - rises materially → the splice path allocates MORE than one per delta, and
    that is a finding to explain before claiming the bytes win.

### 4. THE 16-DELTA CASE — the honest risk

Near flat, 170,000-185,000. The series is only 8,704 bytes there, so there is
almost nothing to win and any new per-frame work shows up as a LOSS. If it
regresses beyond ~190,000 the seam costs more than it saves at short replies,
which argues for a threshold rather than a rewrite. I expect a small regression
here and I would not call it a failure.

### 5. TOOLS AXIS — should barely move

Stage 5 already removed the O(events × tools) derivation. The seam is about
text accumulation, so Tools8 should track Deltas64 plus its tool constant. **If
Tools8 improves substantially, something other than the concatenation changed
and it must be found before anything is claimed.**

## Falsifiers, in the direction that costs me

- **B/op still superlinear** → the message is still accumulated somewhere; the
  split is incomplete. My model would be right about the mechanism and wrong
  that the seam removes it.
- **Allocations flat at ~812** → my disagreement with the plan is lost, and the
  plan's instinct that the splice path costs one object per delta was better
  than my arithmetic.
- **Width axis amplification not falling below ~20x** → doubling growth is not
  what the UI IR does, and I have modelled a fix that was not built.
- **DISARMED BEFORE THE CODE, 2026-08-18**: 091d162e ruled accumulator (a) —
  `[]byte`, splices published, `string(buf)` copied only for a new subscriber.
  That hands out a fresh immutable copy rather than aliasing a live buffer, so
  the guarantee is REPLACED, not traded. If the implementation drifts to
  publishing the buffer directly, this falsifier re-arms and the capacity test
  becomes load-bearing.
- **Anything improving without the aliasing law being addressed** → speed
  bought by removing a guarantee, and it must be reported as a trade, not a
  win. Today's `+=` yields a fresh immutable string; that is not only a cost,
  it is the correctness the published-window law depends on.

## Method commitments

- reachability proof (sabotage → B/op moves) before any before/after
- `-gcflags=-m` on the NEW code before attributing any allocation change
- three benchtimes, B/op must hold, or the instrument is not comparable
- addText growth reported separately from anything attributed to the seam
- corrections in new paragraphs, never silent edits

## KNOWN BLIND SPOT, recorded before the work starts

`TestSmoke_SteerOrderMatchesShow` **SKIPS** on 51e73b01 — *"view auto-promoted
(chrome=2); re-run taller"*. So **the steer path has no pty coverage at all**,
and the delta seam changes how the live view is built, which is exactly what
that test would have guarded.

**A skip is not a pass. It is an absent test wearing a green suit** — instance
seven of the reaching-the-code finding, in a costume the others did not wear:
the test exists, reaches the code, and declines to run.

**AND IT IS NOT NEW, AND ITS CURE IS STALE.** Corrected by 6defe6f9, verified
here:

  - the memory campaign already filed it as open item 3 — *"Scenario 2
    (mid-turn steer) is unverifiable in the current harness; auto-promotes at
    101 and 201 with chrome=2"*. It has been wearing the green suit since
    before this campaign began.
  - the skip says **"re-run taller"** and the test ALREADY runs at
    `newPane(t, env, bin, 100, 100)` with the comment *"tall: tool-heavy turns
    auto-promote"* — the **second tallest pane in the whole suite** (only
    200x120 exceeds it). Someone hit this, followed the advice, bumped the
    pane, and it still promotes. **The remedy printed in the failure is the
    one already tried**, and following it again costs the next person a run.

**THE SUITE-WIDE SHAPE, which is worse than one test.** Of six smoke cases,
FOUR carry skip sites and `SteerOrderMatchesShow` carries two:

    ProcessExitsAfterTurn          none
    ErrorDoesNotBleedIntoStatusBar none
    ExitKeysWork                   1
    OneTurnOneFooter               1
    LettersAreKeybindingsNotText   1
    SteerOrderMatchesShow          2

A green smoke run is therefore consistent with **two-thirds of the suite having
declined to execute.** The suite reports pass/fail; it does not report how much
of itself ran. Nobody should read a clean tmux run after the seam as coverage
without reading the skip lines.

**FIXED 2026-08-18 (2eb6262f)**: 091d162e deleted the false advice — and
proved it false by TRYING IT A THIRD TIME, with bounded output as well as a
taller pane; it still promoted at chrome=2. The commit records a measured
refutation of a sentence rather than an opinion about it. The skip remains, now
labelled a known coverage hole rather than a flake.

**A path was raised and not taken** (6defe6f9): the test's real subject is
ORDER — exactly one steer marker, not adjacent to the inquiry header — and
`fig show` renders the same turn with no pager and no promotion. Asserting
order against `show` would run at any pane height and still catch the hoist it
was written for; what it loses is the pty-specific half, that the LIVE view and
`show` agree. The honest version keeps both: order on `show` (always runs) plus
a pty assertion that may skip. **Raised, not taken** — it is CLI-harness work
on a filed queue, and the seam does not need it to proceed.

Recorded by 091d162e so that a clean tmux run after the seam lands is **not**
mistaken for coverage of the steer path. `DetachedTail` also fails at its
documented vacuity guard (pre-existing, attributed).


## HANDOFF, 2026-08-18: what the successor inherits and what is broken

Baselines preserved out of /var/tmp into `bench/state-door/seam/`:
`before.txt` (the seam BEFORE at 51e73b01, carry-forward verified EIGHT times
to 2db3bd0a), `afterA.txt` (piece A), `aa1/aa2.txt` (the deliberate A/A that
produced the floors), `irA/irB.txt`, the benchstat outputs, and
`scanonly-before.txt` (55,399 ns, 0 B, 0 allocs, n=6 — a valid Part V before).

**OPEN, AND IT IS THE FIRST THING TO FIX: the Dangling fixture.**
`BenchmarkInterruptRepairDangling10000` is broken in three ways and only the
first is fixed:

 1. it measured the SCAN, not the repair — fixed by the split;
 2. `repairInterruptedTail` emits a WARN per call, so with stderr merged the
    log lands ON the result line. **Zero lines carried `ns/op`** and the raw
    output was **14,537,017 bytes of WARN flood**
    (`dangling-corrupted-sample.txt`). My parser read 19 ns / 0 B / 0 allocs
    from it, which is the third distinct route to the same species: not a
    shifted column, not a positional read, but **the measured program writing
    into its own results channel**;
 3. with stderr discarded, **twenty iterations did not complete in 300
    seconds.** The logger is plausibly inside the timed region.

RULED (7e151902): until the logger is out of the timed region, the Dangling
variant is an **allocation instrument only and must say so IN ITS OWN
COMMENT**, beside the b.N disclosure — in the file, not in a note, because the
next reader has the file and not us. Bytes and allocations stand (7,536 B, 20
allocs, exact at every b.N). **No Dangling wall number goes upward.**

Three defects in one fixture, and **each was found only by trying to use it.**
Reading it three times found none of them.
