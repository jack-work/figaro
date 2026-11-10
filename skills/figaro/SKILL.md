---
name: figaro
description: Read before running any figaro command, addressing another figaro, changing the figaro repo, or editing these docs. Holds the daily gestures (send and forget, ls, show, fork), the vocabulary, and an index that says when to open each deeper section.
---

# Figaro

A local coding-agent daemon. One static Go binary, three roles: the `CLI` you
type at, the `Angelus` supervisor that outlives your shell, and an `Agent` actor
for each **figaro**, one conversation apiece.

## If you are a figaro, reading your own context

Your context carries `<system-reminder name="…">` blocks. **They are state,
not the user speaking**: the daemon injects them at each turn, and they are
the only way you see anything outside your own conversation.

| tag | what it is |
|---|---|
| `name="<key>"` | one key of **your bound form**: `cwd`, `mantra`, `aria_id`, whatever your outfit set. `figaro set <key> <value>` changes it. |
| `name="study"` | you began or stopped observing a form: `{form, observing, state, version}`. `state` is the form as it stood when observation began, minus `system.*` or outside your `--keys`. |
| `name="study:@id"` | that form **changed**: `{form, changes, set, removed, version}`, and `"exists":false` when it was deleted (the copy survives, so history still renders). `name="study:<id>"`, no sigil, is another figaro's bound form. |
| `name="fork"` | you are a branch: where you came from, and the outfit's `system.fork_incantation`. Rendered only when an incantation is set. |

A form you do not study is invisible to you. See [forms.md](forms.md) for the
four species, bound form, unbound form, studied form, role, what arrives on
change, and what configures it.

## The gestures

Nine commands cover almost everything. Exact forms, not summaries.

```sh
figaro -- <prompt>                 prompt the figaro this shell is attended to
figaro send -f --id <id> -- <p>    fire and forget: submit, do not stream, exit now
figaro send -A -f -S system.ttl=1h -- <p>   a one-off: a fresh figaro that reaps itself
figaro ls                          your own lineage: parent, siblings, branches
figaro show <id> -n 5              the last 5 turns of a figaro
figaro fork <id>:12 -- <nudge>     branch at turn 12 and nudge the new branch
figaro attend <id>                 bind this shell to a figaro, like cd
figaro state <id>                  that figaro's form
figaro status <id> -j | jq -r .state    dormant, idle, or active
```

Five rules that are not guessable:

- **`--` is mandatory** before a prompt. Everything after it is the prompt.
- **`-f` is how you avoid waiting.** Without it, `send` attaches to the stream
  and stays until the turn ends. With it, the daemon keeps working and you get
  your shell back. Follow along later with `figaro listen <id>`.
- **Inside a figaro, `send` without `--id` reaches you.** `FIGARO_ARIA`
  outranks `-A`; mint with `new` instead. See [subagents.md](subagents.md).
- **A fork's prompt is a nudge, never a brief.** The branch holds your whole
  conversation already, so `go` or `take the second option` is a complete
  instruction. Write a full prompt only for `new`, whose figaro starts empty.
  See [reference/trunks.md](reference/trunks.md).
- **`ls` is scoped to where you are attended.** It is `ls`, not `ls -R /`.
  Start scoped; widen with `-H` (home) or `-g` (global) only when scoped comes
  back empty.

Every command takes `--id <id>` to address a specific figaro, `-j` for one line
of JSON on stdout, and `-h` for its own help.

## Vocabulary

One definition each. The file named owns the model behind it.

| Term | One sentence | Owner |
|---|---|---|
| figaro | One agent and its whole conversation, addressed by an opaque hex id or its two-word handle (`nursery-lioness`); see "Naming a figaro". | [cli.md](cli.md) |
| aria | The append-only log under a figaro, and the old name for a figaro. Commands, flags, JSON keys and env vars still say aria (`aria_id`, `FIGARO_ARIA`, `target-aria`, `--aria`, the ARIA column of `ls`) until a later release renames them. | [reference/store.md](reference/store.md) |
| trunk | A root-to-leaf path through the fork forest; the figaro id is its id, and it survives forks. | [reference/trunks.md](reference/trunks.md) |
| turn | One exchange: your prompt plus everything the agent did about it. The coordinate `:N` in `<id>:<turn>`; `.N` addresses an LT instead. | [reference/turns.md](reference/turns.md) |
| LT | The storage coordinate, positional and cross-channel. Not an address you type. | [reference/turns.md](reference/turns.md) |
| form | Versioned JSON state. Every figaro's own form is its BOUND form; an UNBOUND form (`@`-sigiled id) has no figaro and stands alone; a form carrying `target-aria` is a ROLE. The rest (catalog, stump, libretto, intrinsic) are in the glossary. | [reference/forms.md](reference/forms.md#glossary) |
| outfit | A named patch for a form: model, credo, skills. Named by NAME (`-O sonn5,focus`), folded at birth or onto a live figaro; keys travel beside it on `-S`. | [reference/outfits.md](reference/outfits.md) |
| angelus | The single supervisor daemon that owns the registry and outlives shells. | [reference/architecture.md](reference/architecture.md) |

## Where everything else lives

Each row is a separate read. Open one only when its "when" is true of you.

| File | When to read it |
|---|---|
| [start.md](start.md) | You are new to figaro and want the first hour to go well. Read once. |
| [cli.md](cli.md) | You need a command that is not in the gesture list above, a flag's exact meaning, or the vault (passphrase/keyring) verbs. |
| [agents.md](agents.md) | You are an agent driving figaro, or you are writing a script that does. |
| [subagents.md](subagents.md) | You are spawning a subagent or fanning work out to parallel figaro sessions, and want the exact commands (`new` mints, `send` steers) plus how to watch and collect them. |
| [contributing/](contributing/README.md) | You are changing figaro's source or its docs, or **cutting a release**: a tag alone ships to nobody. Start at that index. |

Deep chapters, in `reference/`. These are long by design and cost real context;
open one when you are working inside that subsystem, not to browse. Chapters
about changing figaro rather than using it live under
[contributing/](contributing/README.md) and are indexed there.

| File | When to read it |
|---|---|
| [reference/trunks.md](reference/trunks.md) | Forking, branches, `attend`, and what `<id>:<turn>` and `<id>.<lt>` address. |
| [reference/turns.md](reference/turns.md) | Turn ids, the turn-shaped read wire, pagination. |
| [reference/store.md](reference/store.md) | Reading a figaro off disk, and the store layout. |
| [reference/architecture.md](reference/architecture.md) | The three roles, the IR, the form, the RPC wire, the provider layer. |
| [reference/compatibility.md](reference/compatibility.md) | A CLI and a daemon of different builds or releases, the wire contract, or adding a field, method or capability to `api/rpc`. |
| [reference/providers.md](reference/providers.md) | Writing `providers/<name>.toml`, pointing a gateway at a new endpoint, signing in to Azure with Entra, Copilot's Responses settings, what CTX counts, or a model failing on one provider and working on another. |
| [reference/ui-stream.md](reference/ui-stream.md) | How a conversation reaches a terminal: the read wire, inline freeze, the pager. |
| [reference/keys.md](reference/keys.md) | Moving a key in the pager, filling the leader, writing `keys.toml`, or why a key means one thing in one mode and another elsewhere. |
| [reference/quoting.md](reference/quoting.md) | Quoting a passage into a prompt (`<412.0:23-1180>!`), visual selection, and the pager's `:send` / `:fork` / `:listen` / `:attend`. |
| [reference/decisions.md](reference/decisions.md) | You want a fast yes/no or a scored choice instead of prose (`figaro decide`, a `??` draft), you are choosing between form entries (`?? @skills`), or you are changing that grammar. |
| [reference/methods.md](reference/methods.md) | A form key should be decided for you on every inquiry (`system.methods`), or a key changed and nobody set it. |
| [reference/cache-control.md](reference/cache-control.md) | Prompt caching, and overriding it. |
| [reference/outfits.md](reference/outfits.md) | Composing outfits, the `-O`/`-S`/`-D` grammar, or dressing did not do what you expected. |
| [reference/forms.md](reference/forms.md) | Using unbound forms and roles: the verbs, the listings, the matrix. |
| [debugging/tapes.md](debugging/tapes.md) | Record a figaro's wire and replay it: a rendering bug someone SAW and you cannot reproduce, or one recorded for CI. |
| [reference/mantra.md](reference/mantra.md) | Maintaining your figaro's mantra. |

This skill is the ONLY one that ships inside the binary, so anything an agent
must be able to reach without a preinstalled config lives here, [subagents.md]
included. The skills that are not bundled (`figscript`, `figla`,
`parallel-arias`, `inquire-figaro`, and the rest of `skills/` in the repo) are
copied into a config dir by whoever wants them.

[subagents.md]: subagents.md
