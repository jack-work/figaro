---
name: figaro
description: Read before running any figaro command, addressing another aria, changing the figaro repo, or editing these docs. Holds the daily gestures (send and forget, ls, show, fork), the vocabulary, and an index that says when to open each deeper section.
---

# Figaro

A local coding-agent daemon. One static Go binary, three roles: the `CLI` you
type at, the `Angelus` supervisor that outlives your shell, and one `Agent` per
conversation. An **aria** is one conversation.

## The gestures

Nine commands cover almost everything. Exact forms, not summaries.

```sh
figaro -- <prompt>                 prompt the aria this shell is attended to
figaro send -f --id <id> -- <p>    fire and forget: submit, do not stream, exit now
figaro send -er -- <prompt>        one-shot throwaway aria, raw text on stdout
figaro ls                          your own lineage: parent, siblings, branches
figaro show <id> -n 5              the last 5 turns of an aria
figaro fork <id>:12 -- <prompt>    branch at turn 12 and prompt the new branch
figaro attend <id>                 bind this shell to an aria, like cd
figaro state <id>                  that aria's chalkboard
figaro status <id> -j | jq -r .state    dormant, idle, or active
```

Three rules that are not guessable:

- **`--` is mandatory** before a prompt. Everything after it is the prompt.
- **`-f` is how you avoid waiting.** Without it, `send` attaches to the stream
  and stays until the turn ends. With it, the daemon keeps working and you get
  your shell back. Follow along later with `figaro listen <id>`.
- **`ls` is scoped to where you are attended.** It is `ls`, not `ls -R /`.
  Start scoped; widen with `-H` (home) or `-g` (global) only when scoped comes
  back empty.

Every command takes `--id <id>` to address a specific aria, `-j` for one line
of JSON on stdout, and `-h` for its own help.

## Vocabulary

One definition each. The file named owns the model behind it.

| Term | One sentence | Owner |
|---|---|---|
| aria | One conversation, addressed by an opaque hex id. | [cli.md](cli.md) |
| trunk | A root-to-leaf path through the fork forest; the aria id is its id, and it survives forks. | [reference/trunks.md](reference/trunks.md) |
| turn | One exchange: your prompt plus everything the agent did about it. The coordinate `:N` in `<id>:<turn>`. | [reference/turns.md](reference/turns.md) |
| LT | The storage coordinate, positional and cross-channel. Not an address you type. | [reference/turns.md](reference/turns.md) |
| chalkboard | Per-aria key to JSON state that rides along with the conversation. | [reference/architecture.md](reference/architecture.md) |
| loadout | The named profile a conversation is born under: model, credo, skills. | [start.md](start.md) |
| angelus | The single supervisor daemon that owns the registry and outlives shells. | [reference/architecture.md](reference/architecture.md) |

## Where everything else lives

Each row is a separate read. Open one only when its "when" is true of you.

| File | When to read it |
|---|---|
| [start.md](start.md) | You are new to figaro and want the first hour to go well. Read once. |
| [cli.md](cli.md) | You need a command that is not in the gesture list above, or a flag's exact meaning. |
| [agents.md](agents.md) | You are an agent driving figaro, or you are writing a script that does. |
| [maintaining.md](maintaining.md) | You are changing the figaro source, or handing changed source back to its owner. |
| [updating-docs.md](updating-docs.md) | You are about to edit any file in this tree. Read it before, not after. |

Deep chapters, in `reference/`. These are long by design and cost real context;
open one when you are working inside that subsystem, not to browse.

| File | When to read it |
|---|---|
| [reference/trunks.md](reference/trunks.md) | Forking, branches, `attend`, and what `<id>:<turn>` addresses. |
| [reference/turns.md](reference/turns.md) | Turn ids, the turn-shaped read wire, pagination. |
| [reference/arias.md](reference/arias.md) | Reading an aria off disk, and the store layout. |
| [reference/architecture.md](reference/architecture.md) | The three roles, the IR, the chalkboard, the RPC wire, the provider layer. |
| [reference/ui-stream.md](reference/ui-stream.md) | How a conversation reaches a terminal: the read wire, inline freeze, the pager. |
| [reference/ui-testing.md](reference/ui-testing.md) | Testing anything that paints, in a real pty. |
| [reference/tapes.md](reference/tapes.md) | A rendering bug someone SAW and you cannot reproduce, or you want one recorded for CI. |
| [reference/paint-repro.md](reference/paint-repro.md) | Hunting a specific paint bug, or running the `scripts/paint-*.sh` instruments. |
| [reference/cache-control.md](reference/cache-control.md) | Prompt caching, and overriding it. |
| [reference/mantra.md](reference/mantra.md) | Maintaining your aria's mantra. |
| [reference/trunks-substrate.md](reference/trunks-substrate.md) | Changing `internal/store` or the fork handlers. Machinery, not usage. |
| [reference/translation-lineage.md](reference/translation-lineage.md) | Provider wire caches across a fork. |
| [reference/range-store.md](reference/range-store.md) | A design that is **not built**. Read as a proposal. |
| [reference/ir-convergence.md](reference/ir-convergence.md) | Part shipped, one part open. Read for the open tool-channel question. |
| [notes/](notes/) | Finished or abandoned work. Verify before trusting. |

Other first-party skills stand on their own: **subagents** for fanning work
out, **figscript** for scripting figaro from a shell, **figla** for waiting on
something without polling.
