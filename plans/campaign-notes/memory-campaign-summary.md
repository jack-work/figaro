# The figaro memory campaign: what it bought, what it refused, what is left

Written 2026-08-16 by aria 091d162e, continuing aria 53289ae2, at the
direction of the role bearer (94f0752b). Branch `feat/layered-cache`,
32 commits ahead of `b734fcd0`, worktree clean, gate green.

For Gluck. Numbers here are the ones that survived verification; where a
number was later found to be misleading, the correction is stated rather
than the original quietly replaced.

---

## 1. WHAT SHIPPED

### The live frame

The campaign began because one live frame recomposed the whole open
region. At 64 rounds with 200-line tool outputs:

    stage                              ns/op        B/op    allocs
    baseline (1245d741)              416,537     ~570 KB       788
    after the output clamp           148,911k/turn  (bytes: 6.4x)
    after incremental composition     78,644     177,933       788
    after the changed-range report    ~16,200      71,734        30
    after holding the region          ~6,000       1,352        21

THE LAST LINE IS THE STILL FRAME AND QUOTING IT ALONE WOULD BE TRUE AND
MISLEADING. It re-emits a frame in which nothing changed. Production's
streaming frame changes one partial per event:

    streaming frame                  ~12,000       9,543        22

That is the honest headline: a frame that used to cost 416,537 ns and
788 allocations now costs ~12,000 ns and 22 while carrying the same
1,009,217 bytes of composed node text. The remaining cost is the clamp
on the one node that is moving (tailBound, 32.9% of the profile) and the
memo's validity guard (25.9%) -- the price of being able to drop rather
than lie.

Attribution is preserved per commit, deliberately: the clamp took the
BYTES (570 MB -> 88.8 MB per turn, 3% of the time), the composer took
the TIME (148.9 ms -> 18.1 ms per turn). Measured together they would
have been one number nobody could revert half of.

### Two correctness fixes, each with its own canary

  - `62505d20` compose: the stable boundary MATCHES results to invokes
    by id instead of counting them. Counting could be satisfied by a
    duplicate result while the call that was actually streaming had
    none, and that call's node renders from a map the memo key cannot
    see. LATENT, NOT LIVE -- stated that way in the commit, because the
    shapes that reach it are prevented by code in another package.
    0 B, 0 allocs; 934 ns -> 1231 ns per frame, ~1.3% of a frame.

  - `61927498` figaro: the two guards the memo depends on FROM A
    DISTANCE are now asserted where they live -- one result per call in
    one tic, and a tool timing stamped before its result goes durable.
    Their canary reported its own limit: a late-stamp mutation fails at
    the "never arrived" arm, not the ordering arm, so the ordering
    comparison is canaried separately against hand-built frames.

### All THREE fork seeds, measured by IDENTITY

Bytes have misled this project three times; pointer identity has not
misled it once.

  - `9343979f` store: a fork seeds its decoded prefix from the ancestor
    that already holds it.
        before  296 strings compared, 0 SHARED, 296 MINTED
        after   296 strings compared, 296 SHARED, 0 MINTED

  - `7f2c99e4` figaro: a fork splices the composed prefix its ancestor
    already holds (`spliceDonated` / `composeSealedTurns` / `TurnsBelow`,
    wired through `Config.TurnDonor` and `angelus.TurnDonor`).
        before   24 compared, 16 SHARED,  8 MINTED
        after    18 compared, 18 SHARED,  0 MINTED
    The minted ones were tool `Output` -- the strings that dominate by
    bytes, which is phase 4's 80-of-120 at its own fixture size.

  - `729c550d` store: a fork seeds its decoded TRANSLATIONS from the
    ancestor, per provider namespace.
        before  76 blocks compared, 0 SHARED, 76 MINTED (all three ns)
        after   76 blocks compared, 76 SHARED, 0 MINTED (all three ns)
    Nobody was re-translating: the round-trips were already shared
    through xwal's fork base. Only the DECODE was duplicated. Two
    hazards that the fig IR does not have are checked in code -- a
    donation must be fingerprint-uniform AND match the durable record
    at the seam (the seam probe compared LT and payload but not
    fingerprint, which was a real hole), and namespace is structural.
    The check fires on a uniform ancestor and refuses only after an
    encoder-dialect change.

Both refuse any donation they cannot prove is a prefix, and degrade to
the old behaviour -- a full decode or a full composition -- rather than
to a wrong history.

---

## 2. THE DELETION LEDGER, PLAINLY

    production Go (non-test)     +812  /  -49
    with tests                 +6,198  /  -81
    commits                        32

Gluck's bar was deletions meeting or exceeding additions. IT IS NOT MET,
and the reason is structural rather than an oversight:

THE CAMPAIGN REMOVED A QUANTITY, NOT A MECHANISM. The 26x on the frame
came from doing less work per frame, not from deleting a subsystem. The
deletions the design promised -- cachedLog's window arithmetic,
TurnCache's private accountant, the translator's ad-hoc cache handling --
were all to be paid for by ONE layered cache replacing three bespoke
ones. All three of those replacements were refused on evidence (below).
Refusing them keeps three accountants alive; building them would have
deleted three and added one larger one, on faith.

The line count is therefore the honest signature of a MEASUREMENT
campaign. Roughly 5,000 of the added lines are tests, oracles and
instruments, several of which are permanent equivalence oracles kept on
purpose: an equivalence claim is a fact about every day after the change
and stays checkable only while both implementations are present.

---

## 3. THE THREE COLLAPSES, AS FINDINGS

Each asked what a cache would buy a layer, and each was told the job was
already taken. That is the campaign's central result.

### Phase 3 -- forest under the decoded IR: a SEED, not a cache

Measured by identity: opening a fork decodes the shared prefix again and
mints strings the parent holds (`childA duplicated=true, childB
duplicated=true`), and a shallow copy shares all of them (`200 of 202
entries, every payload string shared`). Forest's three candidate jobs
were already taken -- the shared prefix by seeding, the tail by the
window, rows nobody holds by the pass-through. Collapsed into
`9343979f`, which introduced no mutex and left the 516 ns lock-free read
path untouched.

### Phase 4 -- forest under the composed UI IR: a SEED, not a cache

Same instrument, one layer up: `120 strings compared, 40 SHARED, 80
MINTED`, and a shallow copy of turns shares every node string. Collapsed
into `7f2c99e4`.

### (c) -- the layered cache itself: THE CLIENT ALREADY HOLDS THE LOCALITY

The design gated `mem` under fig IR on one number and said so in its own
words: "DO NOT BUILD WITHOUT DATA... the hop/scroll fall-through count."
Run at the owner's real parameters (ir_window_mb = 4, ~2500 messages,
tail-heavy skew):

    window                 499 of 2500 rows resident
    scroll, one pass       100 fall-throughs, 0 repeats
    hop, 3 anchors x 3     10 fall-throughs, 6 REPEATS

This layer does re-decode a repeated cold range. THE PRODUCT DOES NOT
ASK IT TO: the CLI client holds folded ranges in its own `aria.Store`
and `Ensure` fetches only HOLES, with no eviction, so scroll-back is
served client-side and the daemon never hears the second request. The
repeats that survive are across clients and processes -- reattach, a
second pane, `fig show` -- each a cold start where the window is cold
anyway.

VERDICT: do not build the layered cache. `3d111219` keeps the gate as a
permanent test, asserting the fact in the direction it is true, so it
goes red the day something memoizes below the window.

WHAT SURVIVES OF THE DESIGN, and it is not nothing:
  - the SHAPE was right and is already shipped where it was needed:
    forest.Cache is in figwal v0.18.0 with the segment payload cache as
    its tenant;
  - the DIAGNOSIS was right three times -- every layer really did
    duplicate work across forks. The fix was cheaper than what was
    asked for, not a refusal of it;
  - every condition the design carried in from measurement held, and
    applying them is what retired the cache.

---

## 4. WHAT IS LEFT UNBUILT, AND WHY

  1. THE TRANSLATOR'S DERIVE-AND-WRITE-BACK. The only surviving piece
     with a case. A cold pass re-derives what a previous process
     computed and disk already holds -- a claim about DURABILITY, not
     locality, so nothing measured above speaks to it. Its measurement
     is specified in `~/notes/layered-cache-design.md`: count records
     derived that disk already holds, then price one derivation against
     one decode of the same range. If derive and decode are within a
     factor, the disk round trip is the more expensive half.

  2. P1/P2, THE SECOND EMITTER. A mutant composer that claims every node
     is settled still passes the reattach pty case. Two hypotheses were
     tested and refuted: "the ticks are a spinner" (they are literal
     tool output, and the case already asserts growth) and "the memo
     absorbs the lie" (M1 produces 22 split-rule violations and 30
     frames with a live-map dependency in the prefix). So something
     other than the composer emits frames on the live path. Recipe to
     settle it: rebuild M1, run with FIGARO_NODE_DEBUG, diff composed
     frames against two pane captures 8 s apart. Costs provider tokens.
     An INSTRUMENT question, which is why it stayed last.

  3. SCENARIO 2, the mid-turn steer: unverifiable in the current harness
     (auto-promotes at 101 and 201, chrome=2). Queued to the CLI/client
     fold refactor with its evidence.

  4. THE DETACHED-TAIL FAILURE: pre-existing, attributed by canary to
     b734fcd0 with both md5s recorded
     (`~/notes/figaro/detached-tail-failure.md`). Belongs to the CLI
     refactor's queue.

  5. KEYING THE MEMO ON WHAT IT RENDERS (partials, argPartials,
     timings). Design written, unbuilt on purpose; its trigger is named:
     a measurement showing partial invalidation is hot, or a path in
     internal/figaro that appends a call's result in more than one
     message.

### A trap for whoever instruments the store layer next

`ReadPage(from, 0, n)` on a TRIMMED window ALWAYS falls through to the
source: `before == 0` means "to the end of history", which a partial
window refuses to answer from memory. A fixture that pages with a zero
upper bound measures the source every time and will report -- correctly
and uselessly -- that the cache does nothing. Caught by a vacuity guard
on its first run, which is the only reason the hop gate's numbers mean
anything.

---

## 5. THE FIVE MAXIMS, AND WHERE THEY LIVE

`skills/figaro/contributing/maintaining.md`, each paid for this week:

  1. MEASURE BEFORE YOU BUILD. Phases 3, 4 and (c) all collapsed on
     measurements taken before their code was written.
  2. WRITE THE HAZARD TEST BEFORE THE CODE THAT COULD VIOLATE IT. The
     aliasing test for the held region asks a CAPACITY question,
     because no comparison of contents can see that law being broken.
  3. CANARY EVERY TEST. A test that has never failed is not evidence.
     Three canaries this session exposed the TEST rather than the code:
     a seam off-by-one that passed because the fixture never exercised
     the seam; a mutation that did not compile and proved nothing; and
     a frame verification whose four assertions were all wrong before
     they were right.
  4. PRE-REGISTER PREDICTIONS AND SCORE THEM IN PUBLIC. The read-side
     change beat its own floor by 20x, which the pre-registration had
     already named as the suspicious direction -- so it was investigated
     before it was claimed.
  5. COMMIT AT EVERY STEP, SEPARATE BASELINES PER FIX. Two fixes
     measured as one cannot be reverted independently; this campaign
     priced that discipline instead of arguing for it.

---

## 6. STATE OF THE BRANCH

32 commits ahead of `b734fcd0`. Nothing merged; Gluck decides what does.
No test was left red, no fixture was left unable to fail, and every
number above is reproducible from the tests in the branch.

---

## CORRECTION (2026-08-16 12:30, role bearer 94f0752b)

I told Gluck the retention trade was "fewer collections, MORE live data
per collection". The first half holds; the second is backwards, and he
caught it by asking why.

A collection here is a Go GC cycle: mark everything reachable, sweep the
rest. It is triggered by HEAP GROWTH (GOGC, or GOMEMLIMIT pressure), so
the allocation rate sets how OFTEN it runs; and its cost scales with
LIVE POINTER-DENSE DATA, not with garbage, so survivors are what you pay
for.

  BEFORE: each frame minted a whole-region []Node (~36 KB, pointer
          dense) AND the server copied it. Roughly TWO live copies at
          any instant, plus a stream of garbage at ~11 frames/second.
  AFTER:  the composer publishes prefix+suffix, the server RETAINS BY
          REFERENCE. ONE copy, shared, and no per-frame garbage.

So it is fewer cycles AND less live data per cycle -- better on both
axes. Seeding is the same shape: one copy of a prefix where there were
two.

WHAT THE REAL CAVEAT IS: not volume, LIFETIME. Shared bytes live as long
as the longest-lived sharer, so dropping an ancestor no longer frees a
prefix a child holds. Total live is still lower; WHEN it frees changed,
and therefore what an eviction buys changed.

UNMEASURED, AND SAID SO: the paragraph above is reasoning, not a
measurement. What would settle it is a heap profile of live
[]livedoc.Node bytes before and after on one workload. The instrument
exists; nobody has run it. Stating a trade-off without it is error class
3 (wrong unit of account) committed by the reviewer who catalogued it.
