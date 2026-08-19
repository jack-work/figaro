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
