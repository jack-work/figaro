# Copy-on-write xwal topology

Lane `lane/cow-topology` from main 0cb70edb (v0.52.3 + gc). Ships with gc.

## Today

Four runtime operations take the store-wide topology mutation
(`beginTopologyMutation`: wait up to 3 s until no aria in the root holds a
head, block every new `Head` while it runs, then retire every hot store):

| caller | file | what it changes |
|---|---|---|
| `Trunks.remove` (RemoveAria, the reaper's purge) | remove.go:19 | unlinks a subtree's dirs in every channel, full `rebuild` |
| `RemoveStump` (outfit stumps) | stumps.go:35 | unlinks one stump |
| `RemoveComponent` (after a move) | move.go:726 | plan, then markers and dirs |
| `Refresh` (escape hatch) | trunks.go:226 | re-reads disk |

Boot (`openTrunks`, `createTrunks`) also takes it, with nothing open yet.
Forks, grafts, stumps and detach use `beginFlatCreate` (`t.mu` plus a hot
flush; no wait on heads). Translator cut and clear are already fenced per
node (`cutChannel`, 0.52.3). `ForkAt` below a node's own base retires every
hot store (fork.go:192) without waiting.

The lock does three jobs at once: (1) the index is edited in place and
rebuilt from disk; (2) the shared hot store could flush into a dir after it
is unlinked; (3) cached log handles must close before the unlink.

## Design

1. **Index edits are atomic.** A removal drops its whole component from the
   index in one write-locked edit, so every reader call sees all of it or
   none. Spawns stay in-place inserts: copying the maps on every spawn would
   cost about 0.5 ms at 10k nodes, on the path every new aria takes.
2. **Logical delete.** Under `t.mu` (the lock forks already take, held
   for milliseconds): compute the component from the current snapshot, write
   the removal plan (`.remove-<token>/plan`, the format `RemoveComponent`
   and boot recovery already use), unlink the `.node` markers leaf first,
   fsync the main dir, then swap in a snapshot without the nodes and add
   them to a per-`Trunks` dead set. The plan is the commit point. No wait
   on any head.
3. **Writers fenced per node.** `borrowHotUntracked` refuses a key whose
   node or any ancestor is dead. No head of a dead node can be opened after
   the swap; heads already out keep reading and writing their own dirs,
   which still exist.
4. **Physical delete, deferred.** Heads that went private (a destructive
   edit through a head) are counted per node in the root state, beside the
   shared stores. A drainer waits until no head in any store
   (hot or retired) reads a dead node, then syncs and unloads those heads,
   `Forget`s the dirs in every store, unlinks the dirs in every channel,
   removes the plan and clears the dead set. When nothing is borrowed, the
   common case for the reaper, it runs inline before `Remove` returns. A
   removal held past a minute logs one Warn naming its nodes and heads;
   `figaro doctor mem` shows pending removals, their heads and the oldest.
5. **Crash recovery** is the existing `resumeGrafts`: at open, before the
   index is built, every `.remove-` plan is finished.
6. **Peers.** A delete bumps the root epoch without the mutating flag; an
   in-process peer rebuilds lazily, and its rebuild sees the markers gone.
7. **Refresh and boot keep the root mutation.** Refresh is a manual escape
   hatch and boot has nothing open; neither is on a turn's path.
8. **Reclaimer.** `RemoveAria` takes the delete set it was decided on and
   refuses (retryable) if the set computed at commit differs. Removals then
   batch freely; the pace becomes a tuning knob.

9. **Names come back.** A stump name is reused when an outfit version is
   minted again; creating a node whose key is still in a pending removal
   refuses until that removal's readers close, or the old drain would unlink
   the new node's directories.

## Invariants and the tests that defend them

**I1. A crash never leaves readable a node the logical step removed, nor
removes one it did not.** "Removed" means the plan is durable. Kill -9
points, each followed by reopen and checks (node absent from List, Head
refuses, no dir left in any channel, survivors byte-identical):
K1 before the plan's fsync (nothing removed, all readable); K2 after the
plan, before the first marker unlink; K3 between marker unlinks (child gone,
parent's marker still there); K4 markers gone, dirs present; K5 between
channels' dir unlinks; K6 dirs gone, plan present. The plan lists the exact
keys read under `t.mu` from the snapshot of the commit; a fork cannot
attach to a dying node because it takes `t.mu` and resolves its parent in
the new snapshot. Node ids are never reissued before a plan resumes: boot
resumes plans before minting, and the in-memory counter never decreases.
Test: `TestARemovalSurvivesAKillAtEveryStep` (subprocess killed at K1..K6
by a hook, reopened), plus a fork racing a delete.

**I2. Role, box and lifetime decisions read one consistent snapshot.**
Index edits are single swaps, so a listing never sees a child without its
parent or half a subtree. The reclaimer's delete commits only against the
delete set it decided on; a fork, move or restore in between refuses the
commit and the decision is retried from fresh state. Role targets are form
data, not topology: gc's full scan before the commit still guards them (the
old lock never covered form writes). Tests: concurrent listing during
deletes never sees a partial subtree; `RemoveAria` with a stale delete set
refuses; existing role re-check, box purge-grace and cross-sandbox-hold
tests.

**I3. A fork reading a parent's segments flat never loses them.** A node is
deleted only with its whole subtree (non-recursive refuses; survivors are
detached first by `RemoveLeaf`), and physical delete waits for every head
reading the node or a descendant, in every store. Test: a borrowed child
head scans through the parent while the parent's subtree is removed; the
scan finishes byte-identical, then the dirs go.

**I4. The earlier fixes stay green on top**: deleteNode and scalar over a
branch (api/form), `TestABoxUnsetLeavesTheSystemBranchWhole`, the translator
cut-scope tests (`TestARetiredStoreHoldingTheNodeCannotUndoACut`,
`TestATranslatorCutIsNotBlockedByAnotherAriasOpenScan`,
`TestAParentAndItsForkCutAtOnce`, `TestAParentCutWaitsForItsForksOpenScan`),
box purge grace and cross-sandbox hold. All of them in the `-race
-count=200` stress set, with internal/store/xwal and the reclaimer.

## Settled with El Inspector before coding

1. **Peers.** The dead set lives in the root's shared state, not in one
   `Trunks`, so every in-process peer's borrow checks it whatever its
   snapshot; and the drainer counts the heads of every peer's stores. Test:
   a peer with a stale snapshot borrows the node during the drain and is
   refused.
2. **Plan durability.** Before the first marker unlink: fsync the plan file,
   the `.remove-<token>` dir and the store root that holds it, in that
   order. K1 and K2 assert it.
3. **Moves.** `RemoveComponent` writes its plan only after the destination
   has acknowledged the import durably, as today. Kill point K0 sits between
   the ack and the source plan: on reopen the source still holds the
   component whole.
4. **No live agent is deleted.** `RemoveAria` kills a running agent and its
   sessions before the store delete; the TTL sweep and the reaper act only
   on dormant arias. Writers left on a dead node are readers or followers,
   never a turn. Test kept: a delete of an aria mid-turn stops the turn
   first.
5. **Cut and delete on one node.** A delete waits for any cut fence along
   the component before marking it dead, under the same lock the fences
   use; a cut that finds its node dead after taking its fence refuses. Test:
   both at once, either order.

## Proof

Benchmarks vs 0cb70edb (benchstat, n=10, raw in /var/tmp/perf/cow-topology):
DoorRoundTrip idle and during 200 removals (p50/p99/max); removal
throughput paced and batched; translator cut beside another aria's upload;
cold open; fork. Stress: the set above at `-race -count=200`; a concurrent
fuzz over many arias (fork, delete, cut, move, write, read) checking that
no record is lost or resurrected; the kill -9 test.
