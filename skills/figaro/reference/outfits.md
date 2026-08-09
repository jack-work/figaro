# Outfits, the chalkboard, and `state`

The model behind every place an outfit can be named. Read it when you are
composing outfits, when `-O` did not do what you expected, or before changing
anything under `internal/outfit`.

## The three nouns

| Noun | What it is |
|---|---|
| chalkboard | An aria's key → JSON state, versioned alongside the conversation. Non-`system` keys the agent sees change; `system.*` is the harness's own namespace (model, credo, skills, cwd). |
| outfit | A named patch for a chalkboard, at `~/.config/figaro/outfits/<name>.toml`. |
| spec | What you type where an outfit is asked for: an ordered list of terms. |

## The spec

Terms are comma-separated and folded **left to right** — later terms win, the
same rule an outfit's own `layers` follows, so `a,b` and an outfit declaring
`layers = ["a","b"]` compose identically. A term is a name or an inline
literal.

```
sonn5                      a name: outfits/sonn5.toml
sonn5,focus                two names, focus winning
ttl=1h                     inline, sugar for {"ttl":"1h"}
mantra="cool thing"        a quoted value keeps its spaces
n=3   on=true              a value that parses as JSON keeps its type
'{"ttl":"1h","n":3}'       the literal itself
'{"layers":["a"],"x":1}'   an inline term may name layers of its own
```

`=`, `/` and `\` are illegal in a name: the first is the sugar's separator, the
others would let a name climb out of the outfits directory. Commas inside
quotes, braces or brackets are data, not separators.

The sugar is client-side. On the wire a spec is JSON — an array whose elements
are strings or objects, and a bare string is read as one spec:

```json
{"outfit": ["sonn5", {"ttl": "1h"}]}
{"outfit": "sonn5"}
```

## Birth versus fold

Two different things happen to an outfit, and which one you get depends on
whether the call creates the aria.

**Birth.** `figaro new -O <spec>`, and `send -O` when this call has to mint an
aria (an unbound shell, or `-e`). The folded patch defines a content-addressed
**stump** — one per (name, content version) — which the conversation is spawned
under, so the outfit's reminders are rendered once in a shared prefix that
every conversation under it inherits. The aria is stamped with
`system.outfit_name` and `system.outfit_version`; `figaro ls` shows both, with
`live` when the stamped hash still matches what is on disk.

**Fold.** Everywhere else. The spec is folded and applied to the aria's
existing chalkboard, **additively**: keys already holding that value are
skipped, and nothing is ever removed. Re-applying is therefore free, and the
agent sees a `<system-reminder>` for exactly what changed.

```sh
figaro send -O focus --id <id> -- <p>   fold, then ask, in one call
figaro fork -O focus -- <p>             fold onto the new branch, then ask
figaro fork -O focus                    fold onto the new branch, say nothing
figaro state outfit focus               fold, no prompt
```

A fold does not re-stamp `system.outfit_name`: the stamp is where the aria was
born, which is provenance and does not change.

## One call, not two

On the prompt verbs the spec travels **on the prompt itself**
(`figaro.qua`'s `chalkboard.outfit`). The agent resolves it when it accepts the
call and merges it into that prompt's patch, so:

- a spec that does not resolve fails the call, with the layer closure attached,
  before anything is queued;
- the fold and the message are one event, so the reminder renders on the turn
  that asked for it rather than the one after;
- an explicit `set` on the same call wins over the outfit.

`figaro.fork` carries the spec too, and applies it to the **alternative** the
moment it exists — resolved before the fork so a bad spec costs nothing,
applied after it so the patch cannot land on the parent and miss the branch.

## Layers

```toml
# ~/.config/figaro/outfits/pr-review.toml
layers = ["house-style", "opus5-ant"]

[system]
thinking_effort = "high"
```

An outfit's patch is its layers folded left to right, then its own keys on top.
Merging is per chalkboard key, so a layer setting `system.model` does not
disturb a sibling's `system.max_tokens`, and skills merge one at a time. A
layer's own layers are folded before it contributes; a layer named twice
applies at both positions; a cycle is refused.

```sh
figaro state outfit --tree pr-review    the closure, green found, red missing
figaro state outfit --list              what is on disk
```

`--tree` reads the config directory directly, so it works with no aria and no
daemon, and exits non-zero when the picture has red in it.

## Absence

A name that does not exist is an error — with one exception, which exists for a
reason: the **configured `default_outfit`** may be absent, because that absence
is what triggers first-run setup. Everything the caller names explicitly must
resolve. An outfit that resolves but sets no `system.provider` is reported as
that, and does not offer to reconfigure your store.

`default_outfit` takes a full spec, so it may compose:

```toml
default_outfit = "house,sonn5"
```

## Where the values come from

`fileName` and `dirName` load content off disk into the patch:

```toml
skills = { dirName = "skills" }          # skills.<base> per file
credo  = { fileName = "credo.md" }
```

A file that begins with a `---` fence is enveloped **front-matter only** (the
agent gets a `filePath` and reads the body if it decides to); anything else
lands whole. Bundled first-party skills load first and the config directory
overrides them by name.

## Stumps and `gc`

One stump exists per outfit VERSION, so editing an outfit mints a new stump the
next time an aria is born under it. Killing an aria collects its stump when it
was the last child; `figaro gc` sweeps the versions that predate that.
Collecting loses nothing: the stump is content-addressed, so the next aria
wanting that outfit re-mints the same id.

## Source

| Thing | Where |
|---|---|
| `Spec`, `Term`, `ParseSpec` | `internal/outfit/spec.go` |
| `LoadSpec`, closures, layers, `fileName`/`dirName` | `internal/outfit/outfit.go` |
| the additive diff | `chalkboard.Additive` |
| fold on a live aria | `figaro.Agent.OutfitPatch` / `ApplyOutfit` |
| fold on a prompt | `figaro.Agent.DressPrompt` |
| birth, and the default | `angelus.handlers.create` |
| fold on a fork | `angelus.handlers.fork` |
