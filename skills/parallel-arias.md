---
name: parallel-arias
description: Orchestrate two or more figaro arias working in parallel on a shared project via separate git worktrees, with cross-aria communication (poll each other's logs, send steering messages) and a supervisor that retries on failure. Use when a task naturally splits into independent sub-projects that need a shared interface contract.
---

# Parallel Arias

Two (or more) `figaro` agents working a shared codebase in parallel,
each in its own git worktree, talking to each other through the CLI.

## Topology

```
            this aria (supervisor)
                 │ launches + watches
        ┌────────┴────────┐
        ▼                 ▼
 chess-backend       chess-frontend     ← named arias
   worktree A           worktree B      ← git worktrees of one bare repo
        ▲                 ▲
        └──── poll/poke ──┘             ← cross-aria comms
```

Use **named arias** so they're addressable: `figaro aria <name> -- "..."`
creates if absent, prompts if present.

## The three primitives

1. **Launch**: `figaro aria <name> -- "<long prompt>"` (returns once
   submitted; the agent keeps running in the background).
2. **Read counterpart**: `figaro aria <other-name> -a -l | tail -300`
   gives the full literal log; pipe-friendly.
3. **Send to counterpart**: `figaro aria <other-name> -- "<message>"`
   queues a user-tic even if the aria is mid-turn. This is your **live
   steering** primitive.

## Treebear setup

```bash
ROOT=/tmp/proj
mkdir -p $ROOT && cd $ROOT
mkdir seed && cd seed
git init -b main
# ... drop in shared README / contract ...
git add -A && git commit -m seed
git branch backend-dev && git branch frontend-dev
cd .. && git clone --bare seed proj.git
git -C proj.git worktree add $ROOT/backend  backend-dev
git -C proj.git worktree add $ROOT/frontend frontend-dev
```

## Prompt template (per aria)

Give every aria:
- Its **role**, its **own id**, and the **other id**.
- Its **worktree path**; firm "stay in your worktree, commit locally".
- A pointer to the shared **contract file** (README.md in seed).
- Scope of work for **this side only**.
- **Comms instructions**: when to poll the other aria, how to send notes,
  prefix convention (e.g. `NOTE FROM backend:`), don't spam.
- **Termination sentinel**: end with `READY: <role>` or
  `FAILED: <role>: <reason>` on its own line.

## Supervising

`figaro aria <name> -- ...` does NOT block until the model is done.
Don't watch the CLI's PID — watch the aria itself:

- `figaro list` → STATE column (`active`/`idle`) and MSGS count.
- `figaro aria <id> -a -l` → tail for the sentinel.
- A stall = `idle` with no MSGS increment for N ticks → send a nudge.
- Real failure = sentinel says `FAILED`, or persistent stall after
  multiple nudges → kill the aria, `worktree remove --force`,
  `worktree prune`, re-create branch, re-launch.

## Retry-on-failure

```bash
reset_side() {
  figaro kill "$aria" 2>/dev/null || true
  git -C "$BARE" worktree remove --force "$wt" || true
  git -C "$BARE" worktree prune
  git -C "$BARE" branch -D "${role}-dev" 2>/dev/null || true
  git -C "$BARE" branch "${role}-dev" main
  git -C "$BARE" worktree add "$wt" "${role}-dev"
}
```

## Merging

Have each side **commit locally** to their own branch as they work.
After both signal READY, run a merge worktree:
- `git merge backend-dev` then `git merge frontend-dev` on `release`.
- README and any shared files: prefer the seed contract; if both sides
  edited, take the union and let a third aria reconcile.
- Run a smoke script that exercises the wire contract end-to-end.

## Gotchas

- **First prompt may not auto-start a freshly-created named aria.**
  If MSGS stays at 2 (just the prompt, no reply), send a one-liner
  follow-up to kick it.
- **Stale worktrees** after a force-removed dir: use `worktree prune`
  *and* delete the branch before re-adding.
- **Sentinel grep must allow `^READY: …`** anywhere in the assistant's
  literal output. `figaro aria <id> -a -l` is the canonical view.

## Worked example

See `/tmp/chess-build/FINDINGS.md` and the scripts in that directory
for a concrete two-aria chess-app build orchestrated from this skill.
