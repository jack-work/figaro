# State layer: running progress

Live notes for whoever holds the role `@980dc16c`. Update this, not chat.

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
