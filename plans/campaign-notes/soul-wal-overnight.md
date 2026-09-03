# soul-wal overnight run: 2026-07-23

## MORNING SUMMARY (read this first)

**Installed on this box: figaro v0.5.0** (main 5442017 + local tag). The
turn journal is gone; durability is per-message IR appends + drain-on-
shutdown + tail repair. Migration gate deleted (your bricked store works),
caches degrade instead of failing sends, doctor gc shipped, SIGTERM drain
verified live. Adversarially reviewed (14-agent workflow): 8 findings, all
fixed and re-verified. Daemon restarted; smoke green. NOT pushed to origin
(main + v0.5.0 await your push).

**Figwal memory-first: DONE, v0.8.1 cut at d62c789.** 26 commits,
+5160/−727 over 53 files. 17 defects found and fixed across the
campaign (4 pre-tag, 12 adversarial-review findings, 1 final-spot-check
cut-bound bug). Full -race green; seed corpus 31/32/33/52/54-60 zero
violations; ~40 mid-fork SIGKILL windows clean; the crash harness is
adopted into figwal as a first-party regression suite (d62c789).
Original state below for the record:

**(as of mid-run)** Branch
memory-first (~/dev/figwal-core, 15 commits, +2714/−714): OpenStore +
flock single-writer, memory-first appends with MaxUnflushedBytes
backpressure (the OOM class is structurally dead), lineage-coherent
flush cuts, .unclean-gated open repair, explicit-fork-only, auto-create
channels, idle eviction, flush-failure poisoning, old-manifest compat.
Crash campaign: coherent cuts held over 808 SIGKILL cycles (zero
orphans; durability bound met exactly; Close lossless). Fork commit was
the last non-atomic spot (inherited from e2f5c89), two-phase-commit fix
landed (9afd678); seed replays running now, tag v0.8.0 cuts when green.
Sealed-segment BYTE corruption (external damage) is documented out of
open-repair scope → future offline fsck.

**Figaro on memory-first: ready, final.** g2-collapse suite green
against v0.8.1 (pin worktree ~/dev/figwal-g2pin at d62c789). All 12
review findings verified FIXED (the one PARTIAL closed by d673ed3).

**(as of mid-run)** Branch g2-collapse (11 commits over
main, net −839 incl. the ship work): store layer collapsed onto
OpenStore, SyncTranslation deleted (flusher owns durability; user tics
Kick), full -race green, real-store-copy verified (open+fold+append),
live e2e green (send/drain/resume). Pinned to figwal via local replace,
swap to the tag after figwal pushes.

**Loss contract now in force** (the deliberate change): cooperative
shutdown loses nothing; kill -9 loses ≤1s flush lag + the in-flight
partial stream. User prompts get expedited flushes.

**Do at your leisure:** push figaro main + v0.5.0; push figwal
memory-first + v0.8.1 (v0.8.0 is tainted, superseded); review + merge
g2-collapse, then repin go.mod from the local replace to the pushed
v0.8.1; exit/rebuild your share-hush devshell (its shim figaro is a
stale footer-branch build). Backup of the pre-ship store: scratchpad
arias-backup-preship. Deferred with eyes open: offline fsck for
sealed-segment byte corruption (M3/M4), stump-level eviction, vestigial
in-process multi-peer machinery decruft, IdleUnload zero-value
semantics (0=default-5m, negative=never).

---

## Wave 0 (done)
- T0 landed on figaro main 4ccbd0f, installed (nix profile): migration gate
  deleted (−745/+19), providers degrade to uncached, verified against a copy
  of the real store, 942fb59e and e6e77a26 open cleanly.
- W1 API freeze: figwal memory-first branch, the memory-first design doc in
  figwal-core (e37f70c) -- a file of THAT repo, never of this one.

## Wave 1 (running)
- A1 figwal core (~/dev/figwal-core, memory-first): flock/OpenStore committed
  (29e80fd); flusher/cuts/repair/auto-create in progress.
- A2 crash harness (~/dev/figwal-harness, crash-harness): harness committed
  (eec178b); long-run findings vs e2f5c89 pending → crashtest/FINDINGS.md.
- A3 journal excision (~/dev/figaro-qua/soul-wal): journal deleted (a8782ff),
  drain (0bc7bd1), tail repair (1d3a26b); ported scenario tests uncommitted.
- A4 cache hygiene: DONE (5150cc3, +364/−23, -race green). Shared
  valid-block predicate gates IR decode + cache in anthropic and
  anthropicsdk; also fixed tool_use-without-input and anthropicsdk
  present-but-empty variants; copilot clean (replays server bytes).
  Parked for Wave 3 merge into soul-wal.

## Ship track (workday gate)
- soul-wal = A3 + A4 merged (64c4c1f); full -race green; real-store-copy
  probe green (dead turn-wal harmless).
- E2E on isolated daemon (soul-wal binary): fresh turn E2E-OK; stop
  mid-tool → drain sealed partial assistant + honest interrupted tool
  result; both arias resume post-restart (RESUMED-OK, no cache poison).
- SHIPPED: main ff-merged to soul-wal + doctor-gc (b8c126f), installed
  (profile pinned to explicit rev, the ?ref=main fetch had served a stale
  tree), old daemon stopped at zero active turns, real store backed up to
  scratchpad (arias-backup-preship) then GC'd, new daemon spawned from the
  profile binary, live smoke SHIP-OK.
- CAVEAT for the user: shells inside the share-hush devshell resolve
  figaro/fig to a stale shim (/run/user/1000/figaro-dev-share-hush/bin,
  stamps 733f19e, built from the footer worktree pre-rebase). The daemon
  is the ship build, so shim CLIs still talk to ship logic over RPC, but
  rebuild/exit the devshell to get the ship CLI locally.
- Ship-gate review COMPLETE: 8 confirmed findings (2 HIGH), all fixed in
  hot-patch 5442017, installed + daemon restarted (live smoke PATCH-OK):
  (1) SIGTERM drain wiring was dead, daemon main now waits for Shutdown,
  socket removed post-drain; drain re-verified live (mid-tool stop seals
  honestly). (2) cache predicate split render vs wire-replay, signed
  empty-summary thinking blocks persist again (400-poison regression from
  the hygiene pass), unparsed tool_use input => whole message uncacheable.
  (3) interrupted completed tools seal full results w/ truncation marker
  fallback. (4) Registry.Kill unpublishes after final seal (restore race).
  (5) nil-args invokes get seal coverage. Docs de-journaled.
- A2 campaign verdict on the SHIPPED pin: append/sync path crash-solid
  (~1400 kill cycles, zero lost appends); fork-commit atomicity + torn
  multi-segment tails are pre-existing gaps deferred to memory-first.

## Incidents
- Hosts deaths 2-3: kernel OOM, figwal log.test at 42GB RSS in the session's
  oom.group scope. Diagnosed: TestCachedConcurrentReaders (contributor's test,
  stop-condition scales with log size) x A1's unthrottled memory-first Write
  (no backpressure; pending/snapshot grow at memory speed). Fix directed:
  MaxUnflushedBytes backpressure in the contract + inline FlushTo over the cap.
  All test runs now scope-capped (MemoryMax=4-8G) so kills stay contained.
- Host process exited mid-Wave-1; no agent work lost (worktrees intact). All
  four resumed from transcripts. A2's first findings report was lost with the
  session; re-running the long mode.

## Cook track progress
- A1 memory-first core COMPLETE (figwal-core memory-first, 8 commits,
  +1692/−654, -race green): OpenStore+flock, memory-first appends with
  MaxUnflushedBytes backpressure, coherent cuts + .unclean-gated repair,
  explicit-Fork-only, auto-create channels (manifest kept, justified),
  sync modes + EnsureChannel surface deleted. Deviations + 6 open risks
  documented in its report; now doing A5 (idle eviction + flush-error
  surfacing).
- A2 rebinding harness to memory-first, re-running campaign vs the
  e2f5c89 baseline findings (K1-K3 fork atomicity, C1-C5 torn tails)
  plus A1's declared risks.
- G2 store-layer collapse DONE on branch g2-collapse (486ce20, net −31,
  full -race green vs figwal pinned at a884c8a via local replace;
  worktree ~/dev/figwal-g2pin). SyncTranslation→Kick, user tics Kick,
  auto-created translation channels. Final pin awaits figwal tag.
- figaro v0.5.0 tag cut locally at 5442017 (NOT pushed).
- A1 follow-ons landed (tip d4e7f10): idle-lineage eviction (flush-then-
  unload, refcounted heads, RSS tracks working set) + flush-error
  poisoning (3 consecutive failures gate Append, reads stay up).
- A2 campaign vs memory-first: coherent cuts CONFIRMED (zero orphans in
  808 kill cycles, head-cut class gone, durability bound met, Close-drain
  lossless, flush-error recovery clean). STILL BROKEN: fork commit not
  crash-atomic (M1 trunk-id loss, M2 .fork-pending bricks open, same as
  e2f5c89); sealed-segment byte corruption (M3/M4) out of open-repair
  scope. A1 now on the fork two-phase-commit fix; M3/M4 rescoped to an
  offline fsck (documented, deferred).
- figwal compat fix 2682f6b: reducer resolution falls back to channel
  name, pre-OpenStore manifests (jsonmerge) open cleanly; proven against
  a real-store copy including appends.
- G2 live e2e on memory-first engine: G2-OK, drain seal honest, resume
  G2-RESUMED, old-format store through the compat path.
- FINAL REVIEW round: 12 confirmed findings fixed as 5 figwal roots
  (5e4607a lifecycle flusher + per-lineage poison, ecb42ac one-ahead
  drain, c19ae0f fork rollback, 4707ec4 Store.Clear, 9575fbc no-retire-
  on-failing-flush) + figaro seams (66c219d, be703db). Fix-verification:
  11/12 FIXED, 1 PARTIAL (stale in-memory readOnly on a raw handle after
  fork abort, unreachable from figaro; final polish in flight). Gate
  corpus: 10+ consecutive seeds green, zero violations.
- Fork two-phase-commit fix (9afd678) verification found TWO more real
  bugs, both fixed: 3f33a2f stray-flush sweep could put a related record
  ahead of main at runtime; 1014cb6 rotation-crash left an empty active
  segment whose watermark header wasn't reseeded at open, swallowed the
  next reducible record (seed 55 durable-loss). Seed 55 replay now green;
  full re-batch + independent 31/32/33 replay running; tag gates on both.

## Queued
- Wave 2 after A1: A5 eviction, A6 figwal decruft + tag.
- Wave 3: G2 store-layer collapse (lead), H1 doctor GC, H2 simplify.
- Wave 4: -race both repos, isolated e2e, adversarial self-review workflow.
