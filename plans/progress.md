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

## Why the parent aria broke

`anthropic: messages.946: tool_use ids were found without tool_result blocks`
killed the previous incarnation mid-run.

**Hypothesis**: `repairInterruptedTail` only repairs the TAIL. It scans back
for the last assistant message carrying `tool_invokes` and closes any call
without a result. A dangling pair in the INTERIOR is never repaired, because
the scan stops at the most recent tool-bearing assistant message and that one
is complete. Index 946 in a 952-message aria is close to the tail but not at
it, which fits: an interrupt (or a fork) left a dangling call, later messages
were appended after it, and the repair on the next wake looked only at the
newest tool-bearing message.

**Avoidance**: do not interrupt mid-tool-call; keep tool calls short so the
window is small. **Fix** (not yet done, worth doing): make the repair scan
the whole suffix from the last USER message rather than stopping at the first
tool-bearing assistant message, and synthesize results for every dangling id
it finds, not just those in one message.

**Also**: `fig cast <self> <role>` from inside the aria HANGS. It is the
self-cast deadlock the plan describes: the cast rides the inbox and the
inbox is busy running the turn that issued it. Workaround used here:
`figaro state set --id <role> target-aria <aria>` directly, which is a form
patch served by the hub with no agent involved. Phase 9 removes the cause.

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
ever took, decoded, resident. Bounding it is the "windowed LRU" idea from
the original conversation, and it is harder than the IR window because
`PatchesBetween` must still answer for ranges below the window, which means
reading them back from figwal. Design is in durable-forms; not started.

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
| 5 `SubscribeFrom` | **done** in the store; not yet used by `form listen` or the hub |
| 6 tombstones and leases | not started |
| 7 retention policy | not started |
| 8 topology form | not started |
| 9 derived forms, libretto | not started |
| 10 API refactor | not started |

Also done outside the phase list: `ir_window_mb` bounded by default (the
single largest memory lever, previously off), `keepMu` retired, the flake
vendorHash reset, docs updated in `forms-design.md` and `reference/forms.md`.

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
