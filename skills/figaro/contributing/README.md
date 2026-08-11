# Contributing to figaro

You are changing figaro's source, or its docs. Using figaro is a different
job: that is [../cli.md](../cli.md) and [../agents.md](../agents.md).

**Read [maintaining.md](maintaining.md) first.** It holds the one rule (never
test against the live daemon), the worktree layout, the dev shells, how to hand
work back, and how a release is cut. Everything else here is opened when its
subject is in play.

## Before you edit

| File | When to read it |
|---|---|
| [maintaining.md](maintaining.md) | Always, before the first change. Worktrees, dev shells, the loop, handing work back, and cutting a release, which goes through `scripts/release.sh` because a tag alone ships to nobody. |
| [updating-docs.md](updating-docs.md) | You are about to edit any file in the skill tree. Before, not after. |

## Testing what paints

A pty is the only honest oracle for anything that draws.

| File | When to read it |
|---|---|
| [ui-testing.md](ui-testing.md) | Changing the incipit, the pager, the composer, footers, freeze or scrollback, or a green test disagrees with a human. |
| [paint-repro.md](paint-repro.md) | Hunting one specific paint bug, or running the `scripts/paint-*.sh` instruments. |

To capture a real session and replay it as a fixture, see
[../debugging/tapes.md](../debugging/tapes.md): it serves debugging as much as
it serves CI, so it lives outside this tree.

## Subsystem machinery

Read the one you are inside. These describe internals, not usage.

| File | When to read it |
|---|---|
| [trunks-substrate.md](trunks-substrate.md) | Changing `internal/store` or the fork handlers. |
| [forms-design.md](forms-design.md) | Changing the form primitive: the single writer, the hub, outfit resolution, observation. |
| [roles-design.md](roles-design.md) | Changing roles, cast, or how a studied form renders into a model's context. |
| [reclamation.md](reclamation.md) | Changing the hub, the sweep or the row caches: what a live aria costs and when the daemon reclaims one. |
| [translation-lineage.md](translation-lineage.md) | Provider wire caches across a fork. |

## Designs and history

Neither is a description of shipped behaviour. Check the banner before
trusting a line.

| File | When to read it |
|---|---|
| [range-store.md](range-store.md) | The range-store contract. |
| [ir-convergence.md](ir-convergence.md) | The open tool-channel question. |
| [notes/](notes/) | Finished or abandoned work. Verify before trusting. |
