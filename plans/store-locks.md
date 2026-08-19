# internal/store's LOCKS: A CLASSIFICATION

fd15d2a0, 2026-08-19, on @980dc16c's order. SURVEY, NOT SURGERY. Nothing is
removed, nothing is recommended, and no ordering is implied.

## THE LIMIT OF THE METHOD, STATED WITH THE FINDINGS AND NOT AFTER THEM

I ENUMERATED BY GREP AND CLASSIFIED BY READING. For every row I read the
declaration, its guarded fields and at least one lock site; for the rows marked
**(read: full)** I read every `Lock()` call site in the file. For rows marked
**(inference)** I read the declaration and its comment but NOT every caller, so
the "calls out" column is a lower bound there — I can prove a call-out exists,
never that none does.

WHAT I COULD NOT DETERMINE AT ALL: whether any given critical section is
actually contended. That is a measurement and this is a reading. Column 4 says
what shape a lock has, NOT what it costs.

COUNT: 30 declarations under `internal/store/` in non-test files at
`f2e9326a`. One (`crashtest/child.go:115`) is in a test-helper binary and is
listed for completeness rather than as production surface.

CURE KEY, from the brief: **A** = publish an immutable value behind an atomic
pointer (removes the lock, keeps the concurrency). **B** = move the work onto a
serialized loop (removes the lock and the concurrency). **NEITHER** = a genuine
multi-object transaction, or a section that must exclude a writer for
correctness.

---

## THE TABLE

| # | file:line — field | guards | invariant spanning the section that could NOT be one published value | cure | calls out? |
|---|---|---|---|---|---|
| 1 | `cached_log.go:51` — `writeMu` (read: full) | mutators only; readers never take it | **NONE for the data** — `view` is already an `atomic.Pointer[logView]`. What it holds is ORDER: append-to-inner then publish must not interleave with another mutator | **B** — it is already a serialization requirement wearing a mutex; an actor loop expresses it directly | YES — `inner.Append` under the lock, i.e. an **fsync** |
| 2 | `crashtest/child.go:115` — local `mu` | test-helper output | none | A | no |
| 3 | `disk/log.go:61` — `mu` RWMutex (read: full) | `sealed`, `active`, segment set, fork state | **YES** — a read must not observe a rotation half-done (active sealed, new not installed). A multi-object transition | **NEITHER** | **YES — RECURSIVE**: `Read` calls `l.parent.Read` under its own RLock, acquiring the parent's lock while holding the child's |
| 4 | `disk/store.go:19` — `mu` (inference) | `logs` map, `closed` | handle-set uniqueness: two opens of one dir must return ONE `*Log` | **NEITHER** (dedupe-on-open is a transaction) | YES — `Open` does file I/O under it |
| 5 | `form.go:614` — `MemFormLog.mu` (read: full) | `records [][]byte` | **NONE** — append-only slice, one writer, readers copy | **A** | no |
| 6 | `libretto.go:79` — `mu` (inference) | `sub`, `stop`, `done` | **YES** — start/stop lifecycle of a goroutine; the channels and the subscription must move together | **NEITHER** | YES — subscription teardown |
| 7 | `libretto_ledger.go:38` — `ledgerMu` | a 64-entry ring buffer + its index | **NONE** — a diagnostic ring | **A** or **B**; also a candidate for a lock-free ring | no |
| 8 | `log/log.go:62` — `wmu` (inference) | write path; `pend` is "mutated only under wmu; read lock-free" | ORDER of appends, same shape as row 1 | **B** | YES — disk writes |
| 9 | `log/log.go:63` — `fmu` (inference) | flush path | flush must not overlap flush | **B** | YES — fsync |
| 10 | `log/store.go:20` — `mu` (inference) | `logs`, `opening`, `closed` | handle dedupe + in-flight open coordination (`opening`) | **NEITHER** | YES |
| 11 | `mem_log.go:10` — `mu` (read: full) | `entries`, `byFigaroLT`, `nextLT` | **YES, BUT THINLY** — append must publish entry and index together. Two fields, one atomic swap away from being one value | **A** | no |
| 12 | `segment/cache.go:137` — `regMu` (read: full) | `registry map[Coord]*Segment` | **NONE** — a lookup table for the Evicted hook and the Recency oracle | **A** | no — and deliberately so: the Evicted hook takes NO lock, by the rule in `docs/store/tree.md` |
| 13 | `tree/cache.go:28` — `mu` (read: full) | `nodes`, all runs, `recomposes` | **YES** — hollowing, insertion and gap-materialization are a transaction over a node's run list | **NEITHER** | **YES, AND IT IS THE SHAPE THE PACKAGE'S OWN DOC WARNS ABOUT**: `rangeInNode` → `materializeLocked` → `fetch` → **`c.src(coord)`, a CALLER-SUPPLIED Source, invoked while holding `c.mu`** |
| 14 | `tree/tree.go:82` — `Budget.mu` (read: full) | `owners` set | **NONE** — a set of registered caches | **A** | no — deliberately: victims are collected under the lock and hollowed after, "so lock order cannot invert" |
| 15 | `xwal/index.go:30` — `mu` RWMutex (read: decl+comment) | `nodes`, `heads`, seqs | **YES** — a derived index whose maps must move together on a topology mutation | **A** (the whole index is a candidate for one published snapshot; `version` is already atomic) | no (inference) |
| 16 | `xwal/lastts.go:17` — `mu` (read: full) | `m map[string]*nodeTS` | **NONE for the counters** — each `nodeTS` is already atomics. The lock guards only MAP GROWTH | **A** | no |
| 17 | `xwal/store.go:49` — `mu` (inference) | `dirty`, `touch`, `lineageFails`, `lineageErr` | four maps mutated together by the flusher | **A** or **B** (a flusher is a loop already) | YES (inference — flush path) |
| 18 | `xwal/trunks.go:44` — `mu` RWMutex (inference) | `idx` and topology reads | **YES** — topology mutation is multi-object | **NEITHER** | YES |
| 19 | `xwal/trunks.go:53` — `hotMu` (inference) | `hot *trunkStore`, `retired` set | generation swap + retirement set | **A** (a generation is a published value) | YES (inference) |
| 20 | `xwal/trunks.go:62` — `validationMu` (inference) | `validationGeneration`, `validatedTopologyVersion`, `validatedForkBranches` | memoized validation state, three fields | **A** | no (inference) |
| 21 | `xwal/trunks.go:86` — `trunkRegistry.Mutex` | process-global `roots` map | **NONE** — a registry | **A** | no |
| 22 | `xwal/trunks.go:91` — `rootTopologyState.mu` + `sync.Cond` | `mutating`, `borrowers`, `epoch`, `owners`, `lineages` | **YES** — it is a CONDITION VARIABLE protocol: wait until no borrowers, then mutate | **NEITHER** — a Cond cannot be an immutable value | YES (wakes waiters) |
| 23 | `xwal/trunks.go:101` — `rootLineageState.mu` + `sync.Cond` | `writing`, `owner`, `heads` | same: writer-exclusion protocol with waiting | **NEITHER** | YES |
| 24 | `xwal/trunks.go:109` — `rootTopologyRegistry.Mutex` | process-global `states` map | **NONE** — a registry | **A** | no |
| 25 | `xwal/xwal.go:162` — `channel.mu` (inference) | one channel's `log`, `reduce`, state | append/rotate/fold on one channel are a transaction | **NEITHER** or **B** | YES — disk I/O |
| 26 | `xwal_backend.go:31` — `mu` (read: full) | `open`, `forms`, `metas`, `librettos`, `lastTS` — FIVE MAPS | handle-set uniqueness across five maps | **NEITHER** as written; **A** per-map if they were separate | **YES, AND IT DOES I/O**: `handleLocked` → `b.store.OpenNode` (opens a node, takes xwal locks) and `newWindowedLog` (reads segments) **under `b.mu`** |
| 27 | `xwal_backend.go:136` — `metaCache.mu` (read: full) | `loaded`, `value` | **NONE** — a memoized load; `loaded`+`value` are one value | **A** | YES — reads the meta file under the lock |
| 28 | `xwal_store.go:296` — `mu` (inference) | `trunks` and store-wide state | multi-object | **NEITHER** (inference) | YES |
| 29 | `xwal_store.go:302` — `observedMu` (read: full) | `observed map[string][]string` | **NONE** — a map of slices, replaced wholesale on change | **A** | no |
| 30 | `xwal_store.go:312` — `deleting` (read: full) | nothing; it serializes an OPERATION | **YES, AND IT IS EXPLICIT**: "a delete reads where the survivors are drawn, unlinks, and writes them somewhere that outlived it; two of those interleaved re-home an aria under a parent the other one is in the middle of taking" | **B** — it is a serialized loop expressed as a lock | YES |

---

## THE COUNT, AS A SHAPE RATHER THAN A RECOMMENDATION

    "NONE" to the standing test          10 of 30   (rows 2,5,7,12,14,16,21,24,27,29)
    A genuine transaction / protocol     13 of 30
    Order-of-operations wearing a mutex   4 of 30   (rows 1,8,9,30)
    CRITICAL SECTION CALLS OUT           17 of 30

TEN LOCKS GUARD SOMETHING THAT COULD BE ONE PUBLISHED VALUE. Seven of those
guard a MAP USED ONLY AS A LOOKUP TABLE — a registry, a set of owners, a memo.

## WHAT GLUCK PREDICTED, AND WHERE IT SHOWS

He predicted that tight adherence would reveal **poorly designed dependencies
or poorly followed rules** beneath seemingly legitimate locks. Asking "what
would have to be true for this lock NOT to be necessary?" of the rows that look
most legitimate:

**ROW 26, `XwalBackend.mu`, IS THE CLEAREST INSTANCE.** It looks legitimate: five
maps, handle uniqueness, obviously a transaction. But the reason the section is
LONG rather than short is that `handleLocked` **opens a node and reads segments
from disk while holding it**. For that lock to be unnecessary, a dependency
would have to stop handing us a thing that must be CONSTRUCTED under the lock
to be inserted: if opening produced a value first and insertion were a
compare-and-swap into a published map, the I/O would leave the critical section
entirely. THAT IS A DEPENDENCY SHAPE, NOT A LOCKING PROBLEM.

**ROW 13, `tree.Cache.mu`, IS THE SAME SHAPE ONE LAYER DOWN AND IT IS SHARPER
BECAUSE THE PACKAGE'S OWN DOC FORBIDS THE MIRROR IMAGE.** `docs/store/tree.md`
requires that an `Evicted` hook take no lock, because eviction can fire under a
consumer's lock and deadlock. But `materializeLocked` calls a **caller-supplied
`Source` while holding `c.mu`** — the same inversion, running the other way.
The rule is followed in one direction and not in the other. For this lock to be
shorter, the Source would have to be invoked OUTSIDE the section, with the
result installed by a second, short acquisition.

**ROWS 1, 8, 9 AND 30 ARE "POORLY FOLLOWED RULES" RATHER THAN BAD DEPENDENCIES.**
None of them guards data. Each serializes an OPERATION — append order, flush
exclusion, delete exclusion — and figaro already has `internal/actor` for
exactly that. `cached_log.go:51`'s own comment says so outright: it exists so
"cache updates land in log order", and it deliberately excludes readers.

**ROW 3 IS THE ONE I WOULD DEFEND AS WRITTEN**, and it is worth naming because
a survey that finds every lock removable has stopped reading: `disk.Log.mu`
guards a rotation, and a reader that observed the active segment sealed but its
successor not yet installed would see a hole. It is also **recursive across the
fork chain** — `Read` takes the child's RLock and then the parent's — which is a
real lock-ordering protocol, not an accident.

## WHAT THIS TABLE DOES NOT SAY

It does not say which cure to apply, because **A** and **B** point opposite ways
on concurrency and that choice is pending with Gluck. It does not order the
rows by value. It does not claim any lock is contended. And it does not treat
"NONE" as a verdict: a lock guarding one publishable value may still be the
cheapest correct thing in a section nobody reaches twice.
