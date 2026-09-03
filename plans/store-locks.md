# internal/store's LOCKS: A CLASSIFICATION

fd15d2a0, 2026-08-19, on @980dc16c's order. SURVEY, NOT SURGERY. Nothing is
removed, nothing is recommended, and no ordering is implied.

## THE LIMIT OF THE METHOD, STATED WITH THE FINDINGS AND NOT AFTER THEM

I ENUMERATED BY GREP AND CLASSIFIED BY READING. For every row I read the
declaration, its guarded fields and at least one lock site; for the rows marked
**(read: full)** I read every `Lock()` call site in the file. For rows marked
**(inference)** I read the declaration and its comment but NOT every caller, so
the "calls out" column is a lower bound there, I can prove a call-out exists,
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

| # | file:line, field | guards | invariant spanning the section that could NOT be one published value | cure | calls out? |
|---|---|---|---|---|---|
| 1 | `cached_log.go:51`, `writeMu` (read: full) | mutators only; readers never take it | **NONE for the data**, `view` is already an `atomic.Pointer[logView]`. What it holds is ORDER: append-to-inner then publish must not interleave with another mutator | **B**, it is already a serialization requirement wearing a mutex; an actor loop expresses it directly | YES, `inner.Append` under the lock, i.e. an **fsync** |
| 2 | `crashtest/child.go:115`, local `mu` | test-helper output | none | A | no |
| 3 | `disk/log.go:61`, `mu` RWMutex (read: full) | `sealed`, `active`, segment set, fork state | **YES**, a read must not observe a rotation half-done (active sealed, new not installed). A multi-object transition | **NEITHER** | **YES, RECURSIVE**: `Read` calls `l.parent.Read` under its own RLock, acquiring the parent's lock while holding the child's |
| 4 | `disk/store.go:19`, `mu` (inference) | `logs` map, `closed` | handle-set uniqueness: two opens of one dir must return ONE `*Log` | **NEITHER** (dedupe-on-open is a transaction) | YES, `Open` does file I/O under it |
| 5 | `form.go:614`, `MemFormLog.mu` (read: full) | `records [][]byte` | **NONE**, append-only slice, one writer, readers copy | **A** | no |
| 6 | `libretto.go:79`, `mu` (inference) | `sub`, `stop`, `done` | **YES**, start/stop lifecycle of a goroutine; the channels and the subscription must move together | **NEITHER** | YES, subscription teardown |
| 7 | `libretto_ledger.go:38`, `ledgerMu` | a 64-entry ring buffer + its index | **NONE**, a diagnostic ring | **A** or **B**; also a candidate for a lock-free ring | no |
| 8 | `log/log.go:62`, `wmu` (inference) | write path; `pend` is "mutated only under wmu; read lock-free" | ORDER of appends, same shape as row 1 | **B** | YES, disk writes |
| 9 | `log/log.go:63`, `fmu` (inference) | flush path | flush must not overlap flush | **B** | YES, fsync |
| 10 | `log/store.go:20`, `mu` (inference) | `logs`, `opening`, `closed` | handle dedupe + in-flight open coordination (`opening`) | **NEITHER** | YES |
| 11 | `mem_log.go:10`, `mu` (read: full) | `entries`, `byFigaroLT`, `nextLT` | **YES, BUT THINLY**, append must publish entry and index together. Two fields, one atomic swap away from being one value | **A** | no |
| 12 | `segment/cache.go:137`, `regMu` (read: full) | `registry map[Coord]*Segment` | **NONE**, a lookup table for the Evicted hook and the Recency oracle | **A** | no, and deliberately so: the Evicted hook takes NO lock, by the rule in `docs/store/tree.md` |
| 13 | `tree/cache.go:28`, `mu` (read: full) | `nodes`, all runs, `recomposes` | **YES**, hollowing, insertion and gap-materialization are a transaction over a node's run list | **NEITHER** | **YES, AND IT IS THE SHAPE THE PACKAGE'S OWN DOC WARNS ABOUT**: `rangeInNode` → `materializeLocked` → `fetch` → **`c.src(coord)`, a CALLER-SUPPLIED Source, invoked while holding `c.mu`** |
| 14 | `tree/tree.go:82`, `Budget.mu` (read: full) | `owners` set | **NONE**, a set of registered caches | **A** | no, deliberately: victims are collected under the lock and hollowed after, "so lock order cannot invert" |
| 15 | `xwal/index.go:30`, `mu` RWMutex (read: decl+comment) | `nodes`, `heads`, seqs | **YES**, a derived index whose maps must move together on a topology mutation | **A** (the whole index is a candidate for one published snapshot; `version` is already atomic) | no (inference) |
| 16 | `xwal/lastts.go:17`, `mu` (read: full) | `m map[string]*nodeTS` | **NONE for the counters**, each `nodeTS` is already atomics. The lock guards only MAP GROWTH | **A** | no |
| 17 | `xwal/store.go:49`, `mu` (inference) | `dirty`, `touch`, `lineageFails`, `lineageErr` | four maps mutated together by the flusher | **A** or **B** (a flusher is a loop already) | YES (inference, flush path) |
| 18 | `xwal/trunks.go:44`, `mu` RWMutex (inference) | `idx` and topology reads | **YES**, topology mutation is multi-object | **NEITHER** | YES |
| 19 | `xwal/trunks.go:53`, `hotMu` (inference) | `hot *trunkStore`, `retired` set | generation swap + retirement set | **A** (a generation is a published value) | YES (inference) |
| 20 | `xwal/trunks.go:62`, `validationMu` (inference) | `validationGeneration`, `validatedTopologyVersion`, `validatedForkBranches` | memoized validation state, three fields | **A** | no (inference) |
| 21 | `xwal/trunks.go:86`, `trunkRegistry.Mutex` | process-global `roots` map | **NONE**, a registry | **A** | no |
| 22 | `xwal/trunks.go:91`, `rootTopologyState.mu` + `sync.Cond` | `mutating`, `borrowers`, `epoch`, `owners`, `lineages` | **YES**, it is a CONDITION VARIABLE protocol: wait until no borrowers, then mutate | **NEITHER**, a Cond cannot be an immutable value | YES (wakes waiters) |
| 23 | `xwal/trunks.go:101`, `rootLineageState.mu` + `sync.Cond` | `writing`, `owner`, `heads` | same: writer-exclusion protocol with waiting | **NEITHER** | YES |
| 24 | `xwal/trunks.go:109`, `rootTopologyRegistry.Mutex` | process-global `states` map | **NONE**, a registry | **A** | no |
| 25 | `xwal/xwal.go:162`, `channel.mu` (inference) | one channel's `log`, `reduce`, state | append/rotate/fold on one channel are a transaction | **NEITHER** or **B** | YES, disk I/O |
| 26 | `xwal_backend.go:31`, `mu` (read: full) | `open`, `forms`, `metas`, `librettos`, `lastTS`, FIVE MAPS | handle-set uniqueness across five maps | **NEITHER** as written; **A** per-map if they were separate | **YES, AND IT DOES I/O**: `handleLocked` → `b.store.OpenNode` (opens a node, takes xwal locks) and `newWindowedLog` (reads segments) **under `b.mu`** |
| 27 | `xwal_backend.go:136`, `metaCache.mu` (read: full) | `loaded`, `value` | **NONE**, a memoized load; `loaded`+`value` are one value | **A** | YES, reads the meta file under the lock |
| 28 | `xwal_store.go:296`, `mu` (inference) | `trunks` and store-wide state | multi-object | **NEITHER** (inference) | YES |
| 29 | `xwal_store.go:302`, `observedMu` (read: full) | `observed map[string][]string` | **NONE**, a map of slices, replaced wholesale on change | **A** | no |
| 30 | `xwal_store.go:312`, `deleting` (read: full) | nothing; it serializes an OPERATION | **YES, AND IT IS EXPLICIT**: "a delete reads where the survivors are drawn, unlinks, and writes them somewhere that outlived it; two of those interleaved re-home an aria under a parent the other one is in the middle of taking" | **B**, it is a serialized loop expressed as a lock | YES |

---

## THE COUNT, AS A SHAPE RATHER THAN A RECOMMENDATION

    "NONE" to the standing test          10 of 30   (rows 2,5,7,12,14,16,21,24,27,29)
    A genuine transaction / protocol     13 of 30
    Order-of-operations wearing a mutex   4 of 30   (rows 1,8,9,30)
    CRITICAL SECTION CALLS OUT           17 of 30

TEN LOCKS GUARD SOMETHING THAT COULD BE ONE PUBLISHED VALUE. Seven of those
guard a MAP USED ONLY AS A LOOKUP TABLE, a registry, a set of owners, a memo.

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
`Source` while holding `c.mu`**, the same inversion, running the other way.
The rule is followed in one direction and not in the other. For this lock to be
shorter, the Source would have to be invoked OUTSIDE the section, with the
result installed by a second, short acquisition.

**ROWS 1, 8, 9 AND 30 ARE "POORLY FOLLOWED RULES" RATHER THAN BAD DEPENDENCIES.**
None of them guards data. Each serializes an OPERATION, append order, flush
exclusion, delete exclusion, and figaro already has `internal/actor` for
exactly that. `cached_log.go:51`'s own comment says so outright: it exists so
"cache updates land in log order", and it deliberately excludes readers.

**ROW 3 IS THE ONE I WOULD DEFEND AS WRITTEN**, and it is worth naming because
a survey that finds every lock removable has stopped reading: `disk.Log.mu`
guards a rotation, and a reader that observed the active segment sealed but its
successor not yet installed would see a hole. It is also **recursive across the
fork chain**, `Read` takes the child's RLock and then the parent's, which is a
real lock-ordering protocol, not an accident.

## WHAT THIS TABLE DOES NOT SAY

It does not say which cure to apply, because **A** and **B** point opposite ways
on concurrency and that choice is pending with Gluck. It does not order the
rows by value. It does not claim any lock is contended. And it does not treat
"NONE" as a verdict: a lock guarding one publishable value may still be the
cheapest correct thing in a section nobody reaches twice.

---

# WHAT HAS BEEN CURED SINCE THE SURVEY (2026-08-19, dec6ef8a and fd15d2a0)

The survey stands as written; this is the ledger against it. Every cure below
is PUBLISH, not delete, and each kept whatever half of its exclusion was real.

    row 5   MemFormLog.mu     CURED (ddc030ad, fd15d2a0). And the finding that
                              frames the rest: THE MUTEX WAS TWO EXCLUSIONS
                              WEARING ONE NAME -- writer/writer dead (one
                              drainer), reader/writer REAL (patchesFromLog runs
                              anywhere). The contract now reports itself
                              breaking: a second concurrent writer is refused
                              with an error that names it.
    row 14  Budget.mu         CURED (5fbd307b). It was taken ON THE EVICTION
                              PATH to read a set that changes only at
                              construction. Published; ownersMu serializes
                              registration. Canary: without it, 14 of 16
                              registrations survive.
    row 27  metaCache.mu      CURED for readers (e9f12a5b). It held a lock
                              ACROSS FILE I/O on the path a listing walks for
                              every aria. The WRITE lock stays: file-then-memo
                              order cannot be published.
    row 29  observedMu        CURED (e9f12a5b). Read on every IR append,
                              published whole; declarers still serialize.
    row 12  segment regMu     IN FLIGHT (fd15d2a0), inside the larger deletion
                              of segment/cache.go's duplicate residency
                              structure.

NOT IN THE TABLE, BUT THE SAME CAMPAIGN: tree.Cache's c.mu (63902f44). Reads
take no lock at all; writers hold it only to publish, never across the Source,
the budget's eviction pass, or the Evicted hook. Interleaved A/B: parallel
Range 2.213us -> 1.242us, and the parallel number now sits at the serial one
instead of double it.

## THE PATTERN THE CURES SHARE, WORTH MORE THAN THE COUNT

In every case the lock was doing TWO jobs and exactly one of them was real. The
question that separated them each time was not "is this lock necessary" but:

    WHO IS EXCLUDED FROM WHOM, AND WHICH OF THOSE PAIRS ACTUALLY EXISTS?

reader/writer was dead weight four times out of four, because the guarded thing
was one value that could be published. writer/writer was real three times out
of four -- ordering a file write against a memo, not losing a sibling from a
copied map, keeping two appends in log order -- and A PUBLISH CANNOT EXPRESS
ORDER BETWEEN TWO WRITERS. That is the boundary between cure A and cure B, and
it is legible per lock rather than per package.

## AND THE COUNT, WHICH IS THE LEAST INTERESTING PART

    83 -> 82 non-test mutex declarations; 31 -> 29 in internal/store.

Two removed outright, four moved off a read path, and one hot path (segment's)
in flight. THE COUNT IS NOT THE GOAL: a mutex nobody contends costs a cache
line, and a mutex on a path every reader takes costs the shape of the whole
design -- which is what kept two cache shapes alive in this stack for a month.

# THE TWO LOCKS THIS CAMPAIGN ITSELF ADDED (ede92072, 2026-08-20)

The standing goal is to reduce the mutexes, and the count has moved 83 -> 81 in
the whole campaign. Both of the locks the campaign's own new code introduced
are audited here against the standing test -- WHAT INVARIANT SPANS THE CRITICAL
SECTION THAT COULD NOT BE PUBLISHED AS ONE IMMUTABLE VALUE?

## 1. translatorEncoders.mu -- PUBLISHED (and the COUNT DOES NOT MOVE)

It guarded a map WRITTEN ONCE PER PROVIDER AND READ ON EVERY FIG IR APPEND.
That is the standing test's own named suspect: "a lock protecting a map that is
written once and read forever". The answer to the question is "none", so the
cure is the one this campaign has used before -- PUBLISH, don't delete: readers
load an immutable map through an atomic pointer, writers still exclude each
other because add() is read-modify-write.

    TestReadingTheEncoderSetTakesNoLock holds the WRITER's lock and serves a
    read from another goroutine. Canary: put writeMu back around get() and it
    goes red by timeout in 3.00s.

AND THE DECLARATION COUNT IS 81 BEFORE AND 81 AFTER, because an RWMutex became
a Mutex. I published "80" before checking. What happened is the campaign's own
finding again -- THE MUTEX WAS TWO EXCLUSIONS WEARING ONE NAME -- and only the
reader/writer half, the half on the hot path, is gone.

    A GOAL COUNTED IN DECLARATIONS CANNOT SEE THE CHANGE THAT MATTERS HERE.
    "83 -> 81" measures how many locks exist, not how many are taken on a read
    path, and those are different questions. The second is the one the standing
    order is actually about ("be on the lookout for spurious locking, even
    where it appears valuable"), and nothing in the tree reports it.

## 2. OnAppend.mu -- RAISED, NOT TOUCHED

RAISED under the escalation rule ("any lock found to have genuinely concurrent
callers is raised to Gluck, not worked around"), because the cure depends on a
contract I should not assume.

WHAT IT IS: ONE process-wide mutex on the OnAppend adapter, guarding a
map[ariaID]*Deriver. Two properties, both worth his eye:

  a. IT IS HELD ACROSS THE DERIVATION, not just the map lookup. So every
     aria's translation serializes against every other aria's, on one lock, on
     the fig IR write path.

  b. IT IS HELD ACROSS CALLS OUT OF THE PACKAGE -- o.translator(ariaID),
     trans.PeekTail(), and d.SeedAt(source, watermark) which does a log
     Lookup. That is the deadlock shape this campaign already documented: "a
     lock whose critical section calls out into a hook, a callback or an
     interface method".

WHAT WOULD FIX IT, AND WHY IT IS NOT MINE TO DO: the per-aria Deriver needs no
lock at all IF appends for one aria are serialized -- and they appear to be,
because figIRLog (the door) is PER ARIA and carries its own mutex. Then the
map lock need only cover fetch-or-create, and the derivation runs outside it.

    THAT IS THE CONCURRENCY-DOMAIN QUESTION THIS FILE ALREADY NAMES: the cure
    is to STATE THE CONTRACT -- one writer per aria, concurrent readers --
    ASSERTED WHERE IT CAN FAIL rather than commented. Asserting it is the work;
    assuming it is the defect.

I have not measured what (a) costs. The honest quantity would be appends
serialized per second across N concurrent arias, and no instrument in the tree
reports it.

# THE INSTRUMENT THE STANDING GOAL WAS MISSING (ede92072, 2026-08-20)

The goal has been measured by a count of DECLARATIONS -- 83 then, 81 now. That
counts how many locks EXIST. The order is about something else: "spurious
locking, even where it appears valuable", which is a question about locks
TAKEN ON A READ PATH, and nothing in the tree answered it.

scripts/lockpaths.sh answers it, statically, over the existing callpath tool.
A tool and not a list, for callpath's own stated reason.

## WHAT IT SAYS TODAY, AND IT IS NOT WHAT THE CACHE WORK IMPLIED

Every decoded read reaches a mutex, and the tree cache is not the one:

    Read (translations)   lineage.go:9, xwal_store.go:393, xwal_log.go:82,
                          xwal_backend.go:300
    Read (fig IR)         lineage.go:9, xwal_store.go:393, xwal_log.go:82,
                          xwal_backend.go:229
    ReadFrom, Lookup      the same four
    PeekTail              xwal_store.go:393, xwal_log.go:82

TWO SITES ACCOUNT FOR ALL OF IT, and both are the store's own `s.mu`:

  XwalStore.Lineage (lineage.go:9) takes s.mu to call trunks.ListLight().
      treeLog.refs() calls it ON EVERY peek, so EVERY READ that consults the
      lineage takes a process-wide lock -- to obtain an answer that changes
      only when an aria is forked or created.

  XwalStore.openNode (xwal_store.go:393) takes s.mu for the whole open, and
      xwalLog.openOnce calls it PER READ -- so every substrate read of every
      channel of every aria queues on one lock.

    SO "A HIT TAKES NO LOCK" IS TRUE OF tree.Cache AND FALSE OF THE READ. The
    campaign removed the lock from the cache and left two process-wide locks
    on the path that reaches it, which no per-layer measurement would show --
    the same shape as "a per-layer benefit test cannot measure a cost that
    exists only BETWEEN layers".

## WHAT I HAVE NOT DONE

Neither is touched. Both are `s.mu` with genuinely concurrent callers, which
the escalation rule reserves for Gluck, and the lineage one has the shape the
standing test names as curable -- an answer that changes rarely, recomputed
under a lock on every read, i.e. a candidate for publishing rather than
locking.

AND THE NUMBER TO BRING HIM IS NOT MEASURED: how long a read waits on s.mu
under N concurrent arias. The static path says the lock IS taken; it says
nothing about contention, and this file has been burned before by treating one
as the other.
