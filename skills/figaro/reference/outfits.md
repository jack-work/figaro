# Outfits, the form, and `state`

The model behind every place an outfit can be named. Read it when you are
composing outfits, when `-O` did not do what you expected, or before changing
anything under `internal/outfit`.

> **Stumps are dead.** The sections below that speak of stumps and
> content-addressed birth describe the pre-forms world; the primitive is now
> the unbound FORM, and `reference/forms.md` is the current model. What remains
> true here is the outfit grammar, the resolver, and how dressing travels.

## The three nouns

| Noun | What it is |
|---|---|
| form | An aria's key → JSON state, versioned alongside the conversation. Non-`system` keys the agent sees change; `system.*` is the harness's own namespace (model, credo, skills, cwd). |
| outfit | A named patch for a form, at `~/.config/figaro/outfits/<name>.toml`. |
| spec | What you type where an outfit is asked for: an ordered list of terms. |

## The two axes

Dressing has two axes and they are never mixed (Gluck's ruling, 2026-08-11).
Outfits are NAMES; patches are DATA; a directive never rides inside a patch.

| flag | takes | folded |
|---|---|---|
| `-O`/`--outfit` | outfit names, comma-separated | first |
| `-S`/`--set` | `k=v` or a whole JSON literal, comma-separated | second |
| `-D`/`--delete` | form key paths, comma-separated | last |

```
-O sonn5                   a name: outfits/sonn5.toml
-O sonn5,focus             two names, focus winning
-S ttl=1h                  sugar for {"ttl":"1h"}
-S mantra="cool thing"     a quoted value keeps its spaces
-S n=3   -S on=true        a value that parses as JSON keeps its type
-S '{"ttl":"1h","n":3}'    the literal itself
-D system.tags,mantra      two keys removed
-O sonn5 -S ttl=1h         compose: the outfit, then your key ON TOP
```

Each flag composes with itself on repeat (`-O a -O b` is `-O a,b`), and the
order between them is fixed: **outfits, then set, then delete**. A key you
wrote always beats an outfit that also sets it.

`layers` is respected in exactly ONE place: the unmarshal that builds a patch
from an outfit FILE, where it names that file's own layers. Written into `-S`
it is ordinary data and is stored as typed. That is what lets a patch cross
the chalkboard without the closure machinery waking up at all.

### The shell is half of this grammar

A literal must be QUOTED. Unquoted, two things go wrong before figaro sees
anything: `{mantra:test}` is not JSON (keys are quoted in JSON: the sugar
`mantra=test` is what you want), and `{a:1,b:2}` is **brace-expanded by the
shell** into two separate words with the braces gone, so the term never
arrives at all. Both are refused by name rather than as "no such outfit".

```sh
figaro new -O base -S '{"ttl":"1h"}'   # quoted literal
figaro new -O base -S ttl=1h           # the same thing, no quoting needed
```

A name is a file basename, and the grammar is narrow about it: no whitespace,
no `=` (that is `--set`), no `/` or `\` (a name must not climb out of the
outfits directory), no `{} []`, quotes or `:`, and no leading `-` (so `-O -j`
says so locally instead of asking the server for an outfit called `-j`). The
same gate applies to a `layers` entry inside a file.

Commas inside quotes, braces or brackets are data, not separators: but the
structure must balance. An unmatched `}` or `"` is an error, not a mode in
which commas stop separating. A term that sets nothing (`{}`) is an error too.

On the wire the two axes stay apart: names in `outfits`, data in `patch`.

```json
{"outfits": ["sonn5"], "patch": {"set": {"ttl": "1h"}}}
```

## Birth versus fold

Two different things happen to an outfit, and which one you get depends on
whether the call creates the aria.

**Birth.** `figaro new -O <names>`, and `send -O` when this call has to mint an
aria (an unbound shell, or `-e`).

TWO PATCHES, and which is which is the whole economy. The **stump** carries the
DEFAULT outfit's closure and nothing else: one per content version: so its
identity is a pure function of that closure and every aria wearing the outfit
shares one node, one set of records, and one rendered prefix in the provider's
cache. The **child** carries what `-O` asked for, plus the runtime fill-ins. So
`-O` ADDS to the default rather than replacing it, and that rule falls out of
the topology instead of being arranged.

Folding `-O` into the stump (which is what 0.22.x did) minted a private stump
per literal, so `-O mantra=x` defeated the sharing stumps exist for.

Collection spares the LIVE default, by hash rather than by name: edit an
outfit's files and the hash moves, so the version nobody wears is reaped once
its last aria dies while the current one stays warm.

**Fold.** Everywhere else. The spec is folded and applied to the aria's
existing form, **additively**: keys already holding that value are
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

On the prompt verbs the dressing travels **on the prompt itself**
(`figaro.qua`'s `form.outfits` and `form.patch`). A name is not resolved by the
client and it is not resolved by the writer either: it is resolved ONCE, at the
daemon's API boundary, by the single dressing call every method routes through
(`angelus.dress`, and `dressParams` for the methods that reach an aria through
its hub). So:

- a spec that does not resolve fails the call, with the layer closure attached,
  before anything is queued;
- the fold and the message are one event, so the reminder renders on the turn
  that asked for it rather than the one after;
- an explicit `set` on the same call wins over the outfit;
- everything below the boundary: the store's single writer, the agent's actor
  loop, the hub's agentless writer: holds pure data and reads no file. That
  last one is not decoration: the hub path is what an attended FORM takes, and
  while it was the one write path that never materialized, `fig form outfit
  test` stored `{"layers":["test"]}` on a board and reported success.

The FOLD still happens at drain, against the board the patch actually lands on.
That is not an implementation detail: a `set` or `unset` queued behind a running
turn has not touched the board yet, so a diff taken at accept can call a key
"already equal", omit it, and let the queued removal win: the turn answered
without the key you dressed for.

`figaro.fork` carries the dressing too, and applies it to the **alternative**
the moment it exists: resolved before the fork so a bad spec costs nothing,
applied after it so the patch cannot land on the parent and miss the branch.

## The resolver

One `Outfitter` per daemon, and it is the only thing that reads an outfit file.

It works in **epochs**: a consistent view of the outfits directory. The first
read of a file in an epoch pins its bytes into a content-addressed snapshot
store, and everything derived in that epoch: including a fold rebuilt after
eviction: comes from the pinned copy, so a resolution cannot straddle an edit.
Within an epoch a cached answer is valid by definition: no stats, no dependency
lists.

- **Staleness**: an epoch is trusted for 100ms, then a pass stats only what it
  touched and turns it over if anything moved. `fig outfit reload` turns it
  over immediately and reads nothing doing it.
- **Eviction** is by BYTES (64MB, LRU, five-minute idle sweep), because large
  outfits are the anticipated case. Evicted folds rebuild from the snapshot.
- **Cycles** are found by the memoised depth-first walk: which IS the
  incremental topological sort: named in the error, and then TAINTED: every
  name on the loop answers from the verdict instead of walking again. Taints
  die with the epoch.
- **Warming** is one background goroutine at startup, for the configured
  default outfit only. Startup blocks on nothing.

The epoch replaced a cache that validated by stat-ing every file it was built
from, with each parent merging each child's dependency list by linear scan.
On a generated tree of 800 files that cost 2.72 SECONDS cold and 2.4ms warm;
it is now 19.9ms and 61µs.

## Layers

```toml
# ~/.config/figaro/outfits/pr-review.toml
layers = ["house-style", "opus5-ant"]

[system]
thinking_effort = "high"
```

An outfit's patch is its layers folded left to right, then its own keys on top.
Merging is per form key, so a layer setting `system.model` does not
disturb a sibling's `system.max_tokens`, and skills merge one at a time. A
layer's own layers are folded before it contributes; a layer named twice
applies at both positions; a cycle is refused.

```sh
figaro state outfit --tree pr-review    the closure, green found, red missing
figaro state outfit --list              what the server has on disk
figaro state outfit --refresh           re-read outfits and config from disk
```

`--tree` reads the config directory directly, so it works with no aria and no
daemon, and exits non-zero when the picture has red in it.

## Absence

A name that does not exist is an error: with one exception, which exists for a
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

## What makes two births the same outfit

The stump id is `<label>@<version>`, and the **version is the identity**: the
canonical hash (keys sorted, no whitespace, numbers by value) of the birth
record the stump writes, minus the version field, which cannot cover its own
hash. Everything is decoded and re-marshalled on the way in, so formatting
never reaches it.

The name is INSIDE that hash, not merely alongside it. That is what lets
everything else key on content alone. All of these are one stump, minted once
and reused:

```sh
figaro new -O 'base,{"mantra":"test"}'
figaro new -O base,mantra=test
figaro new -O 'base,{ "mantra" : "test" }'
figaro new -O 'base,{"x":1,"mantra":"test"}'   # and with the keys the other way
```

The label is the names in order plus one `{}` if any term was a literal, and
that is all: a literal is anonymous, and the hash already says everything about
it. So WHERE a literal sits changes the fold but not the identity -

```sh
figaro new -O base,x=1,mantra=test      # all three land on
figaro new -O x=1,base,mantra=test      # base,{}@0523455ab442fec2
figaro new -O x=1,mantra=test,base      # because base sets neither key
```

- while a literal that actually collides folds differently, hashes
differently, and is a different stump:

```sh
figaro new -O setsx,x=1     # x=1  setsx,{}@994a6d0c…
figaro new -O x=1,setsx     # x=9  setsx,{}@cbeef30a…   (setsx sets x=9)
```

Two outfits with identical bodies and different NAMES stay two outfits, because
the name is hashed with the body, a listing that reported an aria under a name
nobody asked for would be worse than a duplicate stump.

Content decides sameness; the label in the id is a readable restatement of what
the hash already covers, and it renders a literal as something no name may
contain, so a stamp carrying one cannot be re-resolved by accident.

Change one byte of an outfit file and the next birth hashes differently, so it
mints a new stump; arias already under the old one stay there, and `figaro ls`
shows their version instead of `live`.

## The label is a UI concern; the hash is the value

`figaro ls`'s OUTFIT column names the **stump** an aria was born under, read
from the topology. It is not `system.outfit_name`. That key still rides the
form, where the agent can see it (and change it: the form is
mutable by design), but nothing structural reads it: a `set system.outfit_name
x` used to rename the aria's outfit in every listing and, because the column is
what the version is re-resolved against, report an unchanged outfit as stale in
the same breath.

So a row's outfit is immutable for the life of the aria, and a fork keeps its
parent's: the branch is under the same stump. An aria spawned under the root
with no stump carries no outfit rather than borrowing one, and an outfit with
no names in it displays as `{}` with its hash in the VER column.

## Stumps and `gc`

One stump exists per outfit VERSION, so editing an outfit mints a new stump the
next time an aria is born under it. Killing an aria collects its stump when it
was the last child; `figaro gc` sweeps the versions that predate that.
Collecting loses nothing: the stump is content-addressed, so the next aria
wanting that outfit re-mints the same id.

## Where this is going

A stump is very nearly a **cast object** already: a durable reducible thing an
aria observes. The difference is one rule, a stump cannot be patched, only
forked, and that rule is what makes content-addressing work. Minting an aria
becomes "fork a cast object; the fork backs the figaro, the object keeps its
own history".

So the vocabulary settles as: an outfit is a named **spec**; a spec
materializes as a cast object; a cast object is a **stump** when it is closed
to patches and an ordinary object when it is not; a stump can be used as a spec
in turn. Stump versioning stays a separate axis from an object's history, a
new version is a whole new stump, because no version can be produced *from*
one.

## Source

| Thing | Where |
|---|---|
| the grammar: `ParseNames`, `ParseSet`, `ParseDelete` | `internal/outfit/assemble.go` |
| the one dressing call | `outfit.Outfitter.Dress` |
| the API boundary that makes it | `angelus.handlers.dress` / `dressParams` |
| epochs, snapshots, eviction, taints | `internal/outfit/resolver.go` |
| closures, layers, `fileName`/`dirName` | `internal/outfit/outfit.go` |
| the additive diff | `form.Additive` |
| birth, and the default | `angelus.handlers.create` |
| fold on a fork | `angelus.handlers.fork` |
