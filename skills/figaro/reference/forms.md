# Forms: durable state, with and without a figaro

An **unbound form** is durable, versioned, forkable state with no agent
attached: a JSON tree whose every change is a patch in an append-only
channel. A figaro's chalkboard has always been exactly this — the only
news is that the primitive now stands alone. One id namespace covers
both: an id with the `@` sigil (`@a1b2c3`) is an unbound form; a bare
hex id is a figaro, which IS its bound form. Nothing ever converts
between the two — **binding forks**, and the form goes on living.

```
null form ──fork──▶ unbound forms ──fork+aria_id──▶ figaros
```

`state` and `form` are one command; both spellings work everywhere.

---

## Using forms without a figaro (state tracking)

**Mint.** `-O` is required — a form is born of its patch, and `fig form
new` never touches the default outfit:

```sh
fig form new -O name=deploy-tracker -j     # → {"form_id":"@a5af1a83",…}
fig form new -O 'focus,ttl=1h'             # an outfit spec works too; the
                                           # daemon MATERIALIZES it — the form
                                           # holds skills/credo, never a raw
                                           # layers directive
```

**Read.** `fig form @a5af1a83` prints the nested tree; `-j` is already
its shape. `fig form listen @a5af1a83` watches it live (deltas ride
`form.delta`; reconnect resyncs).

**Write.** The same verbs an aria's board uses:

```sh
fig set --id @a5af1a83 status.phase canary
fig unset --id @a5af1a83 status.phase
```

Or attend the form and drop the `--id`: `fig at @a5af1a83` binds your
shell to it (kind-agnostic; `fig at null` goes home). Writes are served
by the daemon without waking anything — a form has nothing to wake.
Concurrent writers serialize through the form's single writer;
conditional writes (`if-version`) refuse when the board moved.

**Fork.** `fig form fork @a5af1a83 who=trial` duplicates the state at
that moment into a fresh `@id`. The parent stays fully patchable; later
patches to it belong to it alone; a later fork takes the later state.
A fork must carry a patch — a fork nobody can name is refused.

**List.** `fig form ls` shows FORM / NAME / TARGET / AGE / PARENT,
scoped by attendance: attending a form shows its subtree, attending a
figaro shows its nearest unbound ancestor's tree, attending nothing
shows everything. `fig ls -g` is the full genealogy — null at the root,
forms as grey-washed rows, figaros at the tails. Plain `fig ls` never
shows forms. Recency (AGE) is the newest record timestamp anywhere in
the node, read without waking anything.

**Remove.** `fig form rm @id` (`--recursive` to take live branches).

**What a plain form refuses.** Turn-shaped verbs, with the remedies:

```
$ fig send --id @a5af1a83 -- hello
error: @a5af1a83 is a form, not a figaro: this verb needs one.
  fig bind @a5af1a83                 birth a figaro from it
  fig set --id @a5af1a83 target-aria <aria>   make it a ROLE, and this verb reaches the holder
```

---

## Using forms beside your figaros (integration)

**Bind: a figaro born of your form.**

```sh
fig bind @a5af1a83 -O focus     # fork the form; its state is the figaro's
                                # inherited prefix; prints both ids
fig bind                        # bind from the ATTENDED form
fig bind null                   # the naked figaro (below)
```

Bind **never rebinds your shell** (attend the printed id yourself) and
**never starts an agent**: the figaro is born dormant and wakes on first
use. That is where a missing provider fails — so `bind null` mints
fine, refuses its first turn, and is repaired by patching the provider
in through the same dormant write path:

```sh
fig bind null -j                        # → {"figaro_id":"dcb89d6b",…}
fig set --id dcb89d6b system.provider anthropic
fig set --id dcb89d6b system.model claude-fable-5
fig send --id dcb89d6b -- hello         # first turn wakes it for real
```

**`fig new` is bind, sugared.** It forks the daemon's **default form**
(the pointer in `default_form.json`), binds, and attends — the one
attendance-moving birth. Reusing the same default-form node across
creates is what shares the rendered prefix and the provider's warm
cache; that reuse is deliberately load-bearing.

**`fig outfit reload`** flags the default form dirty and reads nothing;
the compute lands on the next `fig new`: unchanged files and an
untouched form no-op, a moved file hash remints, and a hand-patched
default form remints too — the lifecycle refuses to propagate an ad-hoc
patch to every future aria. There is deliberately **no `outfit write`**:
outfit files are one-way sources of truth.

**A figaro's board is a form with a turn loop attached.** `fig set` on
an aria projects the patch as a `<system-reminder>` into its next turn;
on a *dormant* aria the write lands without waking it. `fig form
listen <aria-id>` watches a figaro's board exactly as it watches a
form.

**Scripting.** Every form verb takes `-j`:

```sh
id=$(fig form new -O name=jobqueue -j | jq -r .form_id)
fig set --id "$id" jobs.next '"deploy-42"'
fig bind "$id" -j | jq -r .figaro_id
```

---

## The verb × species matrix

The canonical answer table. **Species is fixed at birth** (figwal node
marker); the resolver checks it before anything else.

| verb | `null` | unbound form `@x` | figaro (bound form) |
|---|---|---|---|
| `fig form new -O` | forks it (that is what new is) | `form fork @x` duplicates | **refused**: "not an unbound form" |
| `fig bind` | naked figaro | figaro inheriting `@x` | refused (use `fig fork`) |
| `fig fork` | — | `form fork` | conversation fork (as ever) |
| `fig set` / `unset` | — | writes, wake-free | live: reminder next turn; dormant: wake-free |
| `fig form` / `state` | — | the tree | the board |
| `fig form listen` | — | live deltas | live deltas |
| `fig send` / `q` | attended-null: `fig new` path + autobind | role: **follows `target-aria`**, once, late, per call; plain: refused with the two remedies | the turn, as ever |
| `fig listen` | — | role: follows the target (banner `role @x → aria y`); plain: refused | transcript |
| `fig at` | home (drops binding) | attends the form | attends the figaro |
| `fig ls` | header | **never shown** | rows; form-born arias show `@parent` in OUTFIT |
| `fig ls -g` | root | washed rows | tails |
| `fig form rm` / `kill` | — | removes | removes |

## Roles

A form carrying a **`target-aria`** key is a **role** — a stable name
for "whoever holds this job now". Duck-typed: the key IS the type, and
only unbound forms count (the key is inert on a figaro's board).

**Resolution is late, and per call.** `fig send`, `fig listen`, `hup`,
and the queue verbs against a role read the form THEN — a banner says
`role @x → aria y` — and reach whatever the key names at that moment.
Two invocations with a repoint between them land on two different
arias: that is the succession property, not a race. Rules that keep it
honest:

- **The `form` namespace never redirects.** `fig form @role`,
  `form listen`, `set`, `at` address the role ITSELF — how you watch
  and repoint it. Attendance binds to the role's id; only the
  turn-shaped verbs chase the target.
- **Roles do not chain.** A `target-aria` naming another form is
  refused, not followed.
- **An open `fig listen` does not chase a repoint** — reconnect to
  re-resolve.
- Each role-targeted invocation costs one wake-free form read.

```sh
fig set --id @role target-aria dcb89d6b       # repoint on succession
fig send --id @role -- status?                # reaches the holder, whoever that is now
```

**Study and cast.** `fig study [<aria>] <@form>` subscribes a figaro to
an unbound form; `fig drop` unsubscribes; `fig cast [<aria>] <@form>`
(or `-O <spec>` to mint the role born cast) points the role here AND
ensures the study, serialized through the figaro's actor loop. The
mechanism is the bound board's, generalized: every IR record stamps the
positions of the whole OBSERVED SET (own board plus studied forms), and
the provider translator derives each member's patch-fold between
consecutive stamps at translation time — folded into the provider IR
exactly as the chalkboard's transitions are, re-derived on every
retranslate, never baked into this aria's records. Began/stopped
observing are stated IR marks; a studied form removed mid-observation
renders a tombstone; observation advances at main-record boundaries
(the stamp IS the moment of observation).

---

*Design depth: `reference/forms-design.md` and `reference/roles-design.md`
(storage, hub routing, lifecycle internals). Spec of record:
`plans/forms-and-roles-v2.md`.*
