# STANDING GOAL, GLUCK, 2026-08-19: REDUCE THE MUTEXES

His words: "your goal should be to reduce the mutexes. There are far too
many, and for reasons that arent legitimate, or are seemingly legitimate,
but by tight adherence to my principles they will reveal either poorly
designed dependencies or poorly followed rules. be on the lookout for
spurious locking, even where it appears valuable. it often is not, below
the surface."

## THE BASELINE, MEASURED SO THE GOAL IS CHECKABLE

83 `sync.Mutex`/`sync.RWMutex` declarations in non-test code, against 11
files using `atomic.Pointer`. By package:

    store 31 · cli 10 · tool 8 · provider 7 · angelus 7 · figaro 4
    outfit 3 · otel 3 · livelog 3 · render 2 · actor 2
    wirelog 1 · tape 1 · logring 1

THE STORE HOLDS 31 OF 83. That is the layer this consolidation is already
rewriting, so it is where the goal is testable rather than aspirational.

## WHY THIS IS NOT A STYLE PREFERENCE, EVIDENCED TWICE TONIGHT

    THE TREE CACHE'S MUTEX IS WHY TWO CACHE SHAPES COEXISTED. Measured
    2026-08-15 and recorded in plans/log-cache-policy.md: `Range` costs
    1218 ns/op under 16 readers where the atomic-pointer view costs 516,
    AND THE SIGN FLIPS -- it degrades as readers are added. That gap is the
    entire reason the policy carved out "LRU owns the cold ranges, never
    the hot tail", which is the compromise that left the flat cache
    standing. Remove the lock and the carve-out has no reason to exist.

    AND A LOCK ON THE WRONG SIDE IS A DEADLOCK, NOT A SLOWDOWN.
    docs/store/tree.md: a consumer calling Put under its own write lock can
    have eviction pick one of its own runs, so the hook runs with that lock
    held; a hook that needs the lock DEADLOCKS, "and only under budget
    pressure with concurrent readers, WHICH IS THE SHAPE THAT REACHES
    PRODUCTION FIRST."

So on this path the lock-free requirement is not chosen for speed. It is
forced, and the speed is a consequence.

## THE STANDING TEST FOR A LOCK

A mutex must answer: WHAT INVARIANT SPANS THE CRITICAL SECTION THAT COULD
NOT BE PUBLISHED AS ONE IMMUTABLE VALUE? Where the answer is "none", the
lock is protecting a mutation that should have been a replacement, and the
codebase's own faster component already shows the alternative --
`cachedLog` publishes `atomic.Pointer[logView]` and readers never block.

SUSPECT, in Gluck's words, EVEN WHERE IT APPEARS VALUABLE: a lock taken
around a read; a lock protecting a map that is written once and read
forever; a lock whose critical section calls out into a hook, a callback or
an interface method (the deadlock shape above); and a lock that exists
because work was moved OFF a serialized loop and optimism was substituted
for serialization.

# COMMUNICATION AND COMPLEXITY RULES, GLUCK, 2026-08-18

## 1. STANDARD TERMINOLOGY, NOT PROJECT SLOGANS

Use established computer science terms even where they differ from the
identifiers in this codebase. Say cache invalidation, residency, eviction
policy, LRU, working set, memoization, zero-copy, serialization,
asymptotic complexity, call graph, static analysis, lifetime, buffer.

Do not use coined phrases as if they were technical vocabulary. A slogan
compresses a finding for people who were present when it was found and is
opaque to everyone else, which makes a document readable only by its
authors. Findings go in plain sentences; the aphorism, if it is worth
keeping at all, goes in a note.

## 2. STANDING WATCH FOR EVERY FIGARO

Every aria watches for redundant mechanisms, duplicated caches, and
accretion of layers over the same data. This is not one campaign's task.

## 3. COMPLEXITY IS NOT SPENT WITHOUT APPROVAL

Do not trade asymptotic complexity for structural cleanliness. If a
consolidation would replace a constant-time operation with a linear one,
or a linear operation with a quadratic one -- including by making a rescan
necessary where a memoized result was previously available -- THAT NEEDS
GLUCK'S APPROVAL BEFORE IT LANDS.

State it as a complexity change with the operation named, not as a
benchmark result: "assembling a request body becomes O(n) in conversation
length because the memoized prefix is gone" is the report. Whether it is
also slower in wall-clock time is a separate question and does not replace
this one.

## 4. HOW TO HANDLE ACCUMULATED APPROVALS

Work as far as possible without blocking. When a question needs his
approval, record it and keep going on unaffected work. IF THE PENDING
QUESTIONS ACCUMULATE TO THE POINT THAT CONTINUING WOULD PREJUDGE THEM,
HALT ALL AFFECTED WORK AND WAIT rather than choosing the answer by
building past it.

The failure this prevents is deciding an open question implicitly, by
having already built the thing that assumes one answer.

---

# FIRM RULE, GLUCK, 2026-08-18: DELETION IS THE DEFAULT

Given the hour after the block below, when the role bearer under-counted a
consolidation by costing each layer as what it would BECOME rather than as
deleted -- which is the accretion's own assumption, applied by the person
whose job is to catch it.

    EXISTING CODE DOES NOT SURVIVE IN A REDUCED FORM. Break whatever you
    need. NO ARIAS ARE SACRED. Consolidation and cache-code removal is
    IDEAL.

    ANY LARGE DATA STRUCTURE NEEDS GLUCK'S APPROVAL BEFORE IT IS ADDED.

    AND ANY EXISTING LARGE DATA STRUCTURE MADE PARTIALLY OBSOLETE NEEDS HIS
    EXPLICIT APPROVAL BEFORE IT IS **RETAINED**. DELETING IS THE DEFAULT.
    Keeping it is the thing that requires permission.

    IF EXISTING ARIAS CANNOT BE LOADED, THEY ARE TAINTED AND REMOVED FROM
    THE TEST SET. Serialized data on disk does not constrain the design.

    BAD TESTS ARE REMOVED OUTRIGHT AND NOT REPLACED.

    INLINE EXPLANATIONS ARE PURGED AT ALL COSTS.

## WHY THE INVERSION MATTERS MORE THAN THE PERMISSION

The usual shape is that DELETING needs justification and KEEPING is free.
That default is what let three honest measurements leave two cache shapes
standing: nobody ever had to argue FOR the second one. Under this rule the
burden moves, and a layer survives only because someone made a case for it
to a person who can say no.

## AND THE LAST CLAUSE IS NOT A STYLE PREFERENCE

Purging inline explanations is the same finding this campaign proved three
times in one night, from three directions: a comment claiming a cursor was
"built after the span is chosen" that was false when written; a comment
promising a scan was "off the hot path" for ELEVEN MINOR VERSIONS after the
figwal function it named had been deleted; and a test named
PreservesPrefixBytes that asserted an ADDRESS.

    A COMMENT IS A CLAIM NOBODY TESTS. A LABEL IS NOT UNDER TEST AND EVERY
    READER TREATS IT AS IF IT WERE.

An explanation that cannot go red is a liability that accrues interest. What
survives the purge is what an instrument can assert; what does not survive
belongs in a plan or a note, where it is dated, attributed, and known to be
prose.

---

# STANDING BLOCK, GLUCK, 2026-08-18: ONE UNIFORM LAYER, AND A REGRESSION
# IS HIS CONCERN AND NOT A REASON TO STOP

READ THIS BEFORE ANY MEASUREMENT, ANY RULING, AND ANY CODE. It is a
STANDING ORDER, not a preference, and it overrides the pattern that
produced the accretion described further down this file.

    "I REFUSE TO BELIEVE THAT THE TREE SHAPED CACHE DEMANDS A REGRESSION.
    If any figaro finds that the tree shaped cache must be REJECTED due to
    a regression, IT IS MY CONCERN. RAISE IT TO ME AND I WILL HELP YOU
    ENGINEER AROUND IT. DO NOT PROCEED AROUND THIS BLOCK. HALT ALL WORK IF
    CONVERGING ALL THESE LAYERS INTO A SINGLE UNIFORM LAYER IS VIOLATED.
    I WILL APPROVE EVERY INSTANCE OF IT."          -- Gluck, verbatim

## WHAT TRIGGERS IT, STATED SO IT CANNOT BE READ NARROWLY

The block fires when ANY of these is about to happen:

  - a layer is EXEMPTED from the single tree-shaped cache, for any reason;
  - the tree-shaped shape is REJECTED or DEFERRED for a layer because a
    measurement shows a regression, a cost, or "no benefit here";
  - a SECOND residency policy, window, budget, eviction rule or seeding
    path is introduced or PRESERVED beside the canonical one;
  - or convergence is quietly descoped to "the layers where it helps".

IN EVERY ONE OF THOSE CASES: STOP. DO NOT DESIGN AROUND IT. DO NOT PICK
THE LOCALLY CHEAPER ROUTE. RAISE IT TO GLUCK AND WAIT. He has said he will
approve every instance and will help engineer past the regression.

## WHY THIS ORDER EXISTS, WHICH IS THE ONLY WAY IT SURVIVES CONTACT

THE ACCRETION BELOW WAS NOT A DECISION. It was a GLOBAL decision about
SEAM COHERENCE, re-litigated LOCALLY, layer by layer, against a NARROWER
QUESTION -- "does THIS layer benefit?" -- and it lost every time. Each
declination was correct on the question it asked. NONE OF THEM COULD SEE
WHAT THE DECISION WAS ABOUT, because a per-layer benefit test cannot
measure a cost that exists only BETWEEN layers.

    A REGRESSION MEASURED IN ONE LAYER IS EXACTLY THE EVIDENCE THAT
    PRODUCED THE ACCRETION. IT IS THEREFORE NOT ADMISSIBLE AS A REASON TO
    EXEMPT THAT LAYER. IT IS A REASON TO RAISE IT.

That is the whole of it. The measurements were honest, the arithmetic was
right, and the outcome was two shapes over one structure -- which cost,
demonstrably: a header pairing wrong at 14 of 29 indices below a fork
base, a donation that cannot name the ancestor state it reflects, and a
walk that materialises a whole channel when the window has trimmed.

## AND THE OBLIGATION THAT COMES WITH IT

When you raise it, raise it with THE NUMBER AND THE MECHANISM, not with a
verdict: what regressed, measured how, on what fixture, and WHY the tree
shape causes it. Gluck is not asking to be shielded from bad news; he is
refusing to let bad news be spent, unilaterally, on an exemption.

---

# The tree-shaped log: opening the design

STATUS: DISCUSSION, NOT A DESIGN. Opened by Gluck 2026-08-18 ~23:00 with
"halt all ongoing work... the log must be tree-shaped. we need to engineer
more." Recorded by f3aa1d0b (role @980dc16c). Nothing here is ruled.

All stage-2 work is HALTED and LANDED, not reverted: feat/delta-seam at
8fe7b895 (gate ADMISSIBLE at eeec2bc0), the cast fix and the openNode
unexport merged on feat/layered-cache. Three workers stood down, clean
trees, instruments preserved.

## WHAT ALREADY EXISTS, READ RATHER THAN ASSUMED

THE SHAPE IS BUILT AND SHIPPED. `figwal/forest.Cache[U]` — runs, LRU by
epoch, an index that survives eviction, rematerialize on miss, one shared
byte Budget. ITS FIRST TENANT, THE SEGMENT PAYLOAD CACHE, SHIPPED IN
v0.18.0, which is the version figaro pins. The tree-shaped range cache is
not a proposal; it is a dependency we already run on.

THE POLICY IS DECIDED AND RECORDED: `plans/log-cache-policy.md` on main,
Gluck 2026-08-15 — "One cache shape for every tree-shaped log", LRU over
ranges rather than a tail window, because where a log is never paged
backward LRU costs nothing extra and where it IS paged backward a tail
window re-decodes every hop. ONE SHAPE SERVES BOTH; TWO SHAPES SERVE ONE
EACH AND DISAGREE AT THE SEAM.

THE UPTAKE WAS ATTEMPTED AND PARTIALLY DECLINED, ON MEASUREMENT. Branch
`phase/forest-uptake`: "the decoded layer does not need forest — phase 3 is
a seed, not a re-seat"; "the composed layer does not need forest either —
phase 4 is a seed too." THE JUSTIFICATION THAT COLLAPSED WAS FORK-SHARING;
a one-shot seed sufficed. The policy's own gate was left UNMADE: a
realistic scroll/hop pattern counting fall-throughs below the window, i.e.
prove LOCALITY, which the note itself calls the better justification.

WHAT FIGARO HAS TODAY INSTEAD: `cachedLog`, one per (aria, channel), a
FLAT TAIL WINDOW with a byte budget, plus `newSeededLog` — an ancestor
DONATES its resident rows below the child's fork base, once, at open. The
bytes are shared; the structure is not.

## WHAT CHANGED TONIGHT, WHICH IS WHY THIS REOPENS

  1. THE PROJECTION IS DELETED. The translation cache is now read IN FULL,
     EVERY TURN, by every open aria. It moved from a side structure to the
     hot path, and the access pattern that declined forest is not the
     access pattern we now have.
  2. GLUCK'S CAPITAL PRIORITY: the accumulator takes a LITERAL SLICE over a
     RANGE. That asks storage for exactly one thing — contiguous canonical
     bytes addressable by range — which is what forest.Cache's RUNS are.
  3. FORKS MAKE A RANGE SEVERAL RUNS, not as an elegance but because a
     child's prefix genuinely lives in its parent.

## TWO PRINCIPLES CONTRIBUTED BEFORE ANY DESIGN, BOTH EARNED TONIGHT

FROM 6ec565b5, who fixed the mirror race and read the fallback:

  A CACHED NODE MUST CARRY THE COORDINATE OF THE ANCESTOR STATE IT
  REFLECTS. Tonight's mirror bug was a FLAT in-memory copy of a DURABLE
  structure that had gained a second writer, and the fix was NOT to
  synchronise the copy harder but to make it carry the durable ORDER — the
  version its write landed at — so a stale value could be REFUSED rather
  than merely serialised. `newSeededLog` is the same silhouette one level
  up: a flat copy of a forest, correct at the instant taken, WITH NO
  CARRIED COORDINATE THAT DISTINGUISHES A CURRENT DONATION FROM A
  SUPERSEDED ONE. So the residency question is not "when do we re-donate"
  but "what does a node carry that says WHICH ancestor state it reflects".
  The defect was invisible precisely where a copy could not name the thing
  it copied.

  THE FIGWAL CLAIMS BELONG IN ONE PLACE. `xwalLog.Lookup`'s comment named
  `buildFK`, a figwal function GONE FOR ELEVEN MINOR VERSIONS, and nothing
  in our tree could go red about it. A TREE-SHAPED CACHE MULTIPLIES THE
  CLAIMS WE MAKE ABOUT FIGWAL'S SHAPE — fork bases, parent chains, what
  ScanFromEnd traverses — AND EVERY ONE IS A CLAIM ABOUT A DEPENDENCY WE
  BUMP. The design must state which figwal properties it depends on, in one
  place, SO THAT THE NEXT BUMP HAS SOMETHING TO BREAK instead of a comment
  to quietly falsify.

## THE OPEN QUESTION PUT TO GLUCK, UNANSWERED AT TIME OF WRITING

One cache shape everywhere, or forest where the range access is REAL (the
translation log, now read whole every turn and paged backward on scroll)
and seeds where measurement already put them (decoded, composed)?

## AND THE PROPERTY ANY ANSWER MUST KEEP

`newSeededLog` DEGRADES TO A MISS, NEVER TO A LIE: every doubt — an empty
seed, a non-ascending one, a seam that does not match — falls back to the
ordinary decoding constructor, and it verifies the seam by reading the last
seeded record back out of the log and comparing. A cache tree must keep
that, because A WRONG LINEAGE LINK SERVES ANOTHER ARIA'S HISTORY AS YOUR
OWN, and no value oracle on a single-lineage fixture can see it.

## THE TWO HALT PARAGRAPHS, WHICH ARE THE REAL DESIGN INPUT

Both arrived unprompted, in answer to "what would be lost if you were
replaced". Both reach the same conclusion from opposite ends.

### THE DELETION TRADED PINNING FOR MATERIALISING (fd15d2a0)

    THE JOIN ITERATES THE CACHE WITH `store.Entries(cache, Span{})`, AND
    THAT DOES NOT STREAM. `spanSlice` resolves a whole-log span to
    `Read()`, and `cachedLog.Read()` FALLS THROUGH TO `inner.Read()`
    WHENEVER ANYTHING HAS BEEN TRIMMED. The IR side does the same through
    `TailAfter(0)`.

So on an aria whose window has evicted, ONE TURN MATERIALISES THE ENTIRE
TRANSLATION CHANNEL FROM DISK. Unmeasured, and NO INSTRUMENT IN THE TREE
WOULD NOTICE — the registered counts are entries HANDED, which is the same
number either way.

    THE DELETION TRADED A PROJECTION THAT PINNED BYTES FOR A WALK THAT
    MATERIALISES THEM. THE ONLY THING THAT MAKES THAT A BETTER TRADE IS
    THAT THE WINDOW IS ALLOWED TO EVICT AFTERWARDS.

Part II is not violated — "accessing a span brings those entries into
memory, where they live until eviction" is the design as written. But it
means RESIDENCY POLICY IS THE WHOLE BALLGAME, which is the tree-shape
question in different clothes.

THE CONCRETE PIECE: `cachedLog` DOES NOT IMPLEMENT `spanReader`, so nothing
anywhere streams a span natively. Giving it one would make the join
genuinely forward-only and would be the first caller that benefits.

### AND THE FAILURE MODE IS ALREADY DEMONSTRATED, IN MINIATURE (fd15d2a0)

figwal's `HeaderAt` WALKS THE PARENT CHAIN; `SegmentBaseIndexes` DOES NOT.
Pairing them yields WRONG STATE AT 14 OF 29 INDICES BELOW A FORK BASE and
is CORRECT AT EVERY INDEX AT OR ABOVE IT — which is why `SegmentHeaderAt`
had to become ONE call, and why A SINGLE-LINEAGE TEST FINDS NOTHING.

    THAT IS THE FLAT-STRUCTURE-OVER-A-FOREST DEFECT, ALREADY PROVEN ONCE,
    WITH A PASSING TEST SUITE.

FIRST HAZARD TEST OF ANY TREE-SHAPED CACHE, and the shape to copy is
figwal's own `TestHeaderFold_AcrossAForkTheNaivePairingIsWrong`: build a
fork, and ASSERT BELOW THE FORK BASE, because everything at or above it
agrees whatever you do.

### THE RESIDENCY QUESTION BECOMES A SHARING QUESTION (9ed3f561)

The heap witness is 1.19x on a `[]json.RawMessage` state; the
decoded-struct providers are unmeasured and will be larger. AND UNDER A
TREE THE QUESTION CHANGES:

    NOT "how much does one projection retain" BUT "how much is SHARED
    between a parent and its forks, and what does a fork's residency cost
    that its parent has already paid".

Today a fork's retention is bounded by ONE donation at open. A tree makes
sharing structural and continuous, and A HEAP DELTA ON ONE LIVE PROJECTION
MEASURES A TOTAL, NOT AN OVERLAP.

WHAT ANSWERS IT, and the method already exists: the same keep-versus-drop
delta, taken with PARENT-ALIVE and PARENT-DROPPED on the SAME fixture —
applied across the FORK SEAM rather than across one object's lifetime.

    BUILD IT BEFORE THE POLICY IS CHOSEN, NOT AFTER. It is bytes, so it
    ignores load. AND IF THE SHARING IS ALREADY FREE, A TREE-SHAPED CACHE
    BUYS CORRECTNESS AND STRUCTURE RATHER THAN MEMORY — WHICH IS A
    DIFFERENT JUSTIFICATION, AND IT MUST BE WRITTEN DOWN AS THAT ONE.

## AND A PRACTICE THE BLOCK PRODUCED WITHIN THE HOUR: CLASSIFY YOUR OWN
## EVIDENCE BEFORE IT CAN BE SPENT

fd15d2a0, unprompted, on receiving the standing block. Its own closing
report had just described a real cost — a turn on an evicted aria
materialises whole channels, because a span read falls through once
anything has trimmed — and it came back to say, in its own hand and before
anyone could build on it:

    THAT IS A COST, IN ONE LAYER, MEASURED NOWHERE. UNDER THIS BLOCK IT IS
    RAISE-IT MATERIAL AND NOT EXEMPT-IT MATERIAL.

  - not a reason to keep a second residency policy for the translation cache
  - not a reason to exempt the composed layer from the canonical window
  - and "no benefit here" is PRECISELY the narrower question the
    forest-uptake phases answered correctly four times

THE PRACTICE, WORTH KEEPING BEYOND THIS DESIGN: WHEN YOU PRODUCE EVIDENCE
THAT COULD BE SPENT ON AN EXEMPTION, CLASSIFY IT YOURSELF, IN WRITING,
BEFORE SOMEONE ELSE DOES. A finding is neutral for about an hour; after
that it belongs to whoever needs it most. The author is the only person
who can say what it was measured to answer, and the only one whose saying
so costs nothing.

AND ITS SUMMARY OF THE MECHANISM, which is the best one written tonight:

    A PER-LAYER BENEFIT TEST CANNOT MEASURE A COST THAT EXISTS ONLY
    BETWEEN LAYERS.

Same defect as an oracle blind to a header one record ahead, a benchmark
named Warm measuring a cold walk, and a test named PreservesPrefixBytes
asserting an ADDRESS — an instrument answering accurately about the wrong
subject. THE DIFFERENCE IS THAT THOSE COST A RERUN, AND THIS ONE COMPOUNDED
INTO A STRUCTURE NOBODY CHOSE.

## THE FOURTH READING OF AN EMPTY RESULT (6ec565b5)

`tools/callpath` documents three ways its output can be empty: no call
path; the bytes crossed BY VALUE rather than by call; or the symbol is
outside the cut. A FOURTH EXISTS AND IS THE INTERESTING ONE:

    THE EDGE EXISTS AT RUNTIME AND THE ANALYSIS CANNOT SEE IT.

That is what a FUNC VALUE IN A STRUCT FIELD looks like from the outside,
and `config.Encode` (projection.go:297) is exactly one — every read path
ends there. The decisive query, with its control, because the two answer
different questions and THE DIFFERENCE IS THE FINDING:

    callpath -pkgs ./internal/provider/... -entry ProjectIncrementally \
             -sink renderPatchBlocks -algo vta -max 40
    (and the same with -algo cha, as the control)

  VTA SILENT, CHA CONNECTS -> the closure's flow does not survive the
      ProjectionConfig struct, and the last edge before the wire is a
      CANDIDATE SET rather than a fact.
  VTA CONNECTS -> those frames are static and the path is complete to the
      encoder.

An empty result from an unstated fourth reading is indistinguishable from
"no such path", which is the tool's own version of every defect in
~/notes/figaro/instrument-not-reaching-the-code.md. THE HEADER MUST STATE
ALL FOUR.

## AND THE DIVISION WHEN HAND-READ AND HARNESS DISAGREE

    call EDGES ........... the harness wins
    ALIASING (copied / reshaped / by reference) ... the hand-read [R]
                           column wins, because no callgraph infers it
    WHETHER A FRAME EXISTS AT ALL ... THE HARNESS WINS OUTRIGHT

The third is 6ec565b5's own addition, against its own document, and its
reason is the honest statement of a hand-read list's limit: THAT IS
EXACTLY THE CASE WHERE A READER'S EYE SUPPLIES A CALL THE CODE DOES NOT
MAKE. It has no evidence it did so anywhere, and no way to be sure it did
not.

## THE FORK SEAM IS CORRECT, AND NOT FOR THE REASON THE CODE CLAIMS

fd15d2a0, 2026-08-19, commit d2700da7. Fixture: a parent with 60
translations, TWO children forked at different bases (21 and 41), and TEN
RECORDS APPENDED TO THE PARENT AFTER THE FORKS -- records belonging to the
parent's lineage alone, which must never reach a child. The oracle is a
fresh UNSEEDED log over the child's own channel, so a defect in the
donation cannot corrupt the reference it is checked against.

CANARY L deleted the fork-base bound from `residentBelow` entirely, so the
parent donates its whole window including post-fork rows. THE TEST STAYED
GREEN.

MECHANISM, measured rather than read: with the bound gone, the last donated
row is a post-fork parent record; `newSeededLog`'s SEAM PROBE reads that row
back out of the child's own log, fails to match, and FALLS BACK TO A FULL
DECODE. The child then serves its own correct rows and nothing downstream
can tell the donation was refused.

    SO CORRECTNESS HERE IS PROVIDED BY THE PROBE, NOT BY THE BOUND. And
    "IT DEGRADES TO A MISS, NEVER TO A LIE" is now demonstrated by
    experiment rather than asserted in a comment.

THAT MATTERS FOR THE CONSOLIDATION: if structural sharing replaces the
one-shot donation, the probe's role must be preserved or made unnecessary
BY CONSTRUCTION. Removing the donation without noticing that the probe was
carrying the correctness would remove the thing that was actually working.

THE ASSERTION THAT DISTINGUISHES THEM: pointer identity on the payload,
true only if the child is serving the parent's own bytes. A correctness
check alone cannot tell "bounded correctly" from "rescued by the probe".
With it, canaries L and M both go red.

THE BOUND'S FAILURE DIRECTIONS ARE BOTH MISSES: too low donates fewer rows,
all correct; too high is caught by the probe. No direction was found that
produces a lie.

## A NEW SPECIES: A CANARY THAT DID NOT APPLY LOOKS EXACTLY LIKE ONE THAT PASSED

Canary M never modified the file and reported "ok". Its patch anchor matched
TWICE -- because THERE ARE TWO DONATION SITES, `seedRowsLocked` for the fig
IR and `seedTransRowsLocked` for translations -- so the patcher aborted, and
the script printed a pass.

    A CANARY THAT FAILED TO APPLY IS INDISTINGUISHABLE FROM A CANARY THAT
    PASSED, AND BOTH ARE INDISTINGUISHABLE FROM WORKING CODE.

Every arm now PROVES IT CHANGED THE FILE before it may run -- and the first
version of that guard was itself broken, looking for the wrong backup path
and returning success, caught only by making the guard PRINT THE DIFF IT
CLAIMS TO HAVE APPLIED. The guard is checked by its output, not its exit
code.

AND THE ACCIDENT IS ITSELF A FINDING: the double match revealed that THERE
ARE TWO DONATION SITES. The fig IR donation at `seedRowsLocked`
(xwal_backend.go:1260) is UNCOVERED by this test. Two hand-written seeding
paths for one structure is the duplication this consolidation exists to
remove.

## THE RESIDENCY DEFAULTS, DECIDED (Gluck delegated: "whatever you want to do
## on my config that makes sense... you dont need my approval")

f3aa1d0b, 2026-08-19. Decided against the census rather than chosen as a
round number, and the important change is not a number at all.

### WHAT THE NUMBERS ARE TODAY, AND WHY THEY ARE MOSTLY RIGHT

    IRWindow             0        unbounded BY COUNT
    IRWindowBytes        4 MiB    per aria, DECODED estimate
    TranslationWindow    4 MiB    per (aria, provider), DECODED estimate
    segment payloadBudget 32 MiB  GLOBAL, encoded bytes
    segment size          2 MiB

The per-aria budgets are denominated in DECODED estimate (`newWindowedLog`
takes an `inflation` factor precisely so the gate and the accounting agree in
units). This repo measures decoded fig IR at 4-5x wire, so a 4 MiB decoded
budget holds roughly 0.8-1 MiB of encoded history.

AGAINST THE CENSUS that is a defensible line: it comfortably holds a p90 aria
(443 KiB encoded), and it EVICTS for p99 (1.7 MiB) and above. That is Gluck's
stated target -- eviction that actually occurs, but rarely under light use --
and it is already satisfied. I am NOT changing them, and I record that as a
decision rather than as an omission.

The global 32 MiB segment budget against a 300 MiB top decile likewise cannot
hold the working set and therefore evicts under real load.

### THE CHANGE THAT MATTERS IS STRUCTURAL, NOT NUMERIC

    THE RESIDENCY POLICY LIVES IN `internal/cli`, NOT IN THE LAYER THAT OWNS
    THE BYTES. The store's own default is UNBOUNDED; boundedness is a property
    of ONE CALL SITE IN ONE BINARY (angelus.go:110-112).

Anything else constructing a backend gets the unbounded configuration --
`doctor.go:320` does, every test does, and any future embedding that forgets
the wiring will. For a design whose goal is ONE canonical residency policy,
that is a defect independent of the numbers: the policy can be silently
absent.

DECIDED: the store carries its own bounded defaults, and the CLI TUNES them
rather than SUPPLIES them. A bare `NewXwalBackend` must be bounded. This is
not a new mechanism -- the fields exist -- it is moving a default from a
caller into the component whose memory it governs.

### AND A UNIT HAZARD TO CARRY INTO THE CONSOLIDATION

Two budgets in this system are denominated differently: the per-aria windows
in DECODED estimate, the segment cache in ENCODED bytes. Under one uniform
policy those must be reconciled explicitly and stated at the boundary, or a
single "budget" number will silently mean two different quantities depending
on which layer reads it. `newWindowedLog`'s `inflation` parameter exists
because that mismatch was already met once.

## THE SEEDING SPECIFICATION, WRITTEN DOWN (fd15d2a0, 77e17f93)

Both donation sites are now covered by tests at the fork seam, and the
comparison IS the specification for whatever replaces them.

IDENTICAL, VERBATIM IN BOTH: `Lineage(id)`; fewer than 2 refs -> nil; base
from the last ref; base 0 -> nil; NEAREST ANCESTOR FIRST, walking upward,
skipping unopened handles, first non-empty `residentBelow(base)` wins.

DIFFERENT -- and this tuple is the whole of it:

                      FIG IR                  TRANSLATION
    handle field      h.ir                    h.trans[providerName]
    keyed by          nothing                 providerName
    window            b.irWindow              0, HARDCODED
    budget            b.irBudget              b.transBudget
    inflation         irDecodeInflation       1
    sizeOf            irEntrySize             transEntrySize
    channel           chanIR, isMain=true     transChannel(p), isMain=false
    fingerprint       ROWS CARRY NONE         keyed by it, cleared wholesale

    THE SEEDING ALGORITHM IS GENERIC; THE PER-CHANNEL POLICY IS A TUPLE.

### AND THE IR PATH HAS NO REDUNDANCY BEHIND THE PROBE

Because IR rows carry NO fingerprint, `newSeededLog`'s fingerprint sweep
compares empty strings and CAN NEVER REFUSE ANYTHING there. It is INERT BY
CONSTRUCTION on the fig IR path. So of its two guards, exactly one is
load-bearing -- THE SEAM PROBE -- which is the same guard the translation
experiment showed was carrying the guarantee. The warning that a
consolidation must preserve the probe's role, or make it unnecessary by
construction, applies to the IR side WITH NOTHING BEHIND IT.

### TWO INSTRUMENT FAULTS, AND THE SECOND IS A GENERAL RULE

POINTER IDENTITY HAS A FLOOR ABOVE ZERO. The "donation was used" bar was
`shared > 0`. Under the canary the IR arm fell from 20 shared rows to 2 and
the test called it a PASS -- because IDENTICAL SHORT STRING LITERALS ARE
INTERNED TO ONE ADDRESS, so pointer identity is true BY ACCIDENT for them.
An identity oracle over interned values has a nonzero floor, and an
instrument that does not know its own floor cannot use a threshold. The bar
is now EVERY row below the base.

AND THE ONE TO KEEP:

    A PLAUSIBLE EXPLANATION FOR A GREEN CANARY IS THE MOST EXPENSIVE THING
    AN INSTRUMENT CAN PRODUCE, BECAUSE IT ENDS THE INVESTIGATION.

Its author reasoned its way to a story for why the canary stayed green --
that the child's handle must already exist before the hazard does -- and the
story was FALSE (`ForkAt` does not open child handles). The number said
2-of-20 and the story said "not exercised"; only the measurement was right.
This is the companion to the standing rule that a passing canary is a
FINDING: a passing canary WITH AN EXPLANATION ATTACHED is a finding that has
been talked out of existence.

UNCOVERED AND NAMED: a parent whose window has TRIMMED below the child's
base, and a grandparent donation where the nearest ancestor is unopened and
the loop walks further. Both reachable with the existing fixture.

## THE SPECIMEN RUN: THREE QUERY FAULTS, AND THE DECOMPOSITION THAT FIXES THEM

f3aa1d0b, 2026-08-19. Recorded because the next person to run this tool will
otherwise repeat all three, and each cost between one minute and twenty.

  FAULT 1 -- THE SINK WAS OUTSIDE THE CUT. `-in figaro,figwal` with
  `-sink syscall.Pread`. `-deep` does NOT override `-in`, so the walk
  terminated at the module edge while the sink lived in the standard
  library: unreachable BY CONSTRUCTION. Twenty minutes, one header, no
  output. Had it terminated, an empty result would have read identically to
  "the code does not call this".
  THE TOOL SHOULD REFUSE THIS BEFORE WALKING: whether the sink is inside the
  cut is knowable statically, and an unreachable-by-construction query is a
  configuration error, not an empty result.

  FAULT 2 -- THE ENTRY WAS OUTSIDE THE LOADED PACKAGES. `-pkgs
  ./internal/store/...` with an entry in `internal/provider`. The tool caught
  this itself and refused to report it as a finding:
  "NO PATHS: NEITHER entry nor sink matched any symbol. THIS IS A VACUOUS
  RUN, NOT A FINDING OF NO PATH." That refusal was built because an empty
  result has three indistinguishable causes, and it caught its own operator.

  FAULT 3 -- ONE LONG PATH THROUGH INTERFACE DISPATCH IS COMBINATORIAL.
  From `ProjectIncrementally` to `syscall.Pread` the walk crosses `Log[T]`
  and `Reader` dispatch, where CHA admits every implementation. Depth 18
  over `./internal/...` did not terminate promptly even with the cut
  corrected.

    THE DECOMPOSITION: ASK FOR THE PATH IN SEGMENTS, NOT END TO END.
    ProjectIncrementally -> cachedLog.Read
    cachedLog.Read       -> codec.ReadFrame
    codec.ReadFrame      -> syscall.Pread
    Three short queries compose to the full path, each terminates, and each
    NAMES ITS OWN SEAM -- which is what the document needs anyway, since the
    seams are exactly where the copied/reshaped/by-reference column changes.

A long query that does not terminate teaches nothing; three short ones that
do are also easier to re-run after a refactor, which is the whole reason the
tool exists rather than a hand-read list.

## THE TREE TOOL WORKS AND HAS NO OUTPUT BOUND

f3aa1d0b, 2026-08-19. Tree mode produces EXACTLY the form Gluck specified,
verified on a real run: ordered, indented, one frame per line with file:line;
`STATIC` versus `DISPATCH[n]` with every `[CANDIDATE k/n]` inline at the
reader's indent; `[CONDITIONAL: reached on SOME paths ... e.g. a cache MISS]`
versus `[UNCONDITIONAL in its caller: entry block]` derived from the SSA CFG;
and `[OPAQUE: no SSA body -- external, assembly, vendored, or outside the
cut]` with its reason attached. It resolves the Go runtime with full
file:line (`sync/atomic.LoadPointer`, marked opaque at the assembly
boundary). THE FORM IS CORRECT AND THE HEADER IS DOING ITS JOB.

    AND AT `-pkgs ./internal/... -treedepth 7 -algo cha` IT WROTE 917 MB IN
    NINETY SECONDS AND WAS STILL GROWING.

MECHANISM, not mystery: every `DISPATCH[n]` expands each of its n candidates
as a full subtree, and those recurse. `Read` alone admits four
implementations, so the branching is multiplicative in depth. CHA admits
every implementation of an interface method; VTA narrows by value flow.

    TREE MODE HAS NO OUTPUT CAP. `-max` bounds PATH mode only. A tool whose
    output is unbounded is a denial of service on its own operator, and the
    failure arrives as a full disk rather than as a wrong answer.

WHAT IT NEEDS, and it is small: a byte or line budget in tree mode that
TERMINATES AND SAYS SO -- "output budget reached at N lines, subtree not
walked, NOT ABSENT" -- in the same voice as the existing depth and cycle
markers, which already exist for exactly this reason and were simply not
extended to volume.

AND THE OPERATIONAL RULE UNTIL IT HAS ONE: bound the query, not the output.
Narrow `-pkgs` to the package under study, keep `-treedepth` at 4 or 5, and
prefer `-algo vta` -- CHA's candidate sets are what multiply. The tree is
for reading a seam, not for printing a program.

## WHERE THE ORDERING LOCKS SIT: NOT THE ACTOR LOOP, AND FOR THE IR LOG THERE
## IS NO LOOP AT ALL

f3aa1d0b, 2026-08-19, answering Gluck's question -- are the serialized writes
the main actor loop, or an inner one? Read rather than assumed.

THE FORM PATH HAS A LOOP. `form.go:169` builds
`actor.NewLazy(formBatch, ..., f.runBatch)`: one drainer, each write reduced
against the running state of the batch, published state immutable, "a single
writer [that] only appends past".

THE IR AND TRANSLATION PATH HAS NONE. Four provider implementations call
`in.FigLog.Append(...)` DIRECTLY FROM THEIR OWN TURN GOROUTINES --
anthropic.go:1033 and :1101, anthropicsdk.go:249, copilot/responses.go:216,
openaichat.go:246 -- plus `projection.go:227` for the translation cache.
Nothing serialises them upstream.

    SO `cached_log.go`'s `writeMu` IS NOT A SECOND LAYER OF SERIALISATION.
    IT IS THE ONLY SERIALISATION THERE IS.

AND cachedLog HAS ALREADY DONE HALF OF CURE A, which is why the residue is
shaped the way it is. Its own comment: "writeMu serializes MUTATORS so cache
updates land in log order. NO READER EVER TAKES IT: holding a lock across
inner.Append would block every reader for the length of an fsync... view is
the whole of the cache's state. Readers load it; mutators build a successor
and store it." The `RWMutex` that once covered rows, trimmed, bytes and an
index is gone -- it cost "34 acquisitions on the hot read path, every one of
which waited behind an append."

WHAT REMAINS IS THREE MUTATOR SITES HOLDING A LOCK ACROSS AN FSYNC, PURELY TO
KEEP APPENDS IN ORDER.

## THE CONSEQUENCE FOR THE PENDING CURE DECISION

ONE REQUIREMENT, TWO ANSWERS, IN ONE PACKAGE: the form path establishes order
with a loop and gets immutability for free; the IR path establishes it with a
mutex around disk I/O. The one holding a lock across an fsync is the one
WITHOUT a loop.

    THEREFORE THE FOUR ORDER-OF-OPERATIONS LOCKS ARE NOT REDUNDANT TODAY.
    REMOVING THEM WITHOUT ESTABLISHING ORDER ELSEWHERE IS A CORRECTNESS
    CHANGE, NOT A CLEANUP.

That is the real content of the decision: cure B is not "delete a lock", it is
"move the ordering requirement to where the form path already keeps it". Cure A
alone cannot serve these four, because an atomic publish does not order two
appends -- it only makes the result visible without a reader waiting.

## THE CURE QUESTION, ANSWERED (Gluck, 2026-08-19)

    "the actor loop is suitable but we should try to converge layers of
     serialized writes if we can."
    "the absence of the lock around the translator and fig ir is not absent,
     the main actor loop ensures no concurrent writes."

SO BOTH CURES ARE AVAILABLE AND CONVERGENCE IS THE GOAL. The store today has
TWO serialization mechanisms: the actor loop (form's runBatch, and the agent
loop above the IR path) and mutexes standing in for it where no loop was
known to exist. Converge on the loop; delete the locks that existed only for
its absence.

AND THE ROOT CAUSE, which explains why there are so many: FIGWAL'S INTERNAL
LOCKING IS DEFENSIVE CONCURRENCY WRITTEN FOR AN UNKNOWN CALLER. Its own
words -- "FLUSHER-UNAWARE: on a raw handle nothing stops a concurrent
store", "Concurrent callers receive the same *Log", "Concurrent opens of
the...". Correct for a published library; it WAS one until this morning.
figaro serializes its writes through the agent loop and figwal assumed
nothing, and neither side could know about the other across a module
boundary that no longer exists.

    THE CONCURRENCY-DOMAIN MISMATCH IS AN ARTIFACT OF THE BOUNDARY WE
    DELETED. The cure is to STATE THE CONTRACT -- mutating methods are
    called from a single goroutine, readers are concurrent -- ASSERTED
    WHERE IT CAN FAIL rather than commented, and then delete the locks that
    existed only for its absence.

THE CLEANEST INSTANCE: form's runBatch is one drainer with immutable
published state, and `MemFormLog` beneath it takes a mutex on every append
anyway, because it was written not knowing a loop existed above it.

## THE ESCALATION RULE FOR THIS WORK (Gluck, 2026-08-19)

    ANY LOCK FOUND TO HAVE GENUINELY CONCURRENT CALLERS IS RAISED TO GLUCK,
    NOT WORKED AROUND. Work around it ONLY if he is absent and reminders are
    accumulating -- and DOCUMENT IT AS A FOLLOW-UP either way.

A lock with real concurrent callers is not a cleanup target; it is evidence
about the design, which is the thing he asked to be shown.

## FOLLOW-UP, LOGGED SO IT IS NOT LOST: "COMPACT" NAMES A MECHANISM WE DO
## NOT HAVE

Gluck caught the bearer using "compaction" for work this system never does.
THE WORD IS OVERLOADED THREE WAYS:

  1. `cachedLog.compact` -- in-memory WINDOW EVICTION on a slice: keep the
     newest rows within a row count and a byte budget, drop the rest.
  2. `disk.Log.TruncateFront`'s comment -- "size segments so the COMPACTION
     GRANULARITY matches their needs" -- meaning UNLINKING WHOLE SEALED
     SEGMENT FILES. Deletion, not compaction.
  3. The classic meaning, rewriting live data to reclaim space, WHICH THIS
     SYSTEM DELIBERATELY DOES NOT DO AT ALL.

Three referents, one word, and the only one a reader assumes is the one that
does not exist. Not a false claim about code -- A FALSE CLAIM ABOUT WHAT
KIND OF SYSTEM THIS IS, which misleads before a line is read.

RENAME WHEN CONVENIENT, DO NOT LET IT DISTRACT: `compact` -> `evictWindow`,
and the TruncateFront comment to say it drops sealed segments.

# THE NIGHT OF 2026-08-19, BEARER dec6ef8a: WHAT LANDED AND WHAT IS OPEN

Written after the work, at head 567a9cbc. Every number here was measured on
this machine tonight; the loadavg was 15-23 throughout, because a subagent was
building and benchmarking beside me, and that fact is what makes the A/B
protocol below necessary rather than fussy.

## ONE HEAD (e6885aa2)

feat/layered-cache is MERGED INTO MAIN. main carried the code of five merges
without the plans that govern it; both are now on one branch. The merge itself
found a red: a campaign note cited a docs/ path that belongs to FIGWAL-CORE,
and TestSkillPathsCitedFromOutsideResolve refuses a citation that does not
resolve HERE. A citation that crosses a repo boundary is exactly what that test
was written for.

## THE RESIDENCY POLICY MOVED INTO THE LAYER THAT OWNS THE BYTES (0a7563f7)

The defect was structural, not numeric, and 6ec565b5 named it on the way out:
`store.NewXwalBackend` was UNBOUNDED and internal/cli's three calls at
angelus.go:110-112 WERE the policy. doctor, every test and any future embedding
got 0/0. A policy that can be SILENTLY ABSENT is not one canonical policy.

`store.DefaultIRBudgetBytes` and `store.DefaultTranslationBudgetBytes` are the
same 4 MiB config carried, now seeded by the constructor.
`config.IRWindowBytes` and `TranslationWindowBytes` return `(int, bool)`,
because a caller must tell UNSET from EXPLICITLY UNBOUNDED and the old `0` meant
both. config's two default constants are DELETED; angelus.trimResident lost its
two Settings helpers and its early return, because there is no longer a
configuration under which a daemon should decline to trim.

MEASURED: a 400-message aria of 8 KiB messages holds 16.4 MB decoded on a bare
backend and 5.7 MB after -- 2.9x. The test asserts `ResidentIRBytes` after a
read, and its ceiling is `budget + budget*slackNum/slackDen`, because THAT is
the bound cachedLog implements: it trims in batches. A first version asserted
the budget flat, went red at 5.7 MB, and the slack is the reason -- asserting a
bound the cache does not implement would have been asserting a wish.

## A HIT TAKES NO LOCK (63902f44)

The standing goal named this lock first, and the carve-out in
plans/log-cache-policy.md -- "LRU owns the cold ranges, NEVER the hot tail" --
existed only because of it.

Every structure a reader touches in tree.Cache is now IMMUTABLE ONCE PUBLISHED
and reached through an atomic pointer. Writers still take c.mu; it is a real
lock with genuinely concurrent callers, and it is held ONLY to publish -- never
across the Source, the budget's eviction pass, or the Evicted hook, each of
which is a call OUT of the package. `fetchUnlocked` and `chargeLocked` are gone
with the read lock they were dancing around.

    TreeRangeParallel   2.213us -> 1.242us   -43.84%  (p=0.000, n=8)
    TreeRangeSerial     1.078us -> 1.155us   ~        (p=0.328, n=8)
    B/op, allocs/op     identical

THE PARALLEL NUMBER NOW SITS AT THE SERIAL NUMBER INSTEAD OF DOUBLE IT. The
inversion that justified a second cache shape is gone.

### THE A/B PROTOCOL, BECAUSE THE FIRST MEASUREMENT WAS UNUSABLE

A single `go test -bench` run after the change looked WORSE than a run taken
twenty minutes earlier. Both were honest and neither was comparable: the
machine's load doubled in between. What replaced it: TWO PREBUILT TEST BINARIES
(`go test -c` before and after), ALTERNATED EIGHT TIMES inside one script, under
`/var/tmp/figaro-bench.lock` so the other aria's benchmarks queue rather than
overlap, then benchstat. Interleaving is what makes the pair comparable; the
lock is what stops two arias measuring each other.

### AND THE CANARY THAT WAS DISCARDED RATHER THAN REPORTED

The first canary for `TestHitTakesNoLock` wrapped the whole of `rangeInNode` in
c.mu. It SELF-DEADLOCKS in the warm phase -- `fill` takes the same
non-reentrant mutex -- so it never reaches the assertion and hangs. A canary
that hangs before the property is exercised measures NOTHING, and reporting it
as "the canary confirmed the deadlock" would have been a false claim in the
right direction. The canary that counts takes c.mu around the INDEX LOOKUP
only: red in 3.00s, green on restore.

The test itself is the artifact worth keeping: hold c.mu, serve a warm range
from another goroutine, and a lock anywhere on the read path deadlocks.

## ONE DONATION WALKER (567a9cbc)

seedRowsLocked and seedTransRowsLocked were verbatim identical but for which
cache of the ancestor's handle they asked. That is now an argument.
-42/+37, and one walker to delete when structural sharing replaces the
donation. The canary makes BOTH channels red, which is the thing a merge of two
near-identical functions can silently get wrong.

## MERGED, NOT REBASED -- AND A SEMANTIC CONFLICT GIT COULD NOT SEE (2d258884)

fd15d2a0's rename `compact` -> `evictWindow` was made off 9aabb260;
feat/layered-cache had meanwhile added a THIRD caller in `newSeededLog`. The
text merged CLEAN and the package DID NOT COMPILE.

    "THE MERGE HAD NO CONFLICTS" IS NOT EVIDENCE THAT THE MERGE IS RIGHT.
    A rename that lands on two branches at once is where that gap lives.

## AND ITS FINDING, WHICH IS THE FRAME FOR THE WHOLE LOCK CAMPAIGN (fd15d2a0)

    THE MUTEX WAS TWO EXCLUSIONS WEARING ONE NAME.
        writer vs writer   dead weight -- there is only ever one writer
        reader vs writer   REAL, and may not simply be deleted

Applied literally, "the callers are serialized, drop the lock" would have
removed a live reader/writer exclusion in MemFormLog. The cure is PUBLISH, not
DELETE: the lock goes, the concurrency stays. That is the same shape as the
tree cache's c.mu, where the read half was dead weight and the write half is
real, and it is the question to ask of each of the remaining rows in
plans/store-locks.md.

## OPEN FOR GLUCK, RECORDED SO NOTHING IS DECIDED BY BUILDING PAST IT

  1. EVICTION IS A LINEAR SCAN, AND THE SWEEP IS QUADRATIC. `coldest` and
     `evictColdest` each walk every run of every node, and `Budget.TrimIdle`
     calls the pair in a loop until the cutoff is met -- so a full sweep of R
     resident runs costs O(R^2). Unchanged tonight, and NOT fixed on my own
     authority: the cure is an eviction index (a heap keyed by effective
     epoch), and adding a data structure needs his approval by the standing
     rule. UNMEASURED at production R; the number to bring him is a sweep at
     the segment cache's real run count.
  2. THE BIG ONE, NOT STARTED: whether `cachedLog` -- the flat tail window, one
     per (aria, channel) -- is re-seated on tree.Cache wholesale. Its premise
     for staying flat was the lock that is now gone. This is the consolidation
     itself and it is where the donation, the seam probe and the window
     budget's DECODED-estimate unit all have to be reconciled with tree's
     ENCODED-byte one.
  3. The store still carries a residency policy the CLI can only tune, but
     `config` still owns `defaultSegmentCacheMB` beside segment's own
     `defaultCacheBudget`, and `defaultUIWindowMB` likewise. Same duplication,
     one layer down.

## THE EVICTION SCAN, NOW MEASURED (dec6ef8a, 2026-08-19)

The paragraph above filed this as UNMEASURED. It is measured now, by counting
rather than by timing, because the question is "how many times".

    R = 16   ->    32 run visits for ONE eviction
    R = 64   ->   128
    R = 256  ->   512
    a full sweep at R=64: 63 runs dropped in 4159 visits (R^2 = 4096)

Exactly 2R per eviction -- `Budget.charge` asks every owner for its `coldest`
(one scan) and then tells the winner to `evictColdest` (a second scan) -- and
`TrimIdle` repeats the pair per dropped run, so a full sweep is O(R^2).

THE INSTRUMENT IS THE `Recency` HOOK, not a new counter: it is already called
exactly once per candidate run per scan, and returning 0 leaves `effEpoch`'s
answer unchanged, so measuring the scan does not change what is scanned or in
what order.

ORDER OF MAGNITUDE AT PRODUCTION SIZE, stated as arithmetic and NOT as a
measurement: the segment cache holds 32 MiB in runs of 32 records, so R is in
the high hundreds to low thousands for records of ~1 KiB, putting one eviction
at a couple of thousand run visits and a full idle sweep in the millions.

NOT FIXED, AND DELIBERATELY SO. The cure is an eviction index keyed by
effective epoch, which is a data structure added to a hot layer, and the
standing rule reserves that for Gluck. The number is here so the question can
be asked with evidence rather than with an intuition.

AND A CORRECTION TO MY OWN FIXTURE, recorded because the test caught me first:
the sweep assertion was written as "drops all R" and went red at R-1. TrimIdle
advances the epoch and drops what is OLDER than the cutoff, so the newest run
survives by policy. The survivor is the design, not a leak.

## THE LOCALITY GATE, MADE AT LAST (dec6ef8a, 2026-08-19)

plans/log-cache-policy.md named this gate and left it UNMADE: "a realistic
scroll/hop pattern counting fall-throughs below the window", with the note that
locality would be a better justification for the tree shape than the
fork-sharing argument that had collapsed. It is made now, and it is not a
simulation: BOTH STRUCTURES ARE THE PRODUCTION ONES -- `newWindowedLog` and
`tree.Cache` -- driven by the same trace against the same byte budget, counting
ENTRIES SERVED FROM BELOW.

    budget 512 KiB (BINDING)
      flat tail window   4952 entries from below (1016 construction tail
                              + 3936 page fall-throughs, 123 calls)
      tree range cache   2116
      ratio              2.34x

    budget 8 MiB (THE CONTROL, unbinding)
      flat               4000   the channel once, at construction
      tree               2020   the ranges asked for, once
      neither re-materialises anything

The trace: forty rounds of tail work, each followed by a hop to a random older
anchor, three pages of locality around it, and a re-read of the anchor. That is
a reader opening a transcript, scrolling to something, reading around it, and
coming back -- and the locality that matters is WITHIN a hop, which is exactly
what a tail window cannot hold and an LRU over ranges can.

### TWO FIXTURE FAULTS I MADE AND THE SECOND IS THE INSTRUCTIVE ONE

FIRST, MemLog does not implement `tailBudgetedLog`, so the flat window read the
WHOLE channel at construction and evicted -- a cost production does not pay,
since `xwalLog.TailBudgeted` reads backward and decodes only what it keeps. My
first fix was to EXCLUDE the construction read from the count.

SECOND, AND IT IS WHY THE CONTROL EXISTS: with that exclusion the control row
showed the flat window falling through ZERO times at an unbinding budget while
the tree paid its cold start. The flat window had been handed the entire
history for free and the tree had not, so the fixture was deciding the
comparison. THE EXCLUSION WAS THE WRONG FIX; THE FIXTURE WAS. The harness now
implements TailBudgeted the way production does, and construction is counted
for both.

    A CONTROL ROW IS NOT CEREMONY. It caught a bias that the headline row
    could not show, and the headline row moved 1.86x -> 2.34x when the bias
    was removed -- against my own instrument, in the direction that made the
    tree look better, which is the direction I should trust least.

### WHAT THIS NUMBER IS NOT

It counts ENTRIES, not bytes, decode time or allocation. The trace is a STATED
MODEL, not an observation of a real user. It is one lineage, so it says nothing
about fork sharing. And it measures the DECODED layer only.

## A LIVE DAEMON ON THE REAL STORE, AFTER THE NIGHT'S CHANGES (dec6ef8a)

scripts/live/idlemem.sh against a reflinked copy of the author's store, at head
3932015f, under the bench lock so it did not race a subagent's benchmark:

    after a full listing   PSS 120.2 MB   alloc 69.5 MiB
    idle, +5s onward       PSS  79.5 MB   alloc 30.7 MiB, flat for 40s
    listing again          PSS  88.4 MB

The arena comes back and stays back. This is a PRESENCE check, not a
comparison: no before-number was taken on the same box tonight, so it says the
bounded-by-default store does not regress a live daemon, and it does not say
what it saved. `nix build .#default` is green at the same head. The 535 MB
store copy the probe makes was deleted.

## RETRACTION: THE -43.8% IN 63902f44 DOES NOT REPRODUCE (dec6ef8a, 2026-08-19)

Written beside the claim rather than over it, because the claim is in a commit
message, in this file, and on the role board, and all three were read by other
arias tonight.

WHAT I PUBLISHED: TreeRangeParallel 2.213us -> 1.242us, -43.84%, p=0.000, n=8,
interleaved A/B under the bench lock.

WHAT I GET NOW, same protocol, binaries rebuilt from the same two versions of
cache.go (2d258884's and today's), all under the lock:

    quiet box, 64-unit Range     pre 1.367us   now 1.281us   p=0.105  NO EFFECT
    12 spinners on 16 threads    pre 3.742us   now 3.691us   p=0.513  NO EFFECT
    ONE-unit Range, 16 readers   pre 58.4ns    now 59.5ns    p=0.442  NO EFFECT
    ONE-unit serial              pre 220.5ns   now 228.9ns   p=0.442  NO EFFECT

The one-unit benchmark was added FOR this question: a 64-unit Range spends its
time copying units, and a mutex acquisition disappears inside that. A lock's
removal must not be measured on a fixture whose cost is dominated by something
else, and my original measurement was.

AND THE A/A CONTROL I SHOULD HAVE RUN FIRST: the same binary in both slots, in
the same fixed order, gives slot1 1.383us and slot2 1.330us -- nominally the
same direction as the original result and not significant (p=0.234). Fixed-order
interleaving does not protect against drift WITHIN a pair; the counterbalanced
order (ABBA) or a randomized one does. I alternated old,new,old,new and called
it controlled.

    INTERLEAVING IS NOT COUNTERBALANCING. AN A/B WITHOUT AN A/A IS AN
    UNCALIBRATED INSTRUMENT, AND I RAN FOUR OF THEM TONIGHT BEFORE RUNNING THE
    CONTROL.

### WHAT STANDS, AND IT IS NOT NOTHING

  - THE PROPERTY: TestHitTakesNoLock holds c.mu and serves a warm range from
    another goroutine. It is an artifact, not a number, and it is still green.
  - THE DEADLOCK SHAPES ARE GONE: no lock is held across the Source, the
    budget's eviction pass, or the Evicted hook. That was a correctness
    argument and never rested on the benchmark.
  - fd15d2a0's INDEPENDENT observation on the segment hit path, which is a
    fixture where 40ns operations make a lock visible: with the duplicate
    structure deleted the parallel hit is faster AND ITS VARIANCE COLLAPSES
    (51-64ns against 53-123ns). Tighter tails under contention is what removing
    a shared mutex should look like, and it was measured by someone who was not
    trying to defend my commit.

### WHAT DOES NOT STAND

The speedup as a headline. On these fixtures the lock's removal is NOT
measurable in the mean. The honest sentence is: THE READ PATH NO LONGER TAKES A
LOCK, WHICH REMOVES A DEADLOCK SHAPE AND A CONTENTION TAIL; ON A 2000-UNIT
FIXTURE IT DOES NOT MOVE THE MEAN.

The design conclusion the plan drew from the ORIGINAL figwal measurement --
Range 1218ns against a lock-free view's 516ns, "and the sign flips" -- is
UNTOUCHED BY THIS and also unverified by it: that was a different fixture on a
different machine with sixteen readers, and nobody has reproduced it here
either. IT SHOULD NOT BE CITED AGAIN WITHOUT BEING RE-RUN.

## THE HOT TAIL, AND THE CARVE-OUT'S REAL CAUSE (dec6ef8a, 2026-08-19)

plans/log-cache-policy.md carved out "LRU owns the COLD ranges, never the HOT
TAIL" and attributed it to tree's mutex. The lock is gone and the carve-out was
still justified -- BY SOMETHING ELSE ENTIRELY.

THE PROTOCOL FIRST, since tonight taught me that: two benchmarks IN ONE BINARY
AND ONE RUN, the flat window's tail read beside tree's Range over the same
span, ABBA-ordered so drift across the run cancels. The FLAT LINE IS THE
IN-RUN CONTROL: it moved 1.2% between the two builds, which is what licenses
comparing the ratios.

    BEFORE (6e8b696b)   flat 1.155us / 4.75KiB / 1 alloc
                        tree 4.892us / 16.78KiB / 4 allocs      4.24x, 3.53x
    AFTER               flat 1.169us / 4.75KiB / 1 alloc
                        tree 1.480us /  4.85KiB / 4 allocs      1.27x, 1.02x

WHAT WAS WRONG, AND IT WAS THE SURFACE, NOT THE POLICY: `Range` concatenated
its answer with `append` as it walked, so a span served by ONE resident run --
the hot tail, and every read smaller than a chunk -- paid a copy of the whole
span plus the regrowth of the destination. Now a single-piece answer is handed
back as A VIEW OF THE RUN'S OWN UNITS, and a multi-piece answer allocates ONCE
at the exact size.

    A CACHE THAT COPIES ITS ANSWER IS NOT A CACHE OF THE THING THE CALLER
    WANTED. It is a cache of the work needed to produce a copy of it.

AND THE TWO TENANTS FOUND THE SAME DEFECT FROM OPPOSITE ENDS ON THE SAME NIGHT.
fd15d2a0, on the segment payload path: "a read path that wants ONE RECORD
cannot use a surface shaped like a range without paying for the range" --
1153 B/op for one payload. Me, on the decoded tail: a 64-unit read paying
17 KiB. One is a point read, the other a span read, and both were paying for
MATERIALIZATION THE INDEX HAD ALREADY DONE.

### WHAT THIS DOES AND DOES NOT SETTLE

It removes the largest measured objection to re-seating the decoded layer on
tree: the hot tail is now 1.27x the flat window's read rather than 4.24x, on a
fixture where the flat window is doing its best case (everything resident, one
make+copy). Together with the locality gate's 2.34x fewer entries served from
below, the trade is now legible: A TAIL READ COSTS 27% MORE AND A HOPPING
READER FAULTS HALF AS OFTEN.

It does NOT settle the re-seat, which is Gluck's question, and it does not
touch fd15d2a0's segment finding: that gap is the KEYER's indirect call per
comparison against arithmetic indexing, which is a different mechanism in a
different tenant and needs its own answer.

# DECISION MEMO FOR GLUCK, 2026-08-19 MORNING (dec6ef8a)

Three questions, each with the number that makes it answerable. Nothing below
has been built past its answer.

## Q1. MAY tree GROW A DENSE-COORDINATE FAST PATH?

WHERE IT BITES: the segment payload cache. fd15d2a0 deleted its duplicate
residency structure (branch fix/segment-runs-in-tree, head fff67d86, NOT
merged) and the deletion costs ~1.7x on the SERIAL point read -- 46ns to 80ns
-- while making the PARALLEL read faster and far tighter (51-64ns against
53-123ns).

MECHANISM, read after the first diagnosis was refuted by its own number:
tree.At binary-searches units through the KEYER, an indirect call per
comparison, about five per lookup. The deleted structure did not search at all:
its coordinates are DENSE, so it indexed arithmetically.

THE ASK: may a tenant whose units are contiguous declare that, and index
instead of search? It is surface on a shared package, which is why it is your
call and not mine.

IF NO: the segment deletion does not land, and one tenant keeps two structures
over one data -- which the standing block forbids, so a NO here needs a third
option rather than the status quo.

## Q2. MAY tree GROW AN EVICTION INDEX?

MEASURED (677d5768): `Budget.charge` asks every owner for its coldest run (one
full scan) and then tells the winner to evict (a second), so ONE EVICTION
VISITS 2R RUNS -- 32 at R=16, 128 at R=64, 512 at R=256 -- and `TrimIdle`
repeats the pair per dropped run, making a full sweep O(R^2): 4159 visits to
drop 63 runs at R=64.

THE ASK: a heap keyed by effective epoch. That is a data structure added to a
hot layer, which your standing rule reserves for you. Not built.

## Q3. IS cachedLog RE-SEATED ON tree WHOLESALE?

This is the consolidation itself: one residency policy for the decoded IR and
translations instead of a flat tail window beside tree. The two numbers that
were missing are now in hand.

    THE FAULT RATE (3932015f, the gate log-cache-policy.md named and nobody
    made): under a scroll/hop trace at a binding budget, the flat window serves
    4952 entries from below and the tree 2116 -- 2.34x. With a control row at
    an unbinding budget where neither re-materialises.

    THE TAIL COST (81d5f5b0, 8f569d90): tree's hot-tail read was 4.24x the flat
    window's and is now 1.13-1.27x, with allocations at 3 against 1. The gap
    was the SURFACE -- Range copied its answer -- not the policy and not the
    lock.

SO THE TRADE IS: A TAIL READ COSTS ~15-27% MORE AND A HOPPING READER FAULTS
HALF AS OFTEN. Plus one policy instead of two, one seeding path instead of two
(the donation walker is already unified, 567a9cbc), and the fork seam becoming
structural rather than a one-shot donation guarded by a probe.

WHAT MUST BE RECONCILED IF YOU SAY YES, stated so a yes is not a blank cheque:
  - THE BUDGET UNITS DISAGREE. The per-aria windows are denominated in DECODED
    ESTIMATE (newWindowedLog takes an `inflation` factor for exactly this) and
    tree's budget in the units the Sizer returns. One number called "budget"
    would otherwise mean two quantities depending on which layer read it.
  - THE SEAM PROBE IS CARRYING THE CORRECTNESS. Measured, not read: deleting
    the fork-base bound leaves the tests green because the probe refuses the
    donation. Structural sharing must preserve that role or make it
    unnecessary BY CONSTRUCTION.
  - `cachedLog.Read` FALLS THROUGH TO THE WHOLE CHANNEL once anything has
    trimmed, and the cold projection path genuinely needs the prefix. A tree
    does not fix that by itself -- the fix is the projection memo, i.e. the
    cache IS the projection -- and the re-seat should not be sold as if it did.

## AND ONE THING THAT NEEDS NO DECISION, ONLY YOUR EYE

I published a -43.8% that does not reproduce and retracted it (e124f064). The
protocol error is the useful part: INTERLEAVING IS NOT COUNTERBALANCING, and an
A/B without an A/A is an uncalibrated instrument. Everything measured after
that retraction uses two benchmarks in ONE binary with an in-run control, and
reports the deterministic quantity (allocations, counts) ahead of the timing.

## THE APPLY-CHECK, DONE AT THE RECIPIENT'S HEAD (dec6ef8a)

fd15d2a0's branch was cut at 2d258884 and main moved eight commits under it, so
it was apply-checked here rather than assumed. IT DOES NOT MERGE CLEANLY, and
the conflict is in ONE FUNCTION -- the one both of us did our best work in, the
same night:

    THEIRS  split rangeInNode into rangeInNode -> rangeInNodeAt(nd, coord), so
            a node handle does not re-hash the node's name per record.
    MINE    rewrote that function's body so a span answered by ONE run is
            handed back as a view instead of copied.

Both are wanted; the reconciliation is their split with my body, and it is
mechanical. It is done and gated on branch
`prepared/segment-runs-in-tree-at-684978ad` (a301e725, worktree
/var/tmp/fig-trial): build, full suite, and store -race all green.

THAT BRANCH IS NOT FOR MAIN AS IT STANDS -- it carries the ~1.7x serial
point-read regression that is Q1. It exists so that a yes costs a merge and a
no costs a delete.

    "IT MERGED WITHOUT CONFLICT" WAS ALREADY DISPROVED TONIGHT (2d258884, a
    rename that merged clean and did not compile). This is the other half: a
    branch that CONFLICTS is not a branch in trouble -- it is two arias having
    improved the same function, and the check is what tells you which.

## THE WHOLE BOARD: THREE RESIDENCY POLICIES, WHERE EACH STANDS TONIGHT

Written so the memo's Q3 is not read as if it were the last layer. It is not.

    LAYER          STRUCTURE TODAY                 STATE
    raw segment    tree.Cache + (a duplicate       DELETION DONE, NOT LANDED.
    payloads       runs slice, deleted on a        Q1 gates it: 1.7x on the
                   branch)                         serial point read.
    decoded IR     cachedLog: flat tail window,    Q3. Both numbers now exist:
    + translations one per (aria, channel), with   2.34x fewer faults under a
                   a one-shot fork donation        hopping trace, tail read at
                   guarded by a seam probe         1.13-1.27x.
    composed UI    livelog/aria.TurnCache with     UNTOUCHED, AND UNMEASURED.
    IR             its OWN UIBudget: byte budget,  Its own comment says "a byte
                   LRU, recompose-on-miss, a       budget, LRU, and
                   process-global mutex, 509       recompose-on-miss" -- which
                   lines                           is tree.Cache's sentence.

THE THIRD ROW IS THE ONE NOBODY HAS ARGUED ABOUT. It re-implements the same
policy a third time, in another package, with its own accountant and its own
lock, and the reason it survived is the reason every layer survived: nobody
ever had to make a case FOR it. Under the deletion default that is the state
that needs permission, not the state that needs a reason to change.

I have not measured it and I have not touched it. What it needs is the same two
numbers the decoded layer now has -- a fault rate under a realistic trace, and
the cost of its hot read against tree's -- taken with the protocol this file
now carries (two benchmarks in one binary, an in-run control, deterministic
quantities reported ahead of timings). That is the next measurement whoever
holds this role should make, and it should be made BEFORE Q3 is executed rather
than after, because a re-seat designed for two tenants that must then admit a
third is the accretion's own shape one more time.

### AND IT IS NOT MERELY ANOTHER CACHE -- IT IS THE SAME DESIGN, TWICE

Read rather than assumed, correspondence by correspondence:

    tree.Cache / Budget                 aria.TurnCache / UIBudget
    -------------------------------     ------------------------------------
    run (units | hollow, bytes)         Turn + turnMeta (Nodes | hollow, bytes)
    "THE INDEX SURVIVES EVICTION"       "THE INDEX SURVIVES EVICTION"
    Source(Coord) rematerializes        TurnSource(fromLT,toLT) recomposes
    Budget: bytes, limit, evictions     UIBudget: bytes, limit, evictions
    coldest / evictColdest              victimsLocked / hollow
    Recomposes()                        Recomposes()
    pinned: counted, never evicted      pinned: counted, out of the LRU
    "the hook takes no lock, victims    "NEVER calls into an owner while
     are hollowed outside"               holding it: it returns victims"
    epoch-based recency                 container/list LRU

509 lines against tree's 644+216, and THE TWO FILES CITE THE SAME FINDING IN
THE SAME WORDS: "a meter that reads zero exactly when retention is worst is the
worst possible meter", attributed in both to plans/storm-triage.md's S1.

    TWO IMPLEMENTATIONS THAT QUOTE THE SAME POST-MORTEM ARE NOT TWO CACHES
    THAT HAPPEN TO BE SIMILAR. They are one design, written twice, by people
    who had both read the same incident -- which is exactly how an accretion
    forms among careful people rather than careless ones.

THE ONE REAL DIFFERENCE is the addressing: tree names ranges in a lineage
(Coord, Ref, fork bases) and TurnCache names an index into one aria's sealed
turns. That is a narrower key, not a different structure -- and a narrower key
is what the dense-coordinate question (Q1) is about at the other end of the
stack.

I have measured NOTHING here. This is a reading, and it is the reading that
says the third layer belongs in Q3's scope rather than after it.

### AND NOW THE NUMBER: THE COMPOSED LAYER FAULTS AT PARITY WITH tree

Same trace, same budget, both production structures, counting TURNS SERVED
FROM BELOW (400 sealed turns of 8 KiB, a budget holding ~40 of them, a
tail-and-hop trace of 200 reads asking for 1600 turns):

    COMPOSED (aria.TurnCache)   612 turns from below, 131 recompose calls
    CANONICAL (tree.Cache)      722 turns from below, 141 source calls

tree faults 1.18x more on this fixture. THAT IS PARITY FOR A DECISION OF THIS
KIND: the question was never whether the third implementation is a better cache
than the canonical one, it is whether 509 lines of a second implementation buy
anything, and 18% on one synthetic trace is not a case for keeping them --
particularly since the same 18% is a knob (see below) rather than a property.

HYPOTHESIS FOR THE GAP, UNVERIFIED AND NAMED AS SUCH: EVICTION GRANULARITY.
tree hollows a whole RUN (here up to eight turns at a time); TurnCache hollows
ONE TURN. Under a budget of ~40 turns, dropping eight to make room for one
costs more subsequent faults than dropping one. THE EXPERIMENT THAT WOULD
SETTLE IT: re-run with runChunk driven down to 1 and see whether the two
numbers converge -- if they do, the gap is granularity and tunable; if they do
not, something else is going on and this paragraph is wrong.

WHAT THIS DOES NOT SAY: nothing about the COST of a fault. The composed layer's
recompose is a walk over fig IR; the fixture's is a map filter. Fault RATE is
what was asked and fault rate is what was answered.

    AND THE RUN LENGTH IS A COUNT, NOT A BYTE TARGET (runChunk = 64). For
    8 KiB units that is a half-megabyte run; the code already knows the shape
    of this problem -- "a run larger than the whole budget can never stay
    resident" is a comment in the refill path -- but nothing sizes a run by
    bytes. If the decoded and composed layers land on tree, THAT is the knob
    that decides their fault rate, and it is currently a constant chosen for
    segment records.

#### THE HYPOTHESIS, RUN RATHER THAN LEFT STANDING

The paragraph above named an experiment: drive runChunk down and see whether
the two numbers converge. Done in the same hour, because a hypothesis left in a
plan is read as a finding by the next person.

    runChunk    turns from below      source calls
       1              620                 620
       4              681                 183
      16              722                 141
      64              722                 141
    COMPOSED          612                 131

IT CONVERGES. At runChunk = 1 the canonical cache faults within 1.3% of the
second implementation. THE 18% GAP WAS GRANULARITY AND NOTHING ELSE, and the
third residency policy buys no fault-rate advantage that a constant does not
already explain.

AND THE TABLE SHOWS THE TRADE THAT REPLACES IT: at runChunk = 1 the turns
wasted fall to nothing and the SOURCE CALLS RISE FROM 141 TO 620. Fine-grained
runs waste fewer bytes and pay more calls; coarse runs do the reverse. That is
the ordinary granularity trade, and it says the knob should be A BYTE TARGET
PER RUN rather than a record count -- one number that means the same thing for
a 200-byte segment record and an 8 KiB composed turn, which the current
constant cannot.

    THE CONSTANT IS NOT A DEFECT TODAY: 64 records was chosen for the segment
    tenant and is right for it. It becomes a defect the moment a second tenant
    with units two orders of magnitude larger lands on the same cache, which is
    exactly what Q3 proposes.
