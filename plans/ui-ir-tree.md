# The UI IR tree: one turn store shaped like the trunk forest

Design doc per Gluck (2026-08-14 ~12:00): organize the API, remove the
legacy open path, and build the proper caching UI IR layer AS A TREE --
the same shape the backend already has (fig IR is a prefix tree on
disk; the segment cache is the eviction discipline one layer down).
Prescription-style: nothing here is built; the storm's verdict on
S1-S3 (plans/storm-triage.md) gates the start.

## What exists (and what is wrong with it)

Two FLAT-LIST owners of composed turns: each agent's aria.Server holds
[]Turn for its aria; the reader registry holds another per read aria.
Forks share NOTHING: N branches of one trunk pay N composed copies of
the common prefix. Boot/hydrate materialize the WHOLE log once (the
"legacy open path"), then the turn cache evicts back down -- work done
to be thrown away.

## The shape

ONE process-wide TurnStore, a tree congruent with the xwal topology:

- NODES mirror trunk nodes: a fork's node holds only its DIVERGENT
  suffix of composed turns; the shared prefix lives in (and is served
  from) the ancestor's node. Resolution walks the lineage exactly as
  xwal path resolution does.
- RUNS are the storage and eviction unit inside a node: a contiguous
  span of composed turns with {firstTurn, lastTurn, LT bracket, bytes}
  index that SURVIVES eviction -- the segment-cache block discipline,
  one layer up. Budget stays ui_window_mb, global, LRU per run.
- RECOMPOSE-ON-MISS per run from the node's LT range (formdelta.Seed
  from the record preceding the range, as today). Legacy unbracketed
  turns pin their RUN, not the world (this is also S1's prescription).
- THE LEGACY OPEN PATH DIES: no AdoptIfEmpty/whole-log compose. First
  read materializes the anchored run only; boot cost goes O(history) ->
  O(page). The agent's open region (streaming, versions, delta wire)
  stays exactly where it is -- it was never the cache's business.

## The API organization it forces (phase 10's seam)

    type TurnStore interface {
        Page(lineage, at, dir, budget) Page     // windowed, tree-aware
        Tail(lineage) *Turn                     // pinned, for the agent
        Commit(lineage, Turn)                   // seal path
        Release(lineage)                        // teardown accounting
    }

Consumed by BOTH the agent server (which keeps only the open region and
the subscriber fanout) and the reader (which keeps nothing). One read
path, one storage owner, and agent.go's mu shrinks toward the lock
audit's phase-10 target because the agent stops owning turn storage.

## Interactions and hazards, pre-declared

- Fork-time invalidation: a parent's suffix can grow after a fork took
  its base; run boundaries must respect each child's fork base LT (the
  xwal already stores it; use ITS number, do not re-derive).
- The client fold refactor (heldInquiry -> turn header) should land
  WITH this, not after: the wire's TurnPart already carries whole
  turns, and the tree changes only who assembles them.
- doctor mem: runs resident / of budget / recomposes / shared-prefix
  hit ratio -- the new number that proves the tree earns its keep.
- Measure fork-heavy stores before/after: the win is proportional to
  branch count; a store with no forks must show ZERO regression.

## Gate

Do not start until the storm convicts/acquits S1-S3: building a tree
on top of a latch that pins whole caches would inherit the bug with
more geometry. S1's fix (per-run pin, bracket at seal, count what you
decline to evict) is a PREREQUISITE commit, not a tree feature.
