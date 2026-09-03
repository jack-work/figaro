# Handoff: what ac9c3993 measured, for whoever builds the layered cache

Written 2026-08-15 by aria ac9c3993, on standing down from coding.
Branch `phase/forest-uptake`, worktree `/home/gluck/dev/figaro-qua/forest`,
17 commits, clean. One commit pushed to figwal master (`c64f2ee`).

Read `/home/gluck/notes/layered-cache-design.md` for the design. This file is
the EVIDENCE under it: what was measured, how, and what each number forbids.

---

## 1. One conflict with the design, raised before it is built

`Layered.live` is specified as "the in-flight region, lock-free". For fig IR
that is NARROWER than what is lock-free today.

`cachedLog` publishes its WHOLE resident window through an `atomic.Pointer`,
measured at ~420 rows on a real 2556-message aria, not just the open turn.
Every read of recent-but-sealed history is served from it without a lock.

If `live` covers only the in-flight region, all of that moves to `mem`, and
`mem` is `forest.Cache`, which takes a mutex:

    forest.Range   parallel 1218 ns/op    serial 650 ns/op
    cachedLog view parallel  516 ns/op    serial 551 ns/op

The sign flips: forest slows under contention, the published view speeds up.
2.4x apart at 16 readers and widening. This is the design's own condition 1,
and a narrow `live` violates it by accident rather than by intent.

**Resolution to decide before building:** either `live` spans the whole
published window for fig IR (not just the in-flight region), or cachedLog's
window stays and `mem` sits strictly below it. Either is fine. Silently
narrowing the lock-free path is not.

## 2. One layer with no number behind it

Every layer in the design is justified by a measurement except one: `mem` for
FIG IR, which the doc marks "always".

Phase 3 measured what forest could buy that layer and found all three
candidate jobs already taken, sharing by seeding at open, bounding by the
window, serving-neither by the pass-through. What was NOT measured is whether
a mem LRU helps bounded, non-scan RE-reads of a cold range. That is the only
case left, it is rare (`show -n` twice on the same old region), and
`cachedLog.ReadPage` already argues against populating on backward paging in
its own words: *"a scroll must not permanently re-resident a prefix nobody
will read again."*

Not retired, unmeasured. It deserves a number before it is built, and
"always" is a stronger claim than the evidence currently supports.

## 3. The three findings that are the design's spine

**(a) The duplication is a second DECODE and a second COMPOSITION, and
seeding at open kills both.** Proven by pointer identity, not by heap, heap
is the wrong ruler here and has misled this project at least three times.

    fig IR:  shared record LT 50: childA duplicated=true, childB duplicated=true
             shallow copy: 200 of 202 entries compared, every payload string shared
    UI IR:   120 strings compared, 40 SHARED, 80 MINTED
             shallow copy of 40 turns: 120 node strings compared, every one shared

The 40 shared upstairs are prose, which compose passes through; the 80 minted
are tool nodes' Input/Output, which compose rebuilds, and by BYTES the minted
ones dominate. Seeding is cheaper than any cache and orthogonal to it.

**(b) LRU may never own the hot tail.** See §1 for the numbers.

**(c) A whole-history read costs neighbours their residency, so scans pass
through.** One scan cost two primed neighbours a rematerialization each; five
listing-shaped scans, the same. A translator cold pass IS such a read.

## 4. What S6 costs today: the before-number for incremental composition

`composeTurn` reads the whole open region; `compose.Nodes` rebuilds every node
of it on every stream event; `Server.Update` then diffs and discards whatever
did not change. One frame, by turn size:

| rounds | ns/frame | B/frame | allocs/frame |
|---|---|---|---|
| 10 | 50,788 | 165,814 | 129 |
| 40 | 196,509 | 667,963 | 495 |
| 160 | 812,512 | 2,681,946 | 1,978 |

4x the rounds costs 3.9–4.1x the frame. Linear in turn size, per frame, for
work already done. One 40-round turn at 8 frames/round:

    38.6 ms, 152,796,098 B/op, 103,560 allocs

152.8 MB of allocation for ONE turn, reproduced with no daemon. The live
daemon attributed 157.7 MB to `compose.Nodes`; those are different quantities
(total allocation here, profile attribution there), so treat the agreement as
corroboration, not proof.

**The hazard to pin before writing incremental composition:** it must produce
exactly what wholesale composition produces, for the same inputs, AT EVERY
FRAME, not only at seal. A composer converging only at seal shows correct
transcripts and wrong live frames, and live/committed divergence is forbidden
by invariant. Drive N frames incrementally, compose wholesale, assert
node-for-node equality at every frame.

## 5. Artifacts to take, and one to leave

**Take** (all green, all marked PERMANENT, NOT MIGRATION SCAFFOLDING):

| artifact | what it holds |
|---|---|
| `internal/store/held_view_test.go` | a published view is never mutated under a reader |
| `internal/store/seam_test.go` | every LT exactly once and in order across the boundary; gaps fail too |
| `internal/store/forkbase_convention_test.go` | marker = first-record = BranchedLT = forest `Ref.Base`; prefix sharing proven by source-call counts |
| `internal/store/scan_pollution_test.go` + `scan_policy_test.go` | scans pollute (dependency fact) / our public reads do not (our fact) |
| `internal/store/evicted_nolock_test.go` | Evicted fires under the caller's write lock 20/20; a hook must be a pointer swap |
| `internal/store/lineage.go` + `lineage_test.go` | `[]forest.Ref` from figaro's own topology; 47 lines, no figwal export needed |
| `internal/store/base_leak_test.go` | an ancestor run spanning a base never leaks its post-fork future into the child |
| `internal/store/decode_duplication_test.go`, `internal/cli/compose_duplication_test.go` | the identity measurements in §3(a) |
| `internal/store/cached_log_bench_test.go`, `forest_range_bench_test.go`, `internal/compose/openturn_bench_test.go` | every benchmark, fixture faults already fixed |

**Leave:** the trim-to-Put re-seat. A measurement retired it; do not rebuild it.

## 6. On fixtures: read this before trusting any number here

Seven fixture faults were caught in this campaign, each of which would have
become a confident wrong report:

1. a benchmark that measured an EMPTY read (Keyer returned 0; reported 209ns
   and would have been shown as "forest beats the lock-free path")
2. a Keyer parsing with `fmt.Sscanf`, so 104 allocs/op was the fixture
3. a residency test that would have stayed green through its own regression,
   had it not first asserted a warm read costs the layer below nothing
4. a text comparison fataling on the outfit birth record, which carries no text
5. a sharing test that had compared nothing
6. a compose test reading only `Node.Markdown`, so tool text was invisible
7. a compose test building `tool_result` with no matching `tool_use`, a result
   alone composes to NOTHING, giving `map[prose:14]` and the answer
   "0 MINTED, composition already shares", which is the opposite of the truth

Every one was caught by asking whether the fixture could fail, not by
cleverness. Number 7 nearly retired phase 4 on a fixture that had never
composed a single tool node.

The four maxims this produced are in `skills/figaro/contributing/maintaining.md`,
which ships inside the binary: *instrument the hazard, do not suffer it*;
*assert the fact, not the wish*; *name the subject*; *prove the fixture can
fail*.

## 7. Provenance, since the next hand needs to weigh it

I corrected the role bearer (94f0752b) four times with measurements and every
correction held: that `feat/form-deltas-ui` was safe to delete only after
proving the content was cherry-picked (my first conclusion there was WRONG and
theirs was right); that heap profiles ignore pprof labels; that S3's aliasing
charge was acquitted by their own verdict table; and that the scan-pollution
fix needed no new API because the policy already shipped in
`cachedLog.ReadPage`'s comment.

They corrected me at least as usefully: the deadlock inversion, the moved
seam's leak hazard, that a fresh fork's reads all land on the shared path, and,
the sharpest, that my scan test had the wrong SUBJECT. Neither of us would
have derived the four maxims alone.

Two of my own conclusions were overturned by my own later measurements: the
~90-line deletion projection (made against a design I had not yet measured),
and the seam's location (window edge, actually the fork base). Weigh
everything here as measurements, not as authority.
