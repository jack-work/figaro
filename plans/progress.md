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
