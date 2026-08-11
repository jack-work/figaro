# Forms, binding, roles, casting: the v2 brief

Supersedes Parts 2–4 of `plans/forms-and-roles.md`. Written from Gluck's
iterated spec of 2026-08-10 (aria f70189a8, checkpoint-forked). Part 8 of the
v1 brief (the @ completion defect) landed first as `7f9fee8` + `6045738`.

Everything in the v1 brief that is not superseded here still binds: the
testing regime (Part 6), the twelve mistakes (Part 7), and the Part 5
exclusion is REPLACED by the study/cast spec below (§7): commissioned as its
own milestone, not improvised early.

---

## 1. The model

**One primitive, the form; one verb, the fork; nothing converts.**

```
null form ──fork+patch──▶ any unbound form ──fork+patch⊇{aria_id}──▶ figaro
```

- **Null form**: the ur-form. The store's root, empty state; already
  "markerless, ceremonial" in `xwal_store.go`. It is an UNBOUND FORM, not an
  aria. **Unattendance ≡ attendance of the null form**; whether the pid map
  records a null binding or absence represents it is an implementation
  detail.
- **Unbound form**: kind `form` in the figwal node marker. Patchable,
  independently forkable (an unbound-only privilege), bindable, study-able.
- **Bound form ≡ figaro**: born by forking an unbound form with a birth
  patch that includes `aria_id`. Never by conversion. One public id: the
  aria id IS the bound form's id. `fig fork` on it forks the conversation.
- **Role**, a duck type: unbound form carrying `target-aria` (renamed from
  cast-aria). Mutually exclusive with bound: enforced at `cast` (refuses
  bound targets) and at resolution (`target-aria` is never read off a bound
  form; a hand-set key there is inert). The key itself cannot be forbidden;
  the exclusivity lives in the readers.
- **Outfit form**, a USAGE, not a type: any form used as a forking point.
  Stumps are dead as a concept; the old default-outfit stump becomes a
  proper form: patchable, forkable. Forms are NOT content-addressed: every
  form has its own minted id; the content hash survives only as `fig new`'s
  reuse optimization (§6). Outfit-born and hand-made forms are
  indistinguishable, and patching either follows uniform unbound-form
  semantics: attend it or target its id explicitly.
- **The special-case inventory is closed**: null is the only special node;
  the only other distinctions are bound vs unbound, and cast vs uncast.

## 2. Identity

- Unbound forms mint with the **`@` sigil**: `@<hex>`. Bound forms get no
  sigil. Legacy stump ids (`@<hash>`, `<name>@<hash>`) already read as form
  ids: they simply ARE legacy forms, bindable as-is, no rename, no
  migration of ids.
- Internally an aspect is `(node-id, channel)`; the channel-major layout
  (`arias/form/<node>`, `arias/ir/<node>`) is the ir-vs-form address
  discriminator. Publicly, one id for all operations.
- Kind discrimination rides the figwal node marker (`forkFlat` already takes
  a `kind` string; the index reads it without opening heads, which keeps
  listings off the 300ms path). Immutable by construction: binding forks.

## 3. The verbs

Every verb below supports `-j/--json` with created ids machine-readable.

### fig new
Sugar, not a primitive: ≡ `fig at null` + the send machinery minus the send -
fork the default form, bind a figaro, **attend it**. Reuses the default form
via the hash optimization (§6); this reuse is the one way `fig new` differs
from `fig form new -O default | fig bind`.

### fig bind [<form-id>|null] [-O <spec>]
- 0 args: bind from the **attended form** (error if attending an aria or a
  figaro-less shell attends nothing: that is null, see below).
- `<form-id>`: bind from that form. Legacy stumps included.
- `null`: the naked figaro. Mints fine: birth patch is `{aria_id}` plus
  whatever `-O` folds in, and bypasses the default outfit entirely. What
  fails is the first TURN, unless requirements (`system.provider`,
  `system.model`, …) arrived via `-O` or later patches. The existing error
  codes (`ErrNoProvider` −32011 and kin) fire at wake time.
- `-O` folds an outfit patch into the birth patch (bind is a fork; it rides
  free).
- **Never attends.** Output prints both ids: `bound <aria-id> from <form-id>`.
- **Never starts the agent.** Bind is a store operation; the figaro is born
  dormant and the hibernation revive path constructs the provider on first
  wake. That is also where `bind null`'s deferred failure surfaces.

### fig form new -O <spec> [k=v …]
`-O` is **required**: never the default outfit. Extra values fold on top;
the aggregate is the birth patch of a **fresh** `@id`. Always mints; no
dedup.

### fig at <id>  (attend)
Kind-agnostic: arias, forms, and `null` (= detach to baseline). Attendance
to an aria id is attendance to the figaro: same thing, one id.

### fig send / bare `q …`
- Attending **null** (or unattended: same thing): the default-aria
  optimization. Fork the default form, bind, **autobind attendance** to the
  new figaro, send. This is the ONLY autobind: attendance is null AND the
  send created the figaro. Fresh-shell muscle memory (`q hello` → new
  default figaro) is preserved exactly.
- Attending a **role**: redirect to `target-aria`.
- Attending a **plain unbound form**: error with a `fig bind` hint. This is
  deliberately a measure against accidental figaro creation (attendance is
  one global with two subtypes); it may be softened in the future.
- `fig send <aria-id>`: never rebinds the terminal; opens transcript mode
  unless `--forget`.
- Explicitly targeting a plain form: same error. Explicitly targeting null:
  creates the figaro, NO autobind (autobind requires attended-null).

### fig listen <id>
- Bound: as today.
- Role: redirect to `target-aria`; banner says `role @x → aria y`; a
  repointed target is not chased mid-listen (documented; re-run).
- Plain unbound form: **error**: `fig form listen` is required there.

### fig fork <id>
Bound → conversation fork (as today). Unbound → form fork (state duplicated,
no stump semantics). Only unbound forms fork independently.

### fig study [<aria-id>] <form-id>  /  fig drop
The form is always the LAST positional; with one arg the aria is inferred
from attendance. Unbound targets only. Registration is durable on the
aria's OWN form (`system.studies`): revival resubscribes; a fork inherits
the relationship, never a private copy. Study alone carries no
transactional guarantee.

### fig cast [<aria-id>] [<form-id> | -O <spec…>]
See §7.

### fig outfit reload
See §6. (There is deliberately NO `outfit write`: see §6.)

### fig ls / fig form ls
See §5.

### state
Strict drop-in alias of `form`, across all subcommands. Extend the
alias-collision canary (v1 mistake #11: `state` aliasing `form` is exactly
how a new subcommand gets swallowed silently).

### Positional grammar (cast/study)
The form is always the last positional; `-O` occupies the form slot. So:
two positionals = `<aria> <form>`; one positional + `-O` = the aria; one
positional alone = the form. Kind validation makes every slot self-checking
("`x` names a figaro, but this slot takes an unbound form"), and the `@`
sigil makes misuse lexical before it is semantic.

## 4. Attending a form: the verb table

| verb | attending an unbound form |
|---|---|
| `fig form` / `fig state`, `set`, `unset`, `form listen`, `form fork` | work (hub-hosted writer, §8) |
| `fig bind` (no args) | binds from this form |
| `fig send` / bare `q` | role → redirect; plain → error (`bind first`); null → default-fork + autobind |
| `fig listen` | role → redirect; plain → error (`form listen` required) |
| `fig fork` | form fork |
| queue / interrupt / turn verbs | named error: "@x is a form, not a figaro" |
| `fig cast` with no aria slot | no aria available → auto-fork path (§7) |

Role QoL: a `TARGET` column in form listings; `fig form <id>` header prints
`role → <aria>`; `fig status` on an attended role says so. No new verb
needed to tell.

## 5. Listings

### fig ls -g / --global: the canonical unbound-form view
The full genealogy as ONE TREE: null at the root, forked forms beneath it,
and bound figaros as forks of the unbound forms: figaro paths begin one
level below null or lower and run to the tails. Same tree semantics the ls
UI has today, extended to mixed kinds.

- `-n`/limit semantics unchanged: top N rows by interaction time, bound and
  unbound treated identically by the limit.
- **Unbound forms render with the opaque background color: the same style
  token the transcript TUI uses for node selection.** Figaros get the
  default background. One shared style constant, not a second color.
- Interaction time for a form = its last form-channel write (birth time if
  never patched). [interpretation: flag if wrong]
- Tree pruning under `-n`: connective ancestors that didn't make the top N
  are rendered as dim scaffolding and do NOT count against N.
  [interpretation: strict row-counting is the alternative; flag if wanted]

### fig ls / fig ls -a: the working view
Figaros only, as today, annotated with the id of each figaro's NEAREST
UNBOUND ANCESTOR so rows group by origin form. A forest: only subtrees that
contain figaros appear. Specified by Gluck for the attended-form case;
applied uniformly regardless of attendance so the view never shape-shifts.
[interpretation: flag if the annotation should appear only while attending
a form]

### fig form ls: fig ls -g, scoped
- Attending a form: only that form's hierarchy (its subtree).
- Attending a figaro: the hierarchy under its nearest unbound ancestor.
  [interpretation of "scoped to the attended figaro": flag if wrong]
- Attending null / nothing: one level below the root: the top-level forms,
  like today's outfit listing.

## 6. The default form lifecycle

- A daemon-tracked pointer (successor of `KeepStump`), plus a recorded
  birth-hash and birth-version.
- `fig outfit reload`: sets a dirty flag. Nothing else: cheap by design.
- Next `fig new` with the flag set: materialize the default outfit from
  disk, hash it (`contentVersion` exists), and REMINT the default form iff
  file-hash ≠ birth-hash OR the form was patched since birth
  (version > birth-version: cheaper and more honest than hashing live
  state, which has a normalization swamp). Same hash and unpatched → no-op,
  reuse. A remint happens even if an identical hash exists elsewhere: dedup
  is dead.
- There is NO `fig outfit write` (struck by Gluck, 2026-08-10): outfit
  files are one-way sources of truth: possibly git-tracked, and no
  path may serialize form state back onto them. That the files are
  editable at all is a side effect to be fenced off when access
  controls arrive; the absence of a write verb is a deliberate gap,
  not an omission.
- Direct patches to the default form: allowed, discouraged, DETECTED (the
  dirty-since-birth bit means the next reload-compute remints rather than
  silently propagating an ad-hoc patch to every future `fig new`).
- **The hash optimization IS the prompt cache**: reusing the same
  default-form node is what shares the rendered/translated prefix segments
  with every child. Remints deliberately cold it. Benchmarks must show
  bytes-per-aria and cold-vs-warm prompt cost before/after (v1 mistake #2
  is the standing warning here).

## 7. Study and cast: casting calls

Serialization point: **the figaro's actor loop processes casts serially.**
No dedicated queue, no parked wait, no park timeout: ordinary actor-call
timeouts apply. The comment at the mechanism SHALL note that these are,
literally, casting calls.

- `fig cast <aria> <form>` (server method; hub and agent share the daemon):
  1. Refuse bound targets (exclusivity rule).
  2. Enqueue into the figaro's actor loop: register the study subscription
     (skip if already studying the form), then CROSS-CALL out to the role
     form's writer to patch `target-aria = <aria>`. Safe by `store.Form`'s
     own contract: "Apply is safe to call from inside a turn"; the form
     writer does I/O and nothing else and never calls back.
  3. Reply. A later repointing of `target-aria` is fine by design.
- `fig cast … -O <spec…>`: TWO steps.
  1. Ensure the figaro: when no aria is available (unattended, or attending
     a non-aria), auto-fork one from the default form: the `fig new` path,
     unattended; its id is printed.
  2. In the actor loop, the figaro forks the NULL form with birth patch =
     outfit ⊕ `{target-aria: <aria>}`: the role is BORN cast; there is no
     separate patch step to half-fail.
  - If the figaro was created but the role fork failed: partial failure in
    the RPC response, spelled out on stdout. `-j` carries both outcomes.
- `fig study` / `fig drop` are callable independently, without any of the
  above guarantees.

## 8. Store & daemon mechanics

1. **Fork under a form must mint a NEW trunk.** `ForkTail` is continuation
   (the child keeps the trunk id: "the aria id IS the trunk id"). Binding
   and form-forking need the spawn-beneath shape generalized from
   `SpawnUnderStump` to arbitrary form nodes; figwal may need
   `SpawnChild(node)` exposed. The sharpest store-level edge in this brief.
2. **Hub-hosted `store.Form`** for nodes without an agent. Exactly one Form
   writer per node daemon-wide; kind decides the host (agent for arias, hub
   for forms); no contention because binding forks. `hubFor`/`requireAria`
   admit form ids; `MethodNeedsAgent(figaro.set)` becomes kind-aware.
3. **`set` on a DORMANT bound form is hub-served, without wake.** Breaks
   the naked-figaro chicken-and-egg (you must be able to patch in the very
   provider keys whose absence makes wake fail) and stops `set` waking
   sleepers generally. Care: writer handoff at wake: the hub's Form closes
   before the agent's opens; both in-daemon, actor-serialized.
4. **`form.delta` fanout from the hub**: one mechanism, three customers:
   `form listen` on forms, study subscriptions, and cast's cross-call
   visibility.
5. **The ceremonial IR birth record stays for now** (render anchor +
   inheritable translated prefix). Zero-IR unbound forms are the
   figwal-future note (§10), not this milestone.
6. **GC**: stump collection becomes form collection: collect unreferenced ∧
   unbound ∧ non-default; `fig form rm` for manual removal. Legacy stumps
   stay readable, listed as legacy.
7. **Listings**: tree assembly from the figwal index (lineage + kind, no
   head-opening); nearest-unbound-ancestor computed from the same index.
8. **Completion**: new completers for bind/cast/study/at slots (form ids,
   role marking): with the pid-strict lesson from `6045738` applied from
   day one.
9. **Downgrade note**: an older daemon reading a store with form-kind nodes
   lists them as arias. Cosmetic; recorded.

## 9. Milestones

- **M1: store + daemon**: form-kind birth; spawn-under-form; hub-hosted
  Form (set/read/delta, dormant-set-without-wake); bind never starts the
  agent.
- **M2: CLI surface**: `form new/ls/fork/rm`; `fig at` incl. null;
  attendance-as-null; `@` sigil; the three listings of §5 (the `-g` tree is
  the largest UI chunk); named kind errors; `-j` everywhere; state-alias
  canary.
- **M3: bind + new-as-sugar + default form lifecycle** (`outfit
  reload`/`write`, hash optimization, remint rules).
- **M4: roles**: `target-aria` resolution (send/listen redirect; the
  `form` namespace never redirects); role QoL columns.
- **M5: study/drop + cast** per §7.
- Every milestone: the v1 Part 6 regime: isolated pty runs of every new
  surface, the twelve-aria stress recipe, `-count=6` benchmark slopes,
  migrations rehearsed on a COPY of the real store.

## 10. figwal: in-scope work (authority granted to update master + release)

Gluck 2026-08-10: "you may update figwal master and release as you please.
embody it in this work."

1. **Mandatory server timestamps on EVERY record, in every channel, of
   every xwal**: LT, payload, and a real wall-clock timestamp recorded by
   figwal itself at append time (never caller-supplied). Form patches, IR
   records, translations: all of them. The JSONL envelope already carries
   `_idx`/`_hash` sidecars; a `_ts` sidecar (or a stamped-frame field)
   joins them. Reads TOLERATE legacy records without it (zero time); no
   segment rewrite: `_hash` covers the payload alone, so old bytes stand.
   This is what listing recency reads (§5).
2. **`SpawnChild` for arbitrary nodes**: the spawn-beneath shape
   generalized from stumps/root to any form node (§8.1). New trunk minted
   under a form parent.

## 10b. figwal-future (recorded, deliberately not commissioned)

figwal should tolerate primitives without unused channel baggage: a node
declares which channels it carries; the node marker moves out of `irDir`
(it is identity, not IR); an unbound form becomes ONE standalone reducible
form channel and nothing else. Possible only because figaro-ness is decided
at node creation: bind forks, nothing converts. Touches `forkFlat`/
`channelBases` (per-node channel sets), marker relocation + migration, and
`Flatten`.

## 11. Interpretations: RESOLVED (Gluck, 2026-08-10)

1. Form recency in listings: superseded by §10.1: every record carries a
   figwal-stamped server timestamp; recency reads that, uniformly, for
   forms and figaros alike.
2. `-n` tree pruning (uncounted dim scaffolding): confirmed: today's
   semantics are good enough for figaros, so they are good enough for both.
3. Nearest-unbound-ancestor annotation applies always: confirmed.
4. `fig form ls` while attending a FIGARO shows the nearest unbound form's
   tree: confirmed.
