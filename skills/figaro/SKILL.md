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
figaro state <id>                  that aria's form
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
| turn | One exchange: your prompt plus everything the agent did about it. The coordinate `:N` in `<id>:<turn>`; `.N` addresses an LT instead. | [reference/turns.md](reference/turns.md) |
| LT | The storage coordinate, positional and cross-channel. Not an address you type. | [reference/turns.md](reference/turns.md) |
| form | Per-aria key to JSON state that rides along with the conversation. | [reference/architecture.md](reference/architecture.md) |
| outfit | A named patch for a form: model, credo, skills. Named by a SPEC — `sonn5,ttl=1h` — which is folded at birth or onto a live aria. | [reference/outfits.md](reference/outfits.md) |
| angelus | The single supervisor daemon that owns the registry and outlives shells. | [reference/architecture.md](reference/architecture.md) |

## Where everything else lives

Each row is a separate read. Open one only when its "when" is true of you.

| File | When to read it |
|---|---|
| [start.md](start.md) | You are new to figaro and want the first hour to go well. Read once. |
| [cli.md](cli.md) | You need a command that is not in the gesture list above, a flag's exact meaning, or the vault (passphrase/keyring) verbs. |
| [agents.md](agents.md) | You are an agent driving figaro, or you are writing a script that does. |
| [contributing/](contributing/README.md) | You are changing figaro's source or its docs, or **cutting a release** — a tag alone ships to nobody. Start at that index. |

Deep chapters, in `reference/`. These are long by design and cost real context;
open one when you are working inside that subsystem, not to browse. Chapters
about changing figaro rather than using it live under
[contributing/](contributing/README.md) and are indexed there.

| File | When to read it |
|---|---|
| [reference/trunks.md](reference/trunks.md) | Forking, branches, `attend`, and what `<id>:<turn>` and `<id>.<lt>` address. |
| [reference/turns.md](reference/turns.md) | Turn ids, the turn-shaped read wire, pagination. |
| [reference/arias.md](reference/arias.md) | Reading an aria off disk, and the store layout. |
| [reference/architecture.md](reference/architecture.md) | The three roles, the IR, the form, the RPC wire, the provider layer. |
| [reference/ui-stream.md](reference/ui-stream.md) | How a conversation reaches a terminal: the read wire, inline freeze, the pager. |
| [reference/cache-control.md](reference/cache-control.md) | Prompt caching, and overriding it. |
| [reference/outfits.md](reference/outfits.md) | Composing outfits, the `-O` spec syntax, or `-O` did not do what you expected. |
| [reference/mantra.md](reference/mantra.md) | Maintaining your aria's mantra. |

Other first-party skills stand on their own: **subagents** for fanning work
out, **figscript** for scripting figaro from a shell, **figla** for waiting on
something without polling.
