# State layer: running progress

Live notes for whoever holds the role `@980dc16c`. Update this, not chat.

## SESSION 2 AT A GLANCE (aria c1d55d02)

Phases 6 and 8 closed, phase 3's wire half landed, one lock-audit item done,
and the memory question answered. Everything below is detailed later in the
file; this is the map.

**Shipped**

| | |
|---|---|
| phase 6 | the delete path buries what it takes: a death record before any unlink, a refused delete buries nothing |
| phase 8 | the presentation hierarchy IS a form; `internal/trunk` and `trunks.json` deleted (458 lines), promote is one atomic patch |
| phase 3 (wire) | a set answers `applied` / `unchanged` / `queued` with a version; the CLI stopped claiming writes it never made |
| lock audit #2 | `cachedLog` publishes an immutable snapshot; contended reads 11.45 ns to 1.30 ns |
| cold reads | `PatchesBetween` below the window went O(offset) to O(range): 142 us to 3.05 us, flat with offset |
| memory | translations measured then bounded; `figwal loaded-heads` reported; the OUTFIT column stopped re-opening a form per row (209 to 0) |

**Found, not fixed** — `fig ls` retains ~95 MB on a 515-aria store, none of
it in any cache figaro owns: figwal copies every payload of a channel into
memory when a log opens. See "WHERE THE MEMORY IS". The fix is
segment-granular lazy loading in figwal, and two candidate figaro-side
mitigations are written up with their trade-offs because both change
something a user can feel and both want Gluck's ruling.

**Three of my own claims died on their own measurements**, and each is left
in place as a falsification rather than deleted: translations as the missing
memory (they are 8% of the IR), a "+13% regression" that ten samples showed
was noise, and a version-addressed cold open that measured +338% and was
reverted in full.

**Bugs found while doing something else**: a stale topology decided delete
sets (so `fig kill -r` unlinked a fork figaro never listed); `byFK` was an
unbounded index living inside a bounded window; `resident_ir_bytes` counted
one of the two caches an open aria holds.


## HAZARD: two arias, one worktree

A reviewer fork checked `pr16` out **in this worktree** while I was working
in it, then switched back. For a minute `git log` showed a HEAD that was not
mine and `git status` showed files I had not touched; a `git checkout --` I
ran in that window restored files from the wrong branch. Nothing was lost
(every commit is in the reflog and on the branch) but the recovery cost real
time at 3am, and the failure mode is silent.

**Rule**: a fork that shares a cwd shares the INDEX and the WORKING TREE. If
you are spawned to review, read, or bisect anything, do it in your own
worktree:

```
git worktree add -f /var/tmp/<name> <ref>
```

`/var/tmp/figbase` exists for exactly this and is what every before/after
benchmark in this file was measured against.

## Standing setup

- **Worktree/branch**: `/home/gluck/dev/figaro-qua/incant`, `feat/incantations`.
  Treated as the bona fide source; nothing goes to main first.
- **Role**: `@980dc16c` (`name=state-layer-worker`), `target-aria` points at
  the current worker. Move it on handoff.
- **Heartbeat**: `figaro-state-layer.timer` (user, 10 min) runs
  `/var/tmp/figstate/tick.sh`, which reads context usage and messages the
  ROLE. Escalates: 85% mint a successor, 95% hand off. Log at
  `/var/tmp/figstate/heartbeat.log`.
- **Design of record**: `plans/durable-forms.md` (what and why),
  `plans/state-layer-implementation.md` (how, with code),
  `plans/lock-audit.md` (the two fast-follows).
  Answers: `plans/answers-forms.md`, `plans/wym.md`.
- **Questions**: `~/notes/figaro/form-work/`.

## Rulings in force

- Librettos: **whole-form only**, no projection. One per studied FORM, named
  `@libretto::<formid>` (deterministic from the source id), shared,
  refcounted, does **not** fork. API-level derived forms keep projection.
- figwal's flusher is **gated, not removed**. figaro uses no-flush,
  fsync-before-publish, no exceptions, every channel including the IR.
- Pure reduce precedes the append; fsync precedes publish; a failed sync
  rejects.
- N cursors per IR record is fine.
- Study's two-participant write: ordering, not two-phase commit. Show the
  code before deciding.

## Why the parent aria broke (CORRECTED, read this version)

`anthropic: messages.946: tool_use ids were found without tool_result blocks`.

**My first diagnosis was wrong and the fix I queued would have made it
worse.** Aria 6c2d7b9f read the raw IR of the poisoned aria (`arias/ir/n714`)
and found the result is not missing at all. It is **displaced by one
record**:

```
127 input   tool_result  toolu_01ByJoaN…
128 output  tool_invoke  toolu_01Tqv3GS…   <- the call
129 input   content:null study:{began:true, form_id:"@3c00e173"}
130 input   tool_result  toolu_01Tqv3GS…   <- its result, one record too late
131 input   prose
```

Record 129 is a **study mark**: contentless, but it encodes to a user message
carrying a study system-reminder, and it sits between the `tool_use` and its
result. The provider requires the result in the NEXT message.

**Cause**: `appendStudyMark` writes an IR record from an RPC goroutine with no
regard for whether the drain loop is mid-turn with an open call. The trigger
was `fig cast` from inside a tool call, which is exactly what "create a role
as step one" asks for.

**Do NOT synthesize a result for every dangling id.** That was my queued fix
and it would put TWO tool_results behind one `tool_use`, which providers also
reject, and would tell the model a call failed that in fact succeeded.

**Being fixed by 6c2d7b9f**, on its own worktree off this branch: encoder-side
hold-back (a user record with no tool_result is emitted after the one that
resolves the open ids, which unbricks existing arias without editing
history); the source fix (no out-of-band IR record between a `tool_use` and
its results, covering study, drop, and whatever the libretto later stamps);
and a truthful synthesized result only where a fork genuinely cut a live
call. Its files: `internal/figaro/repair.go`, `internal/figaro/study.go`,
`internal/angelus/study_hub.go`, the four provider encoders,
`internal/form/incantation.go`.

**Boundary while that is in flight**: this line of work stays in
`internal/store`, `internal/actor`, `internal/trunk` and `internal/config`.
Note that phase 4 did touch `internal/figaro/form.go` and
`internal/figaro/agent.go` (`SetIntent`, `applyControlPatch`) and added
`internal/form/protect.go`; none of those are on 6c2d7b9f's list.

**The self-cast deadlock is the same bug from the other end.** `fig cast`
on your own aria from inside a turn hangs, because the cast rides the inbox
and the inbox is running the turn that issued it. The corruption above is
the same operation going AROUND the loop instead of through it. One hangs
because it needs the loop; one corrupts because it bypasses it. Phase 9,
which makes study an ordinary patch on a separate node, should fix both, and
should be checked against both.

**Workaround meanwhile**: patch `target-aria` directly
(`figaro state set --id <role> target-aria <aria>`), which is a form write
served by the hub with no agent and no IR record.

## Devshell quirks

- `nix develop` ships its own Go. A benchmark run inside it showed a uniform
  +8 to +10% against a bare-shell run, including the do-nothing control.
  Compare like with like or do not compare.
- `nix develop` builds from the git tree: **untracked files are invisible**.
  `git add` before running anything in a devshell or you will debug a
  compile error that is not real.
- `FIGARO_ARIA`, `FIGARO_NO_BIND` and `FIGARO_PROMPT` are set inside an
  aria's own shell. `attend` refuses, binding is disabled, and any test
  needing attendance must `unset` all three (and run under a real pty for
  the interactive paths).
- `bc` is not installed. Use awk.
- `pgrep -f` matches the script's own command line. Use `pgrep -x` or filter
  `$$`.

## Log

### 2026-08-12, session 1 (aria ae6633c1)

- Role minted, heartbeat armed, notes started.
- Docs already committed on the branch: `a2364d42` (libretto per-form,
  figwal as a true WAL, lock audit), `a44c956e`, `b056f193`, `170a11c6`,
  `186926b4`.

**Next steps** are the phase list in
`plans/state-layer-implementation.md` Part I. Working order: phase 0
(figwal), phase 1 (actor), phase 2 (form on the actor). Those three are
worth having even if nothing else lands.

### 2026-08-12, session 1: phases 0 to 2 landed

| commit | what |
|---|---|
| figwal `4f9ce6a` | `SyncChannelThrough` (XWAL + Trunks), `NoBackgroundFlush` option, test. Pushed to origin/master. |
| `1be1afa2` | `internal/actor.Lazy`: spawn on submit, batch drain, linger, dormant. Exit race tested and fuzzed. |
| `1d9aa71b` | `store.Form` on the actor; group commit; sync before publish; IR appends sync; background flush gated off for figaro. |

figaro now pins figwal at `v0.16.2-0.20260813012153-4f9ce6a665f6`. **The
flake's vendorHash will need resetting** before any nix build: edit go.mod,
build, take the "got:" value. That is the documented one-step dance in
flake.nix and it has NOT been done yet.

**Two mutexes gone from `Form`**: the write lock and the sink list. The only
one left in form.go is `MemFormLog.mu`, the test double, which the audit
justifies.

#### Deviations from the plan

1. **Sinks emit BEFORE the answer.** The plan had the drainer answer waiters
   and then emit. A caller returning from `Apply` has always been able to
   assume the delta reached the sinks (they ran under the write lock), and
   moving the fanout off its goroutine would have withdrawn that silently.
   The race detector found it as a test race first.
2. **Completion is per submission, not a ticket watermark.** The plan (and my
   first implementation) released waiters when `done >= myTicket`. Tickets
   are handed out before the queue is entered, so a HIGH ticket can land in
   an EARLIER batch than a low one, and the watermark then frees a waiter
   whose result has not been written. `formWrite.done atomic.Bool` per write,
   with the broadcast tick only as the wakeup. Found by `-race -count=3`;
   `-count=1` passed three times in a row first. **Run the store package at
   `-count=3` under race or you will not see it.**
3. **`Command`/`Result`/intent/session/seq are NOT in yet.** Phase 2 kept
   `Apply`/`ApplyEffect` signatures so the blast radius stays inside the
   store. Phase 3 changes them.

#### The performance picture, honestly

Measured with `-benchtime 20x`, `TMPDIR=/var/tmp` (nvme; `/tmp` is tmpfs and
will hide every fsync you are trying to measure, which is a trap worth
remembering):

| writers | ns/op (whole round) | per patch |
|---|---|---|
| 1 | 17,428,254 | **17.4 ms** |
| 8 | 36,578,540 | 4.6 ms |
| 64 | 37,322,649 | 0.58 ms |
| 256 | 95,281,903 | 0.37 ms |

**Group commit works**: per-patch cost falls 47x from one writer to 256.

**The 17.4 ms figure above is a HARNESS ARTIFACT. Do not quote it.** The
benchmark spawns a goroutine and joins a WaitGroup per operation at
`-benchtime 20x`, which is far too few iterations to be trusted. Direct
measurement, same filesystem:

```
append (buffered)      5.8 µs
SyncThrough (fsync)    3.37 ms
Trunks.Head (borrow)   3.0 µs      <- the "double borrow" lead was wrong
ApplyForm end to end   3.16 ms
FormState (control)    41 ns
```

**So a solo form patch costs exactly one fsync, and nothing else measurable.**
The actor, the batch machinery and the handle borrow are all noise beside it.
Against roughly 5 µs before, the regression is ~600x on a single write, and
it is the price of the WAL being a WAL rather than a bug to find.

The floor is the filesystem: a raw `fsync` of 200 bytes on this box's
`/var/tmp` measures **3.13 ms** over 50 writes. Nothing in figaro can go
faster than that per durable write.

What can actually be done about it, in order:

1. **Batch, which already works**: per-patch cost falls 47x from one writer
   to 256, because one fsync covers the batch.
2. **Preallocation plus `fdatasync`** (durable-forms §3.3): an append changes
   the file size, so `fsync` and `fdatasync` cost the same today;
   `fallocate` at segment creation makes `fdatasync` the cheap one. This is
   the only lever that lowers the per-sync floor.
3. **Accept it where it is already invisible.** A turn writes ten to fifty IR
   messages, so 30 to 150 ms per turn against a provider round trip measured
   in seconds. `fig set` at 3 ms is interactive-invisible. The path that
   would hurt is a script doing hundreds of sequential writes.

**Not yet done and important**: no before/after on the live stack, no
`ariastress.sh` run, no twelve-aria recipe, no memory numbers. Gluck flagged
that figaro can allocate ~2 GB and wants savings hunted alongside the
regression.

#### Next steps, in order

1. Chase solo latency (lead 1 above first: one borrow instead of two).
2. Reset the flake vendorHash, then run the suite inside `nix develop`.
3. `ariastress.sh` before/after, PSS and swap, plus the twelve-aria recipe.
4. Phase 3: `Command`/`Result`, intent (`assert`/`ensure`), session+seq, the
   no-op acknowledgement.
5. Phase 4: schema validation in the writer.
6. Phase 5: `SubscribeFrom` as register-then-snapshot, lock-free.

#### Live validation on the WAL build (nix devshell, real provider)

```
20 sequential form patches   275 ms total (13.8 ms each)
a real turn                  2692 ms, answered
daemon PSS                   43.8 M, anon 17.9 M, swap 0
heap_alloc                   4.8 M
goroutines                   21
```

The 13.8 ms per `fig set` is mostly CLI process spawn: an earlier control
measured `figaro list -j`, which touches no board, at 12.16 ms. The
mandatory fsync is hiding inside the shell's startup cost at the CLI
granularity, which is the honest way to read it: **interactively the WAL
change is invisible.** A script that patches through one long-lived
connection would see the 3 ms.

Also done this session:
- `nix build` works: flake `vendorHash` reset for the figwal bump.
  `sha256-y0FdOfhVnIIOXAXsI9S/wg+9aXQbRne/K4sLGZZdzD4=`.
- Full suite green inside `nix develop .#default`.
- `ir_window_mb` defaults to 4 MiB instead of unbounded. The decoded IR is
  63 to 86 percent of a real aria's footprint, so this is the single
  largest memory lever available and it was switched off.
- `keepMu` retired for an `atomic.Pointer[string]`, per the lock audit: one
  string with a documented lock-order hazard, and a pointer swap has no
  order to invert.

#### Still unbounded, and the next memory lever

`formState.patches` grows for the life of the process: every patch a form
ever took, decoded, resident. This is the last unbounded retention in the
store and it is the next memory lever after the IR window.

**The design, including the trap.** Two halves:

1. **Serve below the window from the log.** `PatchesBetween(after, upTo]`
   checks whether the window still reaches `after`; if not, read that range
   back through `FormLog.RangePatches` and return a fresh slice. Correct,
   allocating, and only on the cold path (a retranslate of old history).
   The hot path keeps the zero-copy view.

2. **Trim, but COPY when you trim.** Here is the trap: `commit` appends to a
   SHARED backing array and each published state holds its own length. That
   is what makes the view safe, and it also means **re-slicing the front off
   does not release anything**: a header pointing into the middle of an
   array pins the whole array. Trimming has to copy the tail into a fresh
   array, and copying on every write is O(history) again, which is the
   regression this whole line of work removed.

   So: copy only when the excess crosses a slack allowance, exactly the
   pattern `cachedLog` already uses ("compaction is batched behind a slack
   allowance, 362 ns, zero extra allocations"). Read that code before
   writing this one.

Not started. `PatchesBetween`'s zero-copy contract and its test
(`TestFormPatchesBetweenIsAViewNotACopy`) are what will catch a mistake.

#### Fleet regression: 12 arias, one daemon, one studied form (300 patches)

Same harness (`scripts/ariastress.sh --study --study-patches 300`), same box,
against the numbers taken before the WAL change during the incantations work:

| | before WAL | after WAL |
|---|---|---|
| turns answered | 12/12 | 12/12 |
| history build (300 CLI patches) | 4.11 s | **4.98 s** |
| turn wall (12 concurrent) | 4.53 s | 5.49 s |
| control (12 x `ls -j`) | 0.17 s | 0.16 s |
| daemon PSS loaded | 56.8 M | 58.6 M |
| `Pss_anon` loaded | 30.6 M | 32.2 M |
| swap | 0 | 0 |
| goroutines | 93 | **80** |
| heap_alloc | 14.9 M | 16.0 M |

Read it as:

- **300 mandatory fsyncs cost 0.87 s**, or 2.9 ms each, which is exactly the
  measured filesystem floor. Nothing is being wasted; that is the price.
- **Turn wall is up 0.96 s across 12 concurrent turns**, most of which is the
  IR now syncing per message. Against a provider round trip measured in
  seconds it is visible but not dominant.
- **Goroutines are DOWN 14%** (93 to 80) with 12 live arias, which is the
  lazy actor: forms no longer park a goroutine each.
- Memory is flat to slightly up. The `ir_window_mb` default does not show
  here because these arias have almost no IR (72 rows); it will show on real
  arias, where the decoded IR is 63 to 86 percent of the footprint.
- The control is unchanged, which is what says the rest of the table is
  measuring the change rather than the afternoon.

#### Assert reaches the user

`fig state delete` / `fig unset` now refuse a key that is not there:

```
$ figaro state delete --id @0c479e41 nosuch
error: set: jsonrpc error -32000: remove "nosuch": no such key
```

Threaded as `SetRequest.Assert` (absent means the older forgiving rule, so
nothing else changes), through the hub's `ApplyFormEffectIntent` and the
agent's `SetIntent`.

**Known gap, deliberate**: `Agent.Set` answers BEFORE the loop runs, so on a
LIVE aria the refusal is enforced but only reaches the daemon log, not the
caller. Dormant nodes (hub-served) return it properly. The rule is applied
on both paths; only the reporting differs. Phase 3's synchronous command and
acknowledgement closes it, and until then this is the one place the two
halves of a write disagree about anything.

#### The WAL claim is now tested, not asserted

`internal/store/form_crash_test.go`: a child process patches a form and
prints every version the writer said landed; the parent SIGKILLs it at a
random moment, reopens the store, and checks every acknowledged version is
on disk. 130 to 240 acknowledged patches per attempt, all durable, four
attempts.

The shape is figwal's `crashtest` harness narrowed to one form. Note that it
re-enters the test binary through `FIGARO_FORM_CRASH_CHILD`, and the store
package already had a `TestMain`, so the child hook lives in
`xwal_bench_test.go`'s `TestMain` rather than in a second one.

Run it against real disk or it proves nothing: `TMPDIR=/var/tmp`.

### Where things stand

| phase | state |
|---|---|
| 0 figwal `SyncChannelThrough` + flush gate | **done**, released, pinned |
| 1 `internal/actor.Lazy` | **done**, fuzzed |
| 2 form on the actor, group commit, fsync | **done**, crash-tested |
| 3 command/event/ack | **partial**: intent (`assert`/`ensure`) is wired end to end; command, event, ack, session and seq are not |
| 4 schema validation | not started |
| 5 `SubscribeFrom` | **done**, and reachable through `Backend.SubscribeForm`; no consumer yet (the libretto is its customer, and `form listen` already does register-then-read on its own) |
| 6 tombstones and leases | **tombstone done** (`Form.Tombstone`, `system.tombstone`, sealing, idempotent, survives reopen, rides the ordinary subscription stream). leases are the subscriber set (`Reclaimable`); the delete path does not call either yet. |
| 7 retention policy | not started |
| 8 topology form | not started |
| 9 derived forms, libretto | not started |
| 10 API refactor | not started |

Also done outside the phase list: `ir_window_mb` bounded by default (the
single largest memory lever, previously off), `keepMu` retired, the flake
vendorHash reset, docs updated in `forms-design.md` and `reference/forms.md`.

### Handoff summary (read this first if you are the successor)

**State**: `feat/incantations`, ~50 commits ahead of main, everything green
(suite, `-race`, devshell, `nix build`, crash test, live turns). The branch
is the source of truth; nothing goes to main first.

**Done**: phases 0, 1, 2, 5 complete; phase 3 partial (intent only); phase 4
half (`CheckWritable` exists, not wired). Plus, outside the phase list: the
IR window default, the patch window, `soft_limit_mb`, `actor_linger_ms`,
`handle_idle_minutes`, `form_patch_window`, the sync instruments, mutex and
block profiling, and the removal of `Kick` and two mutexes.

**The three rules that must not be broken by later work**:

1. **Durable before visible, with no buffer.** Reduce purely, append, fsync,
   publish. A failed sync rejects; nothing is applied before the sync, so
   there is nothing to roll back.
2. **Batch durability, never semantics.** Each write is reduced against the
   state as of its own position, or `ifVersion` stops meaning anything.
3. **`PatchesBetween` is a VIEW.** Its safety rests on the published array
   being append-only and capped. Anything that compacts, rewrites or hands
   out an uncapped slice breaks it silently.
   `TestFormPatchesBetweenIsAViewNotACopy` is the guard.

**Two traps that cost me time and will cost you the same**:

- `-race -count=1` passed three times on a real race. Run the store package
  at `-count=3` under race.
- `/tmp` is tmpfs here, so every durability benchmark and the crash test are
  fiction on it. `TMPDIR=/var/tmp`.

### The next three things, in order

1. **Correction, and it retires a task.** I claimed several times that
   `fig form listen` had the snapshot-then-tail race. **It does not.**
   `internal/cli/form_listen.go` dials with the delta handler installed and
   only THEN refetches the snapshot, with the mirror's version check catching
   a delta older than the seed. Register-then-read, already, with the reason
   in the comment. `SubscribeFrom` remains right for derived forms and for
   anything that needs a durable cursor, but `form listen` needs nothing.
2. **`cachedLog.mu` to a published snapshot** (lock audit): 34 uses of one
   `RWMutex` on the hot read path, guarding `rows`, `trimmed` and `byFK`.
   They become one immutable struct behind an `atomic.Pointer`, the same
   pattern `formState` uses. Contended reads stop waiting on appends.
   Sizeable and mechanical; do it with the benchmark in hand.
3. **Phase 4, schema validation.** Beware the blast radius: `KeySystemManaged`
   keys (`aria_id`, `system.cwd`, `system.outfit_version`,
   `system.forked_from`) are written by the angelus during birth and would
   need the privileged path, so land the privileged entry point FIRST and
   only then start refusing.

#### Instruments, and a trap in the crash test

Landed the two missing instruments:

- `figaro.wal.sync.duration` and `figaro.wal.sync.batch`: how long an fsync
  took and how many patches it covered. **A batch distribution collapsing to
  1 is the alarm that group commit stopped working**, which is the only
  thing keeping a mandatory sync affordable.
- Mutex and block profiling are now ENABLED under `FIGARO_PPROF`
  (`SetMutexProfileFraction(5)`, `SetBlockProfileRate(10µs)`). They were
  served by `pprof.Index` and returned nothing, because nothing turned
  sampling on. For a daemon whose writers are serialization points, that was
  the missing profile.

**The crash test is now opt-in** (`FIGARO_CRASH_TEST=1`), because in a full
`go test ./...` it hung the suite for ten minutes. On tmpfs a "sync" costs
nothing, so the child spins fast enough to bury the parent in
acknowledgements. Run it deliberately, on real disk:

```
FIGARO_CRASH_TEST=1 TMPDIR=/var/tmp go test ./internal/store -run Acknowledged -v
```

### Succession

- **Successor minted**: `c1d55d02`, briefed, told to read the plans and wait.
  Its id is in `/var/tmp/figstate/.successor`.
- **The role is `@980dc16c`.** On handoff:
  `figaro state set --id @980dc16c target-aria c1d55d02`. The heartbeat
  reads the role's `target-aria` every ten minutes, so moving it moves the
  wakeups with it.
- **Do not use `fig cast` to move the role from inside an aria**: it rides
  the inbox, the inbox is running the turn that issued it, and it hangs
  until the timeout. Patch `target-aria` directly.

#### A real tool loop on the WAL build

The IR now syncs per message, and a tool loop is the path that appends most.
One aria on `sonn5`, three sequential bash calls then a reply, then a warm
second turn:

```
turn 1 (3 tool calls)   13.0 s wall, 8 messages, answered DONE
turn 2 (1 tool call)     3.4 s wall, answered OK
daemon                   PSS 34.1 M, anon 18.2 M, heap 7.2 M, 26 goroutines
resident IR              11 rows, 1390 bytes
```

Eight messages is eight fsyncs, ~24 ms, inside a 13 s turn. **The IR sync is
0.2% of a real turn.** That is the answer to whether mandatory durability is
affordable on the conversation path: it is, comfortably, and it was the last
open question about the WAL change.

#### Where the 2 GB comes from

Gluck's live daemon, measured this session:

```
heap_alloc   115 MB      heap_inuse  149 MB      heap_sys  416 MB
resident_ir   22 MB across 4 resident arias
goroutines   306         mem_limit  2147483648
```

**The 2 GB is a configured ceiling, not consumption.** `armMemoryLimit` set
`debug.SetMemoryLimit(2 GiB)` unconditionally and it was not configurable. A
high ceiling is a LICENCE as much as a limit: Go collects harder only as it
approaches, so `heap_sys` sits at 416 MB while only 149 MB is in use and the
runtime has no reason to give the rest back.

Now `[memory] soft_limit_mb`, default 2048, 0 for none, `GOMEMLIMIT` still
winning. Lowering it is the one-knob answer to "figaro is holding too much",
at the cost of more GC cycles.

**Not investigated: 306 goroutines with 3 live arias.** That is a lot, and
the lazy actor only removed the per-form ones. Worth a `/debug/pprof/goroutine`
against the live daemon (now that profiling is armed) before assuming it is
fine.

#### Config, read back at the enforcement point

Validated live in a devshell, which is the test Gluck asked for: a
`config.toml` supplying the values, read through the loader, checked where
it is enforced rather than where it is parsed.

```toml
[memory]
soft_limit_mb   = 512
ir_window_mb    = 2
actor_linger_ms = 250
```

```
$ figaro doctor mem -j | jq -c '{mem_limit_bytes}'
{"mem_limit_bytes":536870912}      # 512 MiB, as configured
```

`[memory]` now carries `dormant_after_minutes` (existing), `ir_window`,
`ir_window_mb`, `soft_limit_mb` and `actor_linger_ms`. Still unwired from
config: figwal's `IdleUnload` (the second of the three idle clocks) and the
subscriber lease TTL (phase 6, not built).

#### Final measurements for this session

Read path, unchanged by everything above (this is the zero-copy view from
the earlier perf work, still zero-copy):

```
FormDeltaPerSend100/1000/10000    45 / 48 / 53 ns/op, 0 allocs
FormWholePerSend100/1000/10000    49 / 55 / 57 ns/op, 0 allocs
```

Independence, 16 forms patched concurrently on real disk:

```
FormApplyManyForms   5.9 ms for 16 forms
```

Sixteen fsyncs at ~3 ms each finishing in 5.9 ms of wall clock is the proof
that the domains stay independent: one actor per form, one lock per form,
nothing shared but the store.

#### Size of the change

```
non-test Go      1784 +   179 -
tests            2037 +    56 -
plans and docs   2974 +    35 -
```

The green is mostly tests and design records. Deletions that matter:
`Form`'s write mutex and sink list, `Kick` and its six call sites,
`keepMu`, and the buffered-durability story.

#### Phase 4, first half only

`internal/form/protect.go`: `CheckWritable(patch, privileged)` refuses an
unprivileged write to a `KeySystemManaged` key. The mode has been in
`WellKnownKeys` since it was written and had never been enforced.

**It is NOT wired into the writer yet, on purpose.** The keys it protects
(`system.cwd`, `system.outfit_version`, `system.forked_from`, `model`,
`root`, `token_budget`, `truncation`) are written by the angelus during
birth and by the harness per turn, and every one of those call sites needs
the privileged path before anything starts refusing. Wiring it without that
bricks aria creation.

The order for whoever picks this up:

1. Add the privileged entry point to `store.Form` (an unexported field on
   the write, set only by in-process callers; no JSON tag anywhere near it).
2. Find every harness write of a system-managed key and route it through
   that entry point. `runtimeFillins`, `birthPatch`, `childBirthPatch`,
   `forkDress` and the per-turn ephemeral keys are the ones I know of.
3. THEN call `CheckWritable` from `reduceOne`, before the reduce.
4. Only then consider shape validation, which wants a per-key validator and
   the provider-keyed system schema, and which is a separate argument.

#### Validation gate, end of session 1

All green, on commit `HEAD` of `feat/incantations`:

```
go build ./... && go vet ./...            clean
go test ./... -count=1                    clean, bare shell and nix develop
-race on store/figaro/angelus/actor/form  clean
nix build .#default                       builds
FIGARO_CRASH_TEST=1 crash test            acknowledged patches all durable
live: mint, patch, real turn, tool loop    all answered
```

#### The last unbounded retention is bounded

`formState.patches` now has a window (`[memory] form_patch_window`, default
2048, 0 retains everything). Two halves, as designed:

- **Below the window, read from the log.** `PatchesBetween` checks whether
  the resident array reaches `after`; if not it walks `RangePatches` and
  returns a fresh slice. Cold path only: a retranslate of old history. The
  hot path is still the zero-copy view, still 45 to 57 ns and zero allocs.
- **Trim by COPYING, across a slack allowance.** Re-slicing releases nothing
  because the array is shared by construction, so the tail is copied into a
  fresh array once the excess passes 256. Copying per write would be the
  O(history) cost this whole line of work removed.

The log walk is O(history) per cold call, because `FormLog` has no bounded
range read. Acceptable now (it happens on retranslate, not per Send) and the
obvious next step if it shows up: figwal already has the segment index and
`RecordsBetween` would be a thin wrapper.

**Workflow note, from Gluck:** long commands were held in the foreground and
blocked the actor loop, so heartbeats queued behind them. `/var/tmp/figstate/job
<name> <cmd>` runs work as a transient user service; `/var/tmp/figstate/jobs.sh`
lists status. Use it for anything over a few seconds.

**A bug the benchmark caught, in the window itself.** The first draft decided
"is this range below the window" by comparing against `patches[0].Version`.
That is wrong: a no-op patch appends no record, so a form whose early
records changed nothing legitimately starts above version 1, and reading
that gap as a trim sent EVERY cold read to the log. The benchmark showed
`FormWholePerSend100` at 141 µs and 1245 allocs where it should be tens of
nanoseconds and zero.

`formState.trimmed` records the highest version actually dropped, and only
that sends a read to disk. After the fix:

```
FormWholePerSend100     55.8 ns   0 allocs   (resident)
FormWholePerSend1000    41.8 ns   0 allocs   (resident)
FormWholePerSend10000   12.9 ms   120k allocs (past the 2048 window: the log walk)
```

The last row is the honest cost of the window: a cold whole-history read of
a form with 10,000 patches. The longest board in the author's store holds
99, so it does not happen there, and the fix if it ever does is a bounded
range read in figwal rather than a full walk.

#### Phase 4 landed, and what the attempt taught

`CheckWritable` is wired. A hand-written harness key is refused, on a live
aria and a dormant one, with the same message:

```
$ figaro state set --id <aria> system.cwd /tmp/nope
error: set: jsonrpc error -32000: system.cwd: written by the harness, not by hand
```

Birth, fork and ordinary keys are unaffected. `ApplyFormPrivileged` is the
harness's own path (the boot patch's `system.cwd`), and there is no wire
field for privilege and must never be one.

**The attempt that failed, because the lesson is the valuable part.** I first
made `Agent.Set` synchronous so a live aria could RETURN the refusal instead
of logging it. Two tests failed, and the second was the important one:
`TestFormSetDuringToolRoundAppliesNextRound` hung for the full 30 s timeout.

A `set` arriving mid-turn is applied at the next ROUND BOUNDARY, deliberately.
Waiting for its verdict therefore blocks the caller for the length of a tool
round, and in that test forever. **Synchronous `set` is wrong for a live
aria, and the deferral is a feature, not an oversight.**

The resolution splits the checks by what they need:

- **Protection is a pure function of the patch**, so it is answered at
  ACCEPT time, before queueing, and the caller gets a real error with no
  waiting.
- **A stale `ifVersion` and an `Assert` removal need STATE**, which only the
  writer has. They stay deferred and reach the log. Phase 3's ticket is the
  proper close: the caller gets a handle it may await if it wants, and `set`
  keeps not waiting by default.

#### Fleet, end of session (everything landed)

Same harness again, against the two earlier columns:

| | before WAL | after WAL | after everything |
|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 |
| history build (300 CLI patches) | 4.11 s | 4.98 s | 4.99 s |
| turn wall (12 concurrent) | 4.53 s | 5.49 s | **5.01 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | **46.5 M** |
| `Pss_anon` | 30.6 M | 32.2 M | 30.1 M |
| goroutines | 93 | 80 | 80 |
| heap_alloc | 14.9 M | 16.0 M | **9.2 M** |
| resident form patches | n/a | n/a | 361 |

The memory work shows: **PSS down 12 M and heap_alloc down 6.8 M against the
pre-WAL baseline**, with the durability guarantee added rather than removed.
Turn wall came back most of the way. `resident_form_patches` is visible for
the first time, which is the point of reporting it.

## NEXT STEPS (superseding every earlier list in this file)

Everything above is history. This is the queue.

1. **Phase 3: command, event, ack, session and seq.** The attempt to make
   `set` synchronous proved why this is the right shape: a caller wants a
   HANDLE it may await, not a wait it cannot refuse. It also closes the two
   refusals that currently only reach the log (a stale `ifVersion`, an
   `Assert` removal on a live aria), and it is the whole server side of
   optimistic replication.
2. **`cachedLog.mu` to a published snapshot: DEMOTED, and here is why.**
   `BenchmarkCachedLogReadWhileAppending` measures a reader against an
   appender whose append sleeps 3 ms (standing in for the sync). It reads
   **10.55 ns/op, zero allocs**. The `appendMu` split already took the I/O
   out from under the read lock, which was the whole pathology; what is left
   is a brief cache-update window and it does not show. Do this only if a
   profile says otherwise. The benchmark is the guard: if a change makes
   readers wait on the append again, that number goes from tens of
   nanoseconds to milliseconds.
3. **Phase 6: tombstones and leases.** Needed by 8 and 9, and the tombstone
   is what lets a studied form be deleted at all.
4. **Phase 7: retention as a policy** (N segments), and the type-level rule
   that a channel handing out views may not have one.
5. **Phase 8: the topology form**, replacing `trunks.json`. `internal/trunk`
   dies with it.
6. **Phase 9: derived forms and the libretto**, per the rulings: one
   libretto per studied FORM, `@libretto::<formid>`, shared, refcounted, not
   forked, whole-form only, holding a COPY. Study is a two-participant write
   ordered so every crash over-counts.
7. **Phase 10: the API refactor** and `angelus.hello`.

**Perf work that is known and not done**:

- **Preallocation plus `fdatasync` in figwal.** The only lever that lowers
  the 3 ms per-sync floor. An append changes the file size, so `fsync` and
  `fdatasync` cost the same today; `fallocate` at segment creation makes the
  cheap one available.
- **A bounded range read in figwal** (`RecordsBetween`), so a cold
  `PatchesBetween` below the window is O(range) rather than O(offset). Do
  this BEFORE lowering `form_patch_window`, or the quadratic above bites.
- **306 goroutines on a daemon with 3 live arias.** Still unprofiled.
  `ariastress.sh` arms `FIGARO_PPROF`, but its EXIT trap rests the daemon
  before a profile can be taken, so profiling through it needs the trap
  suppressed or a curl inside the run. The socket lives at
  `$FIGARO_RUNTIME_DIR/pprof.sock`; the live daemon predates profiling being
  armed and would need a restart.

**A cold read below the window now stops at the range's end.** `RangePatches`
visits in index order, so walking past `upTo` finds nothing; without the
stop, every cold read of an early range walked the whole log. Still O(offset)
rather than O(1) because `FormLog` has no bounded range read, which is the
figwal `RecordsBetween` item in the perf list.

### A quadratic waiting to happen, if the patch window is ever tightened

The projection walks IR records in ascending order and asks
`PatchesBetween(prev, cur]` for each. If a board's history exceeds
`form_patch_window`, every one of those reads below the window walks the log,
so a COLD retranslate of such an aria is O(records x history): quadratic.

It does not bite today. The default window is 2048 and the longest board in
the author's store holds 99 patches, so nothing reaches it. But whoever
lowers that knob, or meets a board that writes continuously for a very long
time, will meet this.

The fix is the one already on the perf list: a bounded range read in figwal
(`RecordsBetween(channel, after, upTo)`, a thin wrapper over the segment
index that already exists) so a cold read is O(range) rather than O(offset).
Do that BEFORE lowering `form_patch_window`, not after.

#### Phase 3's seam: Submit and Await

`Form.Submit` returns a `Ticket` and does not wait; `Form.Await(ctx, t)`
parks the caller's own goroutine on the existing broadcast. `Apply` and
`ApplyEffect` are Submit plus Await, so nothing outside changed.

This is the half of phase 3 that lives below the wire, and it is what makes
"a handle you may await" possible instead of "a wait you cannot refuse",
which is the lesson the synchronous-`set` deadlock taught. **A caller that
does not need the version never waits at all**, and that is the only thing
that removes a parked goroutine per writer. The libretto cursor is its first
customer when it exists.

**A bug I introduced and caught in the same minute**: routing `applyEffect`
through the public `Submit` dropped the `priv` flag, so the harness's own
boot patch started being refused. Privilege never reaches the public
`Submit` now; both paths go through an unexported `submit` and the
privileged one is a distinct call site, which is the whole point of not
making it an argument.

Still not done in phase 3: `session`, `seq`, the acknowledgement of a no-op
on the wire, and intent on the RPC beyond the `assert` boolean.

## HANDOFF GATE, passed

Run before the role moved, on the final commit of session 1:

```
nix develop: build + vet + full suite        ok
-race -count=3 on store, actor, figaro       ok
FIGARO_CRASH_TEST=1 crash test               ok
nix build .#default                          ok
```

**Session 1 totals**: 66 commits ahead of main. Phases 0, 1, 2, 4 and 5
complete; phase 3 has its store-side seam (`Submit`/`Await`, intent) and
lacks its wire half (`session`, `seq`, the no-op acknowledgement). Phases 6
to 10 untouched.

Deletions worth naming: `Form`'s write mutex and its sink list, `Kick` and
its six call sites, `keepMu`, `cachedLog`'s lock over the append, and the
buffered-durability story figaro used to have.

The one thing a reader should take away: **figaro's writes are durable
before they are visible, and that cost one fsync per record, recovered
under load by batching and paid for in memory by two new bounds.** The
fleet ends 12 MB lighter in PSS than it began.

#### The tombstone

`Form.Tombstone(reason)` writes `system.tombstone` as an ordinary privileged
patch and seals the form. Three properties, each tested:

- **It is a RECORD.** Subscribers hear the death through the stream they
  already read, and a replay reproduces it. A derived form that must be
  rebuildable from the log cannot learn about a deletion nobody wrote down.
- **It is idempotent.** A delete retried after a crash does not have to know
  whether it got there the first time.
- **It survives a reopen.** The seal is rebuilt from the published state at
  open, so a dead form stays dead without anyone re-declaring it.

**The lease registry is the subscriber set**, and for a single process that
is not a simplification but the whole of it. A durable refcount cannot tell
"still reading" from "died holding a reference"; an in-memory set answers
both, because every holder dies when the process does and a restart is a
clean sweep rather than a TTL to wait out. `Form.Reclaimable()` is
tombstoned-and-unread; `Backend.FormReclaimable` exposes it.

A TTL only covers a holder that is alive but silent, which today is nobody
and later is a node on another machine. When that exists this becomes
`{id, holder, expires}` with a sweep, and nothing above it changes.

**Not wired into the delete path yet**, deliberately: `RemoveLeaf` is the
crash-ordered boundary repair (durable-forms §2) and putting a write in the
middle of it wants its own sitting. That is what remains of phase 6.

## END OF SESSION 1

69 commits ahead of main on `feat/incantations`. Every gate green.

| phase | state |
|---|---|
| 0 figwal sync + flush gate | done, released, pinned |
| 1 `actor.Lazy` | done, fuzzed |
| 2 form on the actor, group commit, fsync | done, crash-tested |
| 3 command/event/ack | **half**: `Submit`/`Await`/`Ticket` and intent below the wire; `session`, `seq` and the no-op acknowledgement above it are not started |
| 4 schema validation | done (`CheckWritable`, `ApplyFormPrivileged`) |
| 5 `SubscribeFrom` | done, reachable through `Backend.SubscribeForm` |
| 6 tombstones and leases | **most**: tombstone and `Reclaimable` done; the delete path calls neither |
| 7 retention policy | not started (needs figwal) |
| 8 topology form | not started |
| 9 derived forms, libretto | not started |
| 10 API refactor | not started |

**Start here**: the NEXT STEPS queue above, then phase 6's last piece
(calling `Tombstone` from `RemoveLeaf`, respecting its crash ordering), then
phase 3's wire half.

**Do not repeat these**: the four traps in the handoff summary; the
synchronous-`set` deadlock; inferring a trim from `patches[0].Version`;
routing a privileged write through the public `Submit`.

**Coordinate**: aria 6c2d7b9f is fixing the displaced-tool_result bug on its
own worktree off this branch, in `internal/figaro/repair.go`,
`internal/figaro/study.go`, `internal/angelus/study_hub.go`, the four
provider encoders and `internal/form/incantation.go`. Stay out of those.

---

# SESSION 2 (aria c1d55d02)

Role `@980dc16c` moved on the handoff; the heartbeat followed it.

### Phase 6 finished: the delete path buries what it takes

`XwalBackend.Remove` now hands `RemoveLeaf` a **bury** hook, called after its
refusal and before any detach or unlink:

- Each doomed form records its own death (`Tombstone`) while its channel
  still exists. A subscriber hears it on the stream it already reads, which
  is the difference between a death and a silence.
- Then the aria's caches are forgotten: handle, form, meta. **Every id in the
  set, not just the one named.** A recursive delete used to unlink a subtree
  and leave its children's forms resident, pointed at files that no longer
  existed, so `FormState` on a deleted aria kept answering from cache.

**The ordering property is the test.** `TestRefusedDeleteBuriesNothing`: a
tombstone cannot be taken back, so burying before the refusal would seal an
aria that is still alive. It failed on the first run, and the reason was not
the ordering.

### The bug that failure exposed: a stale topology decided the delete set

`xwalTopology.Nodes()` read `s.topology.Load()` raw, while `From()` resolves
through `s.Node`, which refreshes. So the adjacency the delete set is built
from could predate the fork it was supposed to include. Consequences, all
observed rather than argued:

- `fig kill <parent>` on a just-forked aria was refused by **figwal**
  (`trunk has 1 live branch(es)`) rather than by figaro, with a message no
  listing can explain.
- `fig kill -r <parent>` unlinked the fork — correct, it is a child — but
  figaro's own delete set never contained it, so its presentation edge, meta
  and caches were all skipped, and its form kept answering from memory after
  its files were gone.

One line: route `Nodes()` through `topologySnapshot()`. When nothing has
moved that is two loads and a compare, and it is the same freshness rule
every other reader of the topology already obeys.

`TestDeleteSetSeesAForkMadeSinceTheLastRefresh` is the guard, and it fails
on the parent commit.

**Deviation from the plan.** durable-forms §7 defers reclamation until a
tombstoned form has no reader. There is no sweep to collect a deferred
unlink, and deferring without a collector converts a tolerated race into an
unbounded disk leak, so the unlink goes ahead and the case is LOGGED
(`tombstone: unlinking a form still being read`). The reader learns from the
tombstone it has just been sent. When the sweep exists, that log line is
where it hooks in.

**Deviation, second.** A tombstone that cannot be written is logged, not
fatal. Deleting is the recovery for an aria whose disk is misbehaving, and
refusing the delete because the death record failed takes that away.

### Design correction folded in (from aria 057ebc2e): fork under-counts

`plans/durable-forms.md` §12.2.2 is new. The libretto refcount ordering was
chosen so every crash OVER-counts; **fork, import and kill are three write
sites outside the study verb that break it in the unrecoverable direction**,
because a child inherits its parent's `study-set` without anything
incrementing the librettos it names. Fork must increment before the child is
created. The reconciliation sweep RECOMPUTES rather than adjusts, so it
repairs under-counts too, which §12.2.1 did not say and which makes it a
backstop for all three.

### `cachedLog` is a published snapshot, and the shadow index is gone

Lock audit item 2. One `RWMutex` over `rows`, `trimmed`, `bytes` and a `byFK`
index became one immutable `logView` behind an `atomic.Pointer`, the pattern
`formState` already uses. Readers take one load and hold nothing; mutators
(append, trim, clear) serialize on `writeMu`, which no reader ever touches.

**`byFK` is deleted outright.** It mapped FigaroLT to absolute index, and it
answered nothing the rows cannot: entries are ascending by FigaroLT (that is
what `ReadFrom` binary searches on), so a resident hit is a search and every
miss already went to the inner log whenever anything had been trimmed. What
it did do was **grow forever** — nothing pruned it on trim — so a bounded
window carried an unbounded index of the entries it had dropped. `Lookup`
now searches for the LAST match, which is what the map's last-write-wins
semantics gave.

Measured, `-benchtime 200x -count=5`, same box, same filesystem:

| benchmark | before | after | |
|---|---|---|---|
| `CachedLogReadWhileAppending` | 11.45 ns | **1.30 ns** | **-88.7%** |
| `OpenWindowed/unbounded` B/op | 7.163 Mi | **5.478 Mi** | **-23.5%** |
| `OpenWindowed/budget=256KiB` | 3.936 ms | 3.875 ms | -1.5% |
| `WindowAppend/window=512` | 257 ns | 207 ns | ~ |
| `CachedLogReadLongAria/10000` | 291.3 µs | 323.9 µs | **+11.2%** |
| `CachedLogReadLongAria/50000` | 1.487 ms | 1.686 ms | **+13.3%** |

The first row is the one the change was made for, and its benchmark says so
in its own comment: a reader mid-append paid the writer's cache update and
now does not.

**The last two rows were NOISE, and re-measuring said so.** Five samples at
200 iterations of a millisecond-scale copy is not a measurement. Re-run from
a base worktree at `-benchtime 300x -count=10`:

| | before | after | |
|---|---|---|---|
| `CachedLogReadLongAria/1000` | 22.29 µs | 22.47 µs | ~ (p=0.85) |
| `CachedLogReadLongAria/10000` | 325.1 µs | 327.7 µs | ~ (p=0.97) |
| `CachedLogReadLongAria/50000` | 1.720 ms | 1.752 ms | +1.86% (p=0.011) |

Geomean +1.15%, and the only significant row is under two percent on a call
documented as the cold path. `Read()` does the same work either way and
allocates identically to the byte, which is what said the +13% could not be
real. **Do not publish a five-sample benchmark**; the base worktree at
`/var/tmp/figbase` makes the ten-sample version cost nothing but patience.

### Phase 3, the wire half: an outcome, not an OK

`SetResponse` gains `Outcome` and `Version`. Three outcomes, because three
things can happen and all three used to be `OK` with an empty list:

- **`applied`** — reduced, appended, fsynced; `Version` is the record.
- **`unchanged`** — legal and changed nothing; `Version` is where the board
  still stands. This is the ambiguity durable-forms §4.1 exists to remove.
- **`queued`** — accepted by a LIVE aria, which applies a set at the next
  round boundary by design. The verdict is not knowable yet and `Version` is
  zero. This is the honest name for the deferral that the synchronous-`set`
  attempt died on last session.

**Deviation: `session` and `seq` are NOT added.** They are for duplicate
suppression across a reconnect and for correlating an ack to an optimistic
client's pending queue. Nothing replicates yet, so a server-side dedup window
would be speculative state with no reader, and wire fields nobody sends are
the opposite of the standing order. The outcome and the version are what the
CLI and a script can use today. When a replica exists, the pair lands with
its dedup window and its conformance test in one change.

**And the CLI stops lying.** `fig set`/`fig unset` print the outcome:
`unchanged: mantra = "x"` when the board already held it, `queued:` when a
live aria will apply it at the next round boundary, and `set … @V` with the
durable version otherwise — which is the number a script quotes back as
`if_version`.

#### Live validation of the outcome, on an isolated daemon

Unbound form (the hub path, no agent):

```
$ figaro state set --id @49f50c5b brief hello
set brief = "hello" (figaro @49f50c5b) @3
$ figaro state set --id @49f50c5b brief hello
unchanged: brief = "hello" (figaro @49f50c5b) @3
$ figaro state delete --id @49f50c5b nosuch
error: set: remove "nosuch": no such key
```

Live aria (the agent path):

```
$ figaro set --id 78c19579 brief hello
queued: brief = "hello" (figaro 78c19579)
$ figaro unset --id 78c19579 nosuch
queued: nosuch (figaro 78c19579)
```

The second one is the point. That refusal is deferred to the round boundary
and reaches only the daemon log, and until now the CLI printed
`unset nosuch (figaro …)` — a write it had not made and would not make. It
now says what it actually did: it queued something.

### The quadratic is gone, and figwal never had to change

`plans/progress.md` (and 6c2d7b9f, from the libretto side) had this down as
"add `RecordsBetween` to figwal". It was not needed. figwal's `Log.Range`
already takes a `from`, and `XWAL.ReadAt` already addresses an arbitrary
index; the waste was entirely in figaro, whose `RangePatches` **started at
record 1 and skipped its way up to the range it wanted**.

`FormLog.RangePatches` now takes `(from, upTo)`. `xwalFormLog` starts the
read AT the range and stops at its end, and `errStopRange` — a sentinel
error whose only job was to abort the walk early — is deleted with it.

`BenchmarkFormColdDelta*`: one small range below the patch window, which is
what a retranslate asks for once per IR record.

| | before | after | |
|---|---|---|---|
| range at 500, history 2000 | 142.7 µs | **3.05 µs** | **-97.9%** |
| range at 1500, history 2000 | 384 µs | **3.4 µs** | **-99.1%** |
| allocs, either | 29 | 29 | unchanged |

The slope is the real result: before, tripling the offset roughly tripled
the cost (O(offset)); after, it is flat (O(range)). A cold retranslate of an
aria with a long board was O(records × history) and is now O(records).

**This unblocks the note that said "do this BEFORE lowering
`form_patch_window`".** The knob is now safe to tighten, which is the
largest remaining memory lever on a form: 2048 decoded patches per resident
board, kept for a cold read that now costs 3 µs from disk.

### The goroutine census, since nobody had run one

The live daemon has no pprof socket (`FIGARO_PPROF` was not set when it
started), so this is an isolated daemon with the profiler armed: 5 arias,
5 unbound forms, then 5 concurrent `form listen` clients, then idle.

```
5 arias                41 goroutines / 5 endpoints
+ 5 forms              48 / 10
+ 5 listeners          46 / 10
listeners gone         46 / 10
20s idle               46 / 10
```

The profile says where they are:

```
10  angelus.(*ariaHub).listen     one per endpoint
10  angelus.(*ariaHub).accept     one per endpoint
 5  actor.Start                   one per resident agent inbox
 5  figaro.(*Agent).act           one per resident agent
```

**Two per endpoint, two per resident agent, and listeners leak nothing** —
the count is identical before and after five of them come and go. So the
355 goroutines on Gluck's daemon (29 endpoints, 4 live arias) are a working
set, not a leak: 58 of them are endpoint accept/listen pairs, the rest ride
live turns.

**The endpoint pair is the interesting one.** It is held for as long as the
node's hub exists, whether or not the aria is awake, so it is a per-node
standing cost that dormancy does not reclaim. Whether a dormant aria needs
its own socket at all is a question for the API refactor (phase 10), where
`node.attach` is already the only method that hands out an endpoint.

**What this does NOT explain** is the memory. The same daemon reports
`heap_alloc` 260 MB against 38 MB of resident IR. Neither bound landed so
far (the IR window, the patch window) touches the other 222 MB, and nothing
in the goroutine census accounts for it either. That is the next thing to
measure, and it should be measured with a heap profile on a daemon that has
`FIGARO_PPROF=1` from the start rather than guessed at.

### The other cache, measured on 515 real arias

The hypothesis was that the unmeasured translation caches were the missing
222 MB. **They are not**, and the probe says so plainly
(`realtrans_probe_test.go`, against a COPY of the real store, 515 arias
opened at once, IR window off):

```
TOTAL   ir = 1,088,582,800 bytes    translations = 85,696,595 bytes   0.08x
largest single aria   ir 11.4 MB / 1609 rows   xlt 2.95 MB / 1606 rows
```

Translations are **8% of the decoded IR**, and about 25% of it on the
longest arias. So the IR remains the dominant term and the 222 MB is still
unaccounted for; it needs a heap profile on a daemon started with
`FIGARO_PPROF=1`, which the running one was not.

**But the comparison that matters is against the BOUND, not against the
IR.** The IR is windowed to 4 MiB per aria; the translations beside it were
windowed to nothing at all. On any aria long enough to matter, the bounded
cache is capped at 4 MiB while its unbounded neighbour keeps growing — so
the unmeasured one becomes the larger half precisely where memory is worst.

So: `[memory] translation_window_mb`, default 4, floored like the IR's, 0
for unbounded. **The default binds nothing that exists today** (the largest
real aria holds 2.95 MiB), which is the point: it caps growth that had no
cap, and it changes no measurement taken so far.

Two tests, at the two ends: `TestTranslationWindowBytesDefaults` for the
three answers a knob owes (unset is bounded, explicit 0 is unbounded, below
the floor is raised), and `TestTranslationBudgetReachesTheCache` for the
thing a config test cannot see — that the budget reaches the CACHE, bounds
residency, and loses no records, because a read below the window still
falls through to the log.

## WHERE THE MEMORY IS. `fig ls` retains 95 MB, and none of it is figaro's

This is the answer to the 2 GB question, and it is not in any cache figaro
owns. Measured, on a daemon started with `FIGARO_PPROF=1` against a COPY of
the real store (515 conversations, 577 trunks, 281 MB on disk):

```
after boot                    heap  ~8 MB
after ONE `figaro ls -j`      heap  208 MB     resident_arias 0
                                              resident_ir_bytes 0
                                              resident_translation_bytes 0
```

Zero resident arias. Zero IR. Zero translations. Every cache figaro
measures is empty, and the heap is 208 MB. The profile (`inuse_space`,
after the call):

```
83.21MB 78.99%  figwal/log.buildOwnSnapshot.func1
95.30MB 90.47%  angelus.(*handlers).list
        chain:  list -> ... -> disk.(*Log).RangeOwn -> buildOwnSnapshot.func1
```

**`figwal/log.buildOwnSnapshot` copies every record's payload into an
in-memory snapshot when a Log is opened:**

```go
err := l.RangeOwn(0, func(idx uint64, payload []byte) error {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	snap.entries = append(snap.entries, cp)
	return nil
})
```

That is figwal's lock-free read cache — the same published-snapshot pattern
this whole changeset has been applying — but **whole-log rather than
windowed**. Opening a node materializes the node's entire encoded history.

**And a listing opens every node.** `handlers.list` asks
`Backend.LastTS(id)` per row for recency, which is figwal's counter on the
OPEN handle, and the comment at `XwalStore.LastTS` says so in as many
words: *"one head open hydrates a cold node, and the trunks layer keeps it
warm"*. It never wakes an agent, which is what the comment was defending,
and it hydrates the store.

So the layering, stated plainly:

| cache | contents | bounded |
|---|---|---|
| figaro `cachedLog` (IR) | decoded entries | yes, `ir_window_mb` |
| figaro `cachedLog` (translations) | decoded entries | yes, as of this session |
| figaro `formState.patches` | decoded patches | yes, `form_patch_window` |
| **figwal `cacheSnapshot`** | **every raw payload of every channel** | **no** |

Every bound this project has added sits on top of an unbounded one. On this
store the ceiling is the store: 281 MB on disk becomes ~281 MB resident
once everything has been touched, before figaro decodes a byte of it.

`handle_idle_minutes` (figwal's `IdleUnload`, 5 min) does reclaim it — which
is why the daemon does not simply grow forever. But **a shell status line
running `fig ls` on a timer re-touches every node faster than the idle clock
can drop it**, and that is the steady state Gluck is looking at: 260 MB of
heap with 4 live arias and 38 MB of reported IR.

### The three candidate fixes, and what each costs

1. **Stop hydrating for recency (figaro, small).** A listing needs "when was
   this node last written", not its history. The newest segment file's mtime
   answers it with one `stat` and no hydration. Changes `fig ls` ordering
   semantics from "newest record timestamp" to "newest write time", which are
   the same thing in every case that is not a restore, so **it needs Gluck's
   ruling before it lands** rather than my judgement at one in the morning.
2. **Bound the snapshot (figwal, large).** A tail window with a fall-through
   to disk below it, exactly what `cachedLog` does one layer up. It is the
   right long-term shape and it costs figwal's "reads are lock-free from
   memory, always" property below the window.
3. **mmap the segments instead of copying payloads (figwal, largest).** The
   page cache becomes the cache, the pages are shared and evictable by the
   kernel, and RSS stops being figaro's problem. Best end state, biggest
   change.

**Nothing here is landed.** The instrument and the evidence are: this
section, `listing_cost_test.go` and `realtrans_probe_test.go`, both env-gated
against a copy. The measurement is repeatable in about ninety seconds and
should be repeated after any of the three.

### And now it is a number: `figwal loaded-heads`

`xwal.Store.LoadedHeads()` was exported and reported by nothing. `doctor mem`
prints it, and the wire response carries it:

```
figwal     loaded-heads=54  (each holds a channel's whole raw history)
heap       alloc=86.0 MiB
```

54 heads, 86 MiB, ~1.6 MB each, after one listing on the 515-aria copy.
Note it is 54 and not 515: a listing does not hydrate every node, and
whatever selects those 54 is the next thing to understand if fix 1 is taken.

The live daemon cannot answer yet — it is running a binary that predates the
field — but **any daemon started after this commit can be asked where its
memory is without a profiler**, which is the whole point. `resident_ir_bytes`
next to `loaded_heads` is the comparison that matters: the first is figaro's
bounded cache of decoded entries, the second is figwal's unbounded cache of
raw ones, and the second is an order of magnitude larger.

### Which call hydrates, refined (and one of my own numbers corrected)

An exact memory profile of the listing test (`-memprofilerate 1`) puts
figaro's own retention at 7.35 MB, **87% of it in `OpenForm`**:

```
6.43MB 87.45%  store.OpenForm
3.53MB 48.05%  encoding/json.(*RawMessage).UnmarshalJSON   (under it)
0.39MB  5.35%  store.topologySnapshot
```

So the hydrator is the **OUTFIT column**. `labelOf` falls back to the board
for any node with no stump — 209 of 515 in the real store — and
`FormState` opens the Form, whose replay opens the NODE, which is what makes
figwal build that node's whole-log snapshot. `topologySnapshot` itself is
0.39 MB: `vectorsLocked` and `presentLocked` are pure in-memory walks, and
`ListLight` was already fixed for this exact reason once before.

**Correcting myself**: I read "+113 MiB in `Conversations()`" off
`ReadMemStats` deltas and attributed it to the topology build. The profile
says otherwise. Both were measured; only one was measured at the right
granularity, and the coarse one attributed the cost to whichever call
happened to straddle a GC. The daemon-side profile (83 MB in
`buildOwnSnapshot`, reached through `handlers.list`) is the trustworthy one,
because it was taken from a living daemon rather than inferred from deltas.

### So the choice is latency against memory, and it is Gluck's to make

The label is two keys. Getting them costs a form replay and a node
hydration, per row, per listing.

1. **Do not RETAIN the form opened for a label.** Purely internal, no
   second source of truth, and it extends a doctrine already in the code
   ("TOUCH IS USE, NOT SIGHT" at `seenLocked`) from the idle clock to the
   registry. **Costs listing latency**: a status line re-replaying 209 forms
   every few seconds is real CPU, and the current design deliberately chose
   the other way.
2. **Cache the label in the meta sidecar**, which a listing already reads
   for message counts and tokens. No replay, no hydration, and the label is
   exactly as stable as the counts beside it. **Costs a second source of
   truth** for state that lives in the form, which this design generally
   forbids — mitigated by the heal path that already exists
   (`meta_heal.go`) and by the fact that `b.labels` does precisely this for
   STUMP labels today, justified there because a stump id is the hash of its
   own content.

I have not taken either. Both change something a user can feel (`fig ls`
latency, or where a label comes from), and the measurement is now cheap
enough that either can be judged in ninety seconds:

```
box=$(mktemp -d); cp -a --reflink=auto ~/.local/state/figaro/arias $box/arias
FIGARO_PROBE_ROOT=$box/arias go test ./internal/store -run ListingCost -v
```

### Phase 8 landed: the hierarchy is a form

`internal/trunk` (296 lines) and `trunks.json` are gone. The presentation
hierarchy is `store.TopologyTree`, a `store.Form` on a reserved stump.

**Why a stump.** The design wanted an unbound form NODE with a well-known
id, which needs client-specified trunk ids, which figwal does not have (and
which Gluck has already asked for: `answers-forms.md` §2). A stump is the
one node figwal names by a caller-chosen string, so the form needs no marker
file and no lookup, cannot be forked and cannot be bound. `listStumps`
filters it out, or the hierarchy grows a row describing itself.

**What it buys, checked rather than claimed:**

- A promote is **one patch naming two edges**
  (`TestTopologyForm_PromoteIsOneRecord`). The file it replaces rewrote the
  whole document per edit and could half-land.
- Durability, versioning and the single writer are the form's: reduce, append,
  fsync, publish. `Rev()` is the form version.
- `trunk.mu` is gone — the lock audit's fifth entry.
- **Parity is the old package's own test suite, ported verbatim**: same
  diagram, same claims, same names. That is what makes "it behaves as the
  file did" checkable.

**Migration**: a legacy `trunks.json` is folded in on first open and renamed
`.migrated`. Ordering rather than a journal — the fold is idempotent, so a
crash before the rename replays harmlessly, and a form already holding edges
is never migrated into.

**Live, on an isolated daemon**: `fig ls` draws parent→child, `promote`
swaps them, `ls -g` shows **no** `@topology` row, and the promote survives a
daemon restart (replayed from the channel, not re-read from a file).

**Deviation: retention is not built.** durable-forms §8 says the topology
form compacts to a single segment, which needs figwal's compacting channel
(phase 7). Until then it keeps every record: correct, and unbounded. Promotes
are rare so the growth is slow, but it IS growth, and phase 7 should point
here first.

| phase | state |
|---|---|
| 0,1,2,4,5 | done (session 1) |
| 3 | **wire half done**: outcome + version; `session`/`seq` deliberately not built |
| 6 | **done**: tombstone, `Reclaimable`, and the delete path calls both |
| 7 retention | not started (needs figwal) |
| 8 topology form | **done**, live-validated, `internal/trunk` deleted |
| 9 libretto | not started; §12.2.2 records a design bug found before it was built |
| 10 API refactor | not started |

### The OUTFIT column stops re-opening forms

A memo for the label, invalidated by any write that names an outfit key,
registered on the form's commit sink where it is OPENED — so every writer
passes it: the hub, the agent's own loop, a birth dressing, an outfit fold.
The board stays the only source of truth; this is a cache of it with a
complete invalidation, not a second copy on disk.

**The cycle it breaks.** A listing reads a label per row, which opens a Form,
whose replay opens the node, which figwal answers by materializing the whole
channel. A form is then evicted for idleness at 15 minutes, and the next
listing opens it again. A status line on a timer therefore cycles the store
through memory forever. Measured on the real-store copy:

```
evict every form, then list again:
  before   209 forms re-opened
  after      0 forms re-opened      (resident_form_patches 0, heads unchanged)
```

**What it does NOT fix, said plainly**: the FIRST listing still costs +114
MiB, and that arrives on the topology build rather than on the labels. Two
instruments disagree about which call triggers it — the daemon's own heap
profile says `buildOwnSnapshot` under `handlers.list`, and the in-test
`ReadMemStats` deltas put it on `Conversations()` — and I have already been
wrong once by trusting the coarse one. Whoever takes it next should isolate
the topology build under a profile rather than a delta.

**A bug in my own instrument, since it wasted three runs**: `HeapAlloc` can
FALL between two readings when a GC collects more than the step allocated,
and an unsigned subtraction renders that as 17592186044416 MiB. The helper
now returns `int64` and prints signed deltas.

### Fleet regression after this session (same harness, same box)

`scripts/ariastress.sh --arias 12 --study --study-patches 300`, against the
three columns session 1 left:

| | before WAL | after WAL | end of session 1 | **end of session 2** |
|---|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 | **12/12** |
| history build (300 CLI patches) | 4.11 s | 4.98 s | 4.99 s | 5.14 s |
| turn wall (12 concurrent) | 4.53 s | 5.49 s | 5.01 s | 5.85 s |
| control (12 x `ls -j`) | 0.17 s | 0.16 s | — | **0.16 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | 46.5 M | 48.3 M |
| `Pss_anon` | 30.6 M | 32.2 M | 30.1 M | 31.8 M |
| goroutines | 93 | 80 | 80 | **80** |
| heap_alloc | 14.9 M | 16.0 M | 9.2 M | 10.5 M |
| resident translation bytes | — | — | — | **194,967** |

**The control is unchanged**, which is what licenses reading the rest.
History build +3% and PSS +1.8 M are the new residents: the topology form
(one more open form for the daemon's life), the label memo, and the
translation accounting that now actually measures itself. Goroutines flat.

**Turn wall is +0.84 s and I do not claim it as a regression.** It is
dominated by provider round trips, it moved 5.49 → 5.01 between two runs of
the SAME build in session 1, and nothing in this session touched the turn
path. If it matters, it wants the twelve-aria recipe run three times on one
build before anyone reads a number off it.

The last row is new because nothing could report it before: 60 translation
rows, 195 KB, on arias with 72 IR rows between them.

### A negative result, and it is worth more than the change was

**Hypothesis**: a form's cold open replays every record to rebuild a value
figwal already holds folded (the form channel is reducible, and a segment
header carries the fold). So take `StateAt(last)` for the snapshot and read
only a bounded TAIL of patches, and a cold open becomes O(window) where it
was O(history).

I built it, with `FormLog.Bounds` and `FormLog.FoldedAt`, both real logs
implementing them, the `trimmed` trap handled, and a `TestColdFoldEqualsReplay`
equality suite (fold and replay must be indistinguishable: same version, same
state, same `PatchesBetween` answers at every range). All of it passed,
including the stump-hosted topology form.

**Then I measured it. `BenchmarkFormOpenReplay`, 6 samples:**

| | before | after | |
|---|---|---|---|
| M=30 / N=100 | 17.78 ms | 23.65 ms | **+33%** |
| M=30 / N=2000 | 26.17 ms | 114.66 ms | **+338%** |

**Reverted in full.** Two reasons, and the second is the interesting one:

1. Three calls (`Bounds`, `FoldedAt`, `RangePatches`) each opened the node
   handle, where the replay opened it once. That part is fixable.
2. **The replay was never reading disk.** By the time figaro asks for the
   first record, figwal has already copied every payload of the channel into
   memory (`buildOwnSnapshot`, see "WHERE THE MEMORY IS"). So figaro's
   "replay" is a walk over RAM, and there is no I/O for a fold to save. The
   optimization was aimed at a cost that does not exist at this layer.

**What that means for the next person, and it is the useful part:** no
figaro-side change to how forms open can pay for itself while figwal
materializes whole channels. The cold-open cost IS the hydration. Fix it in
figwal — 6c2d7b9f's segment-granular lazy loading, sketched in their message
and worth building — and only then revisit the fold, which becomes a genuine
saving the moment reading a record can miss.

The equality test was the right instrument and it did its job: it proved the
change CORRECT, and the benchmark proved it not worth having. Both were
needed, in that order.

### The steady-state listing is now free

Recency was the second per-row hydrator. `LastTS` is answered from figwal's
counter on the OPEN handle, so a cold node is hydrated to answer it — the
same disease as the OUTFIT column, and the same cure: memoize, and let the
writes invalidate.

Invalidation is complete because every append this daemon makes passes one
of two points: the IR log handed out by `Open` (wrapped in a one-method
`recencyLog` decorator) or a Form's commit sink. A delete drops both memos
with the aria's other caches.

Measured on the real-store copy, listing every row exactly as `handlers.list`
does (label + recency), then evicting every form and listing again:

```
first listing    +115 MiB   216 heads loaded
SECOND listing    +0.0 MiB    0 forms re-opened   216 heads (unchanged)
```

**A status line on a timer now costs nothing.** That was the actual
complaint: not that a listing is expensive once, but that it was expensive
every few seconds forever, because the caches it filled were evicted and
refilled in a cycle.

**The first listing still costs 115 MiB and that is figwal's**, not
figaro's: opening any node copies the whole channel into memory. Nothing on
this side can fix it (see "A negative result"), and the fix is
segment-granular lazy loading in figwal.

### PR 16 taken, after validating it on my own harness

`8d330026` (the translator skips derivation for a record it has already
encoded, plus the splice carrying `FormVersionOfSnapshot` AND
`LastStudyVersions`) merged into `feat/incantations` at `8f1a6853`.

What I checked, because Gluck asked for it on my harness rather than theirs:

| leg | result |
|---|---|
| `internal/provider` Observation suite, 6 samples | no regression; `Warm8` **-19.5%** (p=0.009) |
| long history (2000-turn IR, 1 and 8 forms), added to BOTH trees | `LongCold` **-6.5%** (p=0.041), long warm unchanged |
| full suite, vet, `-race -count=3` on store and provider | green |
| `nix develop`, fresh | build and full suite green |
| real store, fresh copy | 653 nodes, 647 forms, 5935 patches; warm delta reads still **35-36 ns/op, 0 B/op** |
| ad hoc, visually checked | fork, promote, delete, normalize all correct |

**Their -63% is not reproducible on my harness and must not be quoted from
it.** These benchmarks carry no per-LT translation cache, so the cache-hit
path their change optimizes is never taken. My harness proves the other
half, which is the half a merge needs: nothing around it got slower.

The visual leg is worth keeping as a recipe, because it exercises phase 8
end to end at the same time: two forks draw nested, a promote climbs, a
delete repairs the boundary (the survivor absorbs its prefix and its FORK
column goes to `-`), a second delete of the same id errors cleanly,
`normalize` reports already-normalized, the survivor's board is intact, and
`loaded-heads` is 0 afterwards. `/var/tmp/figstate/prvisual.sh`.

**Pushing to origin is Gluck's call.** The merge is local; the PR stays open
until he says otherwise.

### The config test the plan asked for, and it catches an unwired knob

durable-forms §9 asks for "a config supplying all four, read through the
loader, asserted at each enforcement point, not at the parser". It did not
exist. It does now, and it needed a small extraction to be possible: the
daemon set its knobs inline in `runAngelus`, so nothing could test the trip
from a config FILE to the place a value is enforced.

`applyStoreSettings` and `applyCacheSettings` are that boot step, extracted.
`TestMemorySettingsReachTheirEnforcementPoints` writes a real `config.toml`
with all six memory knobs, loads it through `config.Load`, applies them the
way the daemon does, and asserts the package variables the store consults
when it opens a handle, builds a form, or trims one.

**Proved red before green**: unwire `SetPatchWindow` and it fails with
`form patch window at the enforcement point = 2048, want 64`. That is the
class of bug this project has shipped twice — the IR window defaulted to off,
figwal's `IdleUnload` read nothing — and a parser test cannot catch either,
because the parse was never the broken half.

`HandleIdleForTest`, `FormLingerForTest` and `PatchWindowForTest` exist only
to let the test read those enforcement points from another package.

### `fig set --wait` (Gluck's ruling, 2026-08-13)

The one place the two halves of a write disagreed: on a LIVE aria a stale
`ifVersion` and an `Assert` removal are answered by the writer, which runs at
the next round boundary, so they reached the daemon log and not the caller.

`--wait` asks for that verdict. The event carries a buffered channel, the
round boundary fills it, and the caller gets `applied`/`unchanged` plus the
version. **Both** places a queued set can be applied report it — the drain
loop and `serviceSets` at the boundary — or a waiter hangs on a turn that
already applied its patch.

**Opt-in, and the tests are what keep it so.** Live, on an isolated daemon:

```
$ figaro set --id a5edb50e brief one            # default, unchanged
queued: brief = "one" (figaro a5edb50e)
$ figaro set --id a5edb50e --wait brief two
set brief = "two" (figaro a5edb50e) @6
$ figaro set --id a5edb50e --wait brief two
unchanged: brief = "two" (figaro a5edb50e) @6
```

Three tests: the DEFAULT set does not block during a tool round (the deadlock
the first attempt shipped, asserted as an absence), `--wait` does block and
answers correctly when the round ends, and a caller whose context expires
stops waiting while the patch still lands. `TestFormSetDuringToolRoundApplies
NextRound` — the test that hung the first attempt for its full timeout — is
green.
