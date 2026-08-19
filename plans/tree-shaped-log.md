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
