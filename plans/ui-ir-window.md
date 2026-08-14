# The UI IR window: one read path, bounded, uniform with the other caches

Design outline for the follow-up Gluck pinned on 2026-08-13, to land
after the form-deltas work merges. Written during that work's closing
gate; nothing here is built.

## The finding it answers

`aria.Server` is two organs wearing one name:

1. **Irreducible**: the open streaming region and the delta/version wire
   (NodeDelta splices against the previous frame, version counters,
   OnDesync). Mid-turn state is not durable yet, and a diff protocol
   needs memory of the last frame. No stateless reader can serve either.
2. **A cache pretending to be an organ**: the sealed-turns section is
   memoized compose output — unbounded, resident for the agent's life
   (~6 MB at bench size, a suspect slice of the live daemon's 88 MiB of
   unnamed heap). The `AriaReader` is the other extreme: O(whole
   history) per page, 13.1 MB/op thrown away at 10k messages.

## The shape

ONE windowed component — call it the **turn cache** — shared by the
agent and the reader:

```
turnCache
  ├─ window: sealed turns, materialized, byte-budgeted, LRU by access
  ├─ miss:   recompose the requested RANGE from the log (fig IR is the
  │          only truth; UI IR is a pure derivation, so eviction is
  │          always safe and there is no on-disk store)
  └─ (agent only) open region + delta/version state, PINNED — the open
     turn is never evictable
```

- The agent's `aria.Server` keeps its streaming half and delegates the
  sealed section to the turn cache.
- The `AriaReader` becomes the same component with no open region — the
  duplicate read path (the one that produced the delta-liveness bug) is
  gone by construction.

## Eviction policy, uniform with the rest of `[memory]`

The house pattern (IRWindowMB / TranslationWindowMB / SegmentCacheMB):
a **byte budget in MiB**, per axis, LRU on access, evicted at insert
when the budget is crossed and on the EXISTING idle sweep — no new
clock. The four-idle-clocks lesson stands: this rides
`sweep_interval_seconds`, it does not bring its own timer.

```toml
[memory]
# UIWindowMB bounds resident COMPOSED UI IR (sealed turns) across every
# materialized aria, in mebibytes. The composed form runs ~1.3-2x its
# decoded IR (measure before shipping; the number goes here). A turn
# evicted costs one range recompose on the next read that lands in it
# (~4.2ms per 800 turns today, less for a range). 0 is unbounded, which
# is the behaviour figaro has always had.
ui_window_mb = 8
```

Sizing: `EncodedBytes` already rides every entry for exactly this kind
of accounting; the composed estimate follows the same pattern (estimate
at insert, times a measured inflation factor — do not sum
reflect-walked struct sizes, that lied 3x low once already).

## The two independences Gluck asked for

1. **fig IR and UI IR evict independently.** They already live in
   different layers (cachedLog window vs turn cache); the rule to
   enforce: composing a range reads decoded IR TRANSIENTLY through
   cachedLog and retains nothing — the turn cache must never hold a
   reference into the decoded entries it composed from (copy the
   strings it keeps, or the UI window silently pins the IR window).
2. **fig IR eviction is independent of agent liveness.** Already true
   (segment cache + cachedLog operate below the agent) and verified
   during the delta work; the turn cache must preserve it: a live agent
   pins ONLY its open region.

## Range recompose without the full walk

- Turn ids are PERSISTED on records (`appendMsg` stamps every durable
  write), so a range ask maps to an LT span via `ReadPage` — no whole-log
  walk. `StampIDs` stays deterministic for the ranges it re-derives.
- Legacy arias whose records carry no turn ids (`derivedIDs`) fall back
  to the full walk, once, and may cache the derived id index; that is
  the same degradation `show` already implements.
- Form deltas: `formdelta.PerRecordFrom` with a `Seed` from the record
  preceding the range — built for `aria.read` and reused as-is. The
  delta cost is bounded by the same window for free.

## The observability rule

A new resident structure arrives WITH its number in `doctor mem`
(earned twice before this project stopped forgetting it):

```
ui cache   resident-turns=N  resident=X MiB of Y  recomposes=K
```

## Order of work

1. Extract the sealed-turn store out of `aria.Server` behind an
   interface the server and reader both consume (no behavior change,
   the seam only).
2. Bound it: budget, LRU, recompose-on-miss; `ui_window_mb`; doctor mem.
3. Point `AriaReader` at it (deletes the reader's per-call recompose).
4. Measure: DaemonDay/ListingCost probes, idlemem.sh, the fleet — and
   the 88 MiB attribution from the FIGARO_PPROF=1 heap profile, which
   should name this structure if the suspicion is right.

## What will bite (pre-declared)

- The pager's anchor arithmetic assumes turns are addressable even when
  not resident: `Read`/`ReadBefore` must transparently recompose below
  the window, or scroll-up dies at the boundary.
- The live delta wire's version counters must survive eviction of the
  sealed section (they belong to the open region's state, but verify —
  a close frame quotes a record version).
- Two clients paging disjoint ranges of one big aria will thrash a
  small window; LRU by range, not whole-aria, is the mitigation.
- The just-sealed turn should enter the window already composed (the
  stream built it); do not evict-then-recompose the thing most likely
  to be read next.

## Post-merge tasks (Gluck, 2026-08-13 evening — DO AFTER MERGE)

1. **Tune his real config.toml**: set `ui_window_mb = 16` explicitly
   (the binary default, duplicated so the setting is visible), AND add
   the other in-binary [memory] defaults to the real config.toml for the
   same reason. Test that tuning each actually changes behaviour
   (doctor mem is the instrument).
2. **Record working set before/after on the SAME load** — the live
   daemon's PSS under his normal session, old binary vs merged, plus
   doctor mem's ui window line. The 179MB → ? number is the deliverable.
3. **After this and the CLI refactor**: design how to PREDICT, MEASURE
   and EVALUATE trade-offs as a practice — performance, memory, CPU
   thrashing, disk — rather than per-change improvisation.

## TODO for a later generation (Gluck's design note)

The turn UI is a TREE (forks), but every aria.Server holds a flat
[]Turn: forked arias share no composed prefix, so N branches of one
trunk pay N copies of the common history. Consider structuring the
composed UI IR as an IN-MEMORY PREFIX XWAL, the same shape the fig IR
uses on disk — branches share the prefix, the window bounds the union,
and fork cost drops to the divergent suffix. Almost certainly another
generation's work; recorded so it is not re-discovered.
