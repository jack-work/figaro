# One cache shape for every tree-shaped log

Gluck's decision, 2026-08-15, approved by the role bearer with three
conditions that are measurements rather than opinions.

## The decision

Every log that is tree-shaped in xwal gets an in-memory cache, and the
shape is **LRU over ranges**, not the tail-window cachedLog uses today.
The reasoning is his and it is sound: where a log is never paged
backward, LRU degenerates to "hold what is used" and costs nothing
extra; where a log IS paged backward (the IR under a fast scroll, a
reader hopping through a transcript), a tail window re-decodes on every
hop and LRU does not. One shape serves both; two shapes serve one each
and disagree at the seam.

That shape already exists and is released: figwal's `forest.Cache[U]`
(runs, LRU by epoch, an index that survives eviction, rematerialize on
miss, one shared byte Budget). Its first tenant, the segment payload
cache, shipped in v0.18.0.

## The three conditions

1. **LRU OWNS THE COLD RANGES, NOT THE HOT TAIL.** Measured: forest
   `Range` costs 1218 ns/op under 16 readers where cachedLog's
   atomic-pointer view costs 516, and the sign flips, forest slowing
   with contention while the lock-free view speeds up. So the hot tail
   keeps its immutable published view and its lock-free read; LRU takes
   the ranges below it. Anything that puts a mutex on the tail read is
   a regression against a benchmark that already exists.

2. **SCAN-SHAPED READS PASS THROUGH.** Measured: one whole-history read
   through the cache cost two primed neighbours their residency (1 and 1
   rematerializations each). figaro already wrote this policy down in
   cachedLog.ReadPage, "a scroll must not permanently re-resident a
   prefix nobody will read again", and the translator's cold pass is
   exactly such a read: it walks the whole log to rebuild a request
   body. Cache what a reader will come back to; pass through what it
   will not.

3. **SEED CHILDREN AT OPEN, INDEPENDENTLY OF ANY CACHE.** Measured
   twice, by pointer identity: a fork re-DECODES its parent's prefix
   (decoded IR) and re-COMPOSES it (UI IR), and a shallow copy of the
   ancestor's resident rows shares every payload string. Seeding is
   cheaper than any cache can be, needs no forest, and is orthogonal to
   this decision.

## The translator log's different dynamics, as Gluck observed

Translations are read whole on a cold pass and appended at the tail
otherwise; nobody pages backward through them. So the useful policy
there is "hold the whole thing while it fits the budget, pass through
when it does not", which is what a byte-budgeted LRU does naturally --
and the per-namespace budgets (`ir_window_mb`, `translation_window_mb`)
become budgets on the shared accountant rather than three separate
implementations of the same arithmetic.

## What gates it

The measurement that has not been made: a realistic scroll/hop pattern
against the IR, counting fall-throughs below the window. ac9c3993 built
the instrument (a counting decorator on the layer below). If hopping
re-decodes repeatedly today, this decision is justified by locality --
which is a better justification than the fork-sharing one that
collapsed under measurement in phases 3 and 4.

PRIORITY: this waits behind S6 (incremental composition), per Gluck's
ruling of the same evening. S6 is 157.7 MB and 44% of live heap; this
is an efficiency policy on top of a quantity S6 shrinks at the source.
