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

**Solo latency is the regression, and it is severe.** ~17 ms for one patch
against roughly 5 µs before. A raw `fsync` on this box's `/var/tmp` measures
**3.13 ms** (50 writes of 200 bytes), so the floor is high to begin with and
we are paying something like five times the floor.

Leads, in the order I would chase them:

1. **The form path borrows the trunk handle twice per patch**: once for
   `Trunks.Append`, once for `Trunks.SyncChannelThrough`, and `Head()` takes
   a lineage hold, a root borrow and `ensureCurrentTopology` each time. An
   `AppendSynced` that does both under one borrow would halve it for the
   solo case, with the separate sync kept for batches.
2. **Count the syncs per patch.** If `SyncThrough` also `syncDir`s, or the
   segment syncs more than once, that is multiples of 3 ms.
3. **Preallocation plus `fdatasync`** (durable-forms §3.3). An append changes
   the file size, so `fsync` and `fdatasync` cost the same; `fallocate` at
   segment creation makes `fdatasync` the cheap one.
4. **The IR now syncs per message too**, so a turn pays this per message.
   Not yet measured end to end; the twelve-aria recipe will show it.

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
