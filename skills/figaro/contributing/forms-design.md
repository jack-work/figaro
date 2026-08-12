# Forms: the design

Why the primitive is shaped this way, and what a change to it must not
break. For someone editing `internal/store`, `internal/angelus` or the form
family in `internal/cli`. If you want to USE forms, read
[../reference/forms.md](../reference/forms.md) first; if you want the outfit grammar, read
[../reference/outfits.md](../reference/outfits.md).

Spec of record: `plans/forms-and-roles-v2.md`. This file is the distilled
design, kept current with the code.

## 1. One primitive, one verb, nothing converts

```
null form ──fork+patch──▶ unbound form ──fork+patch⊇{aria_id}──▶ figaro
```

- **Null form** is the ur-form: the store's root, empty state, markerless.
  Unattendance is attendance of null. Whether the pid map records a null
  binding or an absence is an implementation detail.
- **Unbound form** carries kind `form` in the figwal node marker. Patchable,
  independently forkable, bindable, studiable.
- **Bound form is a figaro.** Born by forking an unbound form with a birth
  patch containing `aria_id`. One public id: the aria id IS the bound form's
  id.
- **Role** is a duck type, not a kind: an unbound form carrying
  `target-aria`. See [roles-design.md](roles-design.md).
- **Outfit form** is a usage, not a type: any form used as a forking point.

The reason nothing converts is that kind must be immutable by construction.
If a form could become a figaro in place, every reader would have to handle
a node whose species changed under it, and the marker would need a write
path. Binding forks instead, so kind is decided once, at node creation, and
the index can read it without opening heads.

The special case inventory is CLOSED. Null is the only special node. The
only other distinctions are bound versus unbound, and cast versus uncast.
A change that adds a third kind is a change to this design, not an
implementation detail.

## 2. Identity

Unbound forms mint with the `@` sigil. Bound forms have none. Legacy stump
ids (`@<hash>`, `<name>@<hash>`) already read as form ids, so they simply
ARE legacy forms: bindable as-is, no rename, no id migration.

Internally an aspect is `(node-id, channel)`, and the channel-major layout
(`arias/form/<node>`, `arias/ir/<node>`) is what discriminates the form
address from the IR address. Publicly there is one id for every operation.

## 3. The single writer, and what it reduces

`store.Form` is the one writer per node, whichever path reaches it: an
agent writes through `backend.ApplyFormIf`, and an agentless node is served
by the hub through the same call. There is never contention, because
binding forks.

The writer REDUCES before it appends. A patch is only an event if it
changes something: keys already holding the value asked for are dropped,
removals of absent keys are dropped, and a patch that survives none of that
appends no record, moves no version and fans out no delta.

That rule lives in the writer and nowhere else, for two reasons that are
easy to rediscover the hard way:

1. It is the only place the diff is ATOMIC with the append. Filtering in a
   handler reads, then filters, then writes. Two shells, one setting `a=2`
   and one setting `a=1` against a board holding `a=1`, and the second
   silently drops the write that should have won.
2. It is the only place both write paths pass through. Two implementations
   of "already wearing it" is how the agent path and the hub path came to
   disagree, which mattered most for observation: a no-op patch on a role
   moved its version, and every observing aria derived a transition
   announcing nothing.

`ApplyEffect` returns what actually landed. A caller that reports to a human
or fans a delta out to listeners must speak about the reduced patch, not
the requested one.

## 4. Outfits resolve ABOVE the writer

Outfitting is figaro API, not part of the reduction core. Every request that
carries dressing carries it as NAMES in an `outfits` field, beside a patch
that is pure data, and exactly one call at the daemon's API boundary turns
the first into the second (`angelus.dress`, plus `dressParams` for the
methods that reach an aria through its hub).

Below that boundary nothing reads a file. `layers` is respected in exactly
one place: the unmarshal that builds a patch from an outfit FILE. Written
into a patch it is ordinary data.

This is not tidiness. While the hub's write path applied patches verbatim
and the agent's did not, `fig form outfit test` stored `{"layers":["test"]}`
on a board and reported success, because an attended form has no agent and
takes exactly that path.

## 5. Hub-hosted forms

A node with no agent is served by the hub: reads from the store, and `set`
from the store's writer, with no wake. Three things depend on it.

- An unbound form has no agent and never will.
- A DORMANT figaro takes a patch without being restored, which is also what
  breaks the naked-figaro deadlock: `fig bind null` mints a figaro whose
  first turn fails for want of provider keys, and this is the only path
  that can patch those keys in.
- `set` stops waking sleepers generally.

The care point is the writer handoff at wake: the hub's Form closes before
the agent's opens, both in-daemon and actor-serialized.

## 6. Fork under a form mints a new trunk

`ForkTail` is continuation: the child keeps the trunk id, because the aria
id IS the trunk id. Binding and form-forking need the spawn-beneath shape
instead, generalized from stumps to arbitrary form nodes. This is the
sharpest store-level edge in the whole design; a change here is a change to
lineage, listings and recency at once.

## 7. Recency, listings, GC

Recency is figwal's `LastTS`, a retained atomic counter read per node. It is
not a sidecar field and must not become one again.

The listing reads lineage and kind from the figwal index without opening
heads. That is what keeps `fig ls` off the 300ms path, and it is fragile in
a specific way: any per-row read that opens a node turns a listing into a
scan. One did, once, and cost +6784% at 300 arias.

GC collects forms that are unreferenced AND unbound AND not the default.
Legacy stumps stay readable and are listed as legacy.

## 8. Observation is pull at the stamp

An aria observes a SET of forms. Its own board is member zero, the fork it
was born restudying; studied forms are the shared members.

Every IR append stamps the whole set's positions into ONE cursor map
(figwal `AppendMainCursors`). The provider translator derives each member's
patch-fold between consecutive stamps and folds it into the provider IR
exactly as it folds the form's own transitions. It is re-derived on
every retranslate and never baked into the aria's records.

Consequences worth stating, because they look like bugs if you do not know
them:

- Observation is SAMPLED at main-record boundaries. The stamp is the moment
  of observation. There is no push, no pending queue, no watcher.
- A window can contain many patches to one key. The renderer folds them to
  the value the key ends at; see [roles-design.md](roles-design.md) for why
  that is not optional.
- Study and drop are stated IR marks, so a replay can account for when
  observation began.
- A form removed while observed renders a tombstone.
- The translator asks each member for an ABSOLUTE range, `(after, upTo]`,
  and the store answers with a read-only VIEW into an immutable, published
  patch array. It used to answer with a copy of the whole history, once per
  member per Send: `plans/form-view-perf.md` has the measurements and the
  reason a bounded question was being answered at unbounded cost.
- What each event SAYS beyond the fact is `system.study_incantation` on the
  observer's own board. See §8c.

## 8c. Incantations

The machinery states facts; what a fact MEANS to a particular figaro is not
the machinery's business. `system.study_incantation` is an object with any
subset of `onstudy`, `onupdate` and `ondrop`, each a string, and the matching
phrase rides that event's block as a `say` field.
`system.fork_incantation` is the same for a branch's birth, spelled either as
a bare string or as `{"onfork": …}`.

Three properties, each load-bearing:

- **The bound form only.** A studied form does not get to put words in its
  observer's mouth. All system settings live on the observer's own board and
  this is no exception.
- **Tolerance is asymmetric.** A malformed incantation costs its own phrase
  and nothing else: a non-object is refused wholesale, a non-string field is
  dropped alone, an unknown key is named in the log, and the block it
  decorates still renders. A strict parse would mean one typo in a shared
  outfit silently breaking every aria wearing it.
- **Fork's default is silence, and stays silence.** `form.Render` skips the
  whole `system.` namespace, so a forked aria was never told it was forked.
  The incantation is what overrides that; with no key set, nothing changes
  for anybody.

The warnings go to the daemon log, which nobody reads. There is a
`TODO(notifications)` at the site: they belong in a verb a human can run.

## 8b. The default form, and what an upgrade does to it

`fig new` reuses one default form, and that reuse IS the prompt cache: every
aria minted from the same node shares its rendered prefix. So the pointer is
reused with NO comparison while it is clean, and `fig outfit reload` is a
flag rather than a computation.

That cheapness has one hole, and it is the shipped skills. An upgrade
replaces the first-party skill the binary carries (it is embedded, unpacked to
a content-hashed directory under the state dir, and the loader merges it OVER
a user's copy of the same name), but nothing in the store changed, so a clean
pointer would go on minting arias wearing the skills of the build you
replaced, until somebody happened to run `fig outfit reload`.

The default-form record therefore carries the BUNDLED ROOT it was minted
against. That root is named by the CONTENT HASH of the embedded tree, so it
moves exactly when a skill changed; a daemon booting with a different one
marks the record dirty. It only sets the flag: whether anything is reminted is
still decided by the birth-hash comparison, so a build whose skills are
byte-identical keeps the same form and the same cache.

Two consequences to know:

- The DAEMON MUST RESTART for any of this. It is the daemon that reads the
  skills, and an old daemon holds the old ones. The build-identity guard
  already forces the restart, because a new CLI refuses to pair with a daemon
  of another build.
- EXISTING arias keep what they were born wearing. A form is per-aria state,
  and no upgrade may silently rewrite it. `fig state outfit <name>` re-folds
  the new skills onto a live aria, additively.

## 9. Deliberate absences

- There is NO `fig outfit write`. Outfit files are one-way sources of truth,
  possibly git-tracked, and no path may serialize form state back onto
  them.
- Forms are NOT content-addressed. Every form has its own minted id. The
  content hash survives only as `fig new`'s reuse optimization for the
  default form, which is also what shares the rendered prefix in the
  provider's cache.
- Nothing converts. If you find yourself wanting a `promote` from form to
  figaro, you want a fork.

## 10. Invariants

A change that breaks one of these is a change to the design and needs to be
said out loud.

1. Kind is decided at node creation and never mutates.
2. One writer per node, whichever path reaches it.
3. A patch that changes nothing is not an event.
4. Outfit names are resolved above the writer; the core reads no files.
5. `layers` is respected only in an outfit file.
6. Listings read the index, never the heads.
7. Recency comes from figwal, not from a sidecar.
8. Observation is derived at translate time and never stored in the
   observer's records.
9. The default form is reused while clean, and an upgrade that moves the
   bundled skills marks it for recomputation.
10. A form patch read answers a bounded question at bounded cost: the store
    returns a view, never a copy of the history.
11. An incantation is decoration on a fact. A malformed one costs its phrase
    and never the fact, never the block, never the turn.
