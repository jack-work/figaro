---
name: forms
description: Getting started with figaro forms and roles: durable, versioned, forkable state with or without a figaro attached. Use when the user wants to track state in a form, make a role, bind a figaro from a form, or asks what @-prefixed ids are.
---

# Forms and roles: getting started

A **form** is durable, versioned, forkable state: a JSON tree with
history, stored in the same WAL an aria's conversation lives in. A
figaro's form IS one. An id starting with `@` is an **unbound**
form: no figaro attached, and none ever will be: making a figaro from a
form *forks* it, and nothing in the system converts.

## The five gestures

```sh
fig form new -S name=deploys -j        # mint state → {"form_id":"@a1b2c3",…}
fig set --id @a1b2c3 status.phase canary   # write (or: fig at @a1b2c3; fig set …)
fig form @a1b2c3                       # read; `fig form listen @a1b2c3` to watch
fig bind @a1b2c3                       # birth a figaro from it (dormant; never rebinds your shell)
fig set --id @a1b2c3 target-aria <id>  # now it's a ROLE: fig send @a1b2c3 reaches the holder
```

## The mental model, three lines

    null form ── fork+patch ──▶ any form ── fork+patch ──▶ figaro

- **Forks only, nothing converts.** `fig new` itself is sugar: bind the
  daemon's *default form* (minted from your default outfit, reused so
  every aria shares one rendered prefix, and one warm provider cache).
- **One id namespace.** A figaro is addressed by its bound form's id;
  the `@` sigil is how you tell the species apart. Attendance
  (`fig at`) is kind-agnostic, and unattended ≡ attending `null`.
- **Roles are duck-typed.** A form carrying `target-aria` is a role;
  verbs that need a figaro (`send`, `listen`) resolve it *at each
  invocation*: repoint the key and the next send reaches the successor.

## What a figaro SEES of all this

Four species, one id namespace, and only one of them reaches your context
uninvited:

| species | what it is | how you see it |
|---|---|---|
| **bound form** | your own board | one `<system-reminder name="<key>">` per key, every turn |
| **unbound form** `@x` | free-standing state, no figaro | not at all, unless you study it |
| **studied form** | an unbound form you observe | a `study` block when you begin, a `study:@x` block on each change |
| **role** | a form carrying `target-aria` | an ordinary form: visible only if you study it. `fig cast` studies it for you |

**On change** you get one `study:@x` block per user message, folded: several
patches inside one window arrive as a single block (`"changes":2`), and it
carries the value the key ENDS at, not the intermediate ones. A deleted source
arrives as `"exists":false`, its copy outlives it, so the history you were
shown still makes sense. Values are **not truncated**: a delta carries the
whole changed value, so keep studied values small if context matters.

**What configures it**

- `fig study @x` / `fig drop @x`, the subscription itself. It lives on your
  board as `system.studies`, which is system-managed: only the verbs write it.
- `system.study_incantation` on **your own board**: `{onstudy, onupdate,
  ondrop}`, a sentence added to each event so a figaro is told what the change
  MEANS to it, not just what changed.
- Nothing else. There is no per-key filter today: a studied form is mirrored
  whole.

**Recommended**: put a short primer in your **base outfit**, every key an
outfit sets becomes a reminder of that name, so every figaro you ever mint
inherits it. One key, a few lines, and no figaro has to be told twice.

## What refuses, and why

- `fig send @x` on a plain (non-role) form: *"@x is a form, not a
  figaro … bind it first."* Deliberate, a guard against accidental
  figaro creation.
- `fig bind null` mints a **naked figaro** that fails its first *turn*
  (not its mint) until you `fig set` a provider in: which works while
  it sleeps: patches to dormant figaros never wake them.
- `fig outfit reload` flags the default form; the next `fig new` re-reads
  the outfit files and remints only if they changed (or the form was
  hand-patched). There is deliberately **no** `outfit write`: the files
  are one-way sources of truth.

## Where things show

`fig form ls`: unbound forms (NAME, TARGET, AGE, PARENT), scoped to
what you attend. `fig ls`: figaros only; a form-born aria shows its
`@form` in the OUTFIT column. `fig ls -g`: the whole genealogy as one
tree, form rows washed in the selection grey.

Deeper: [reference/forms.md](reference/forms.md) (all semantics, both
personas, including `fig study` and `fig cast`, which are built and
documented there), and [contributing/forms-design.md](contributing/forms-design.md)
plus [contributing/roles-design.md](contributing/roles-design.md) for the
design. The governing brief, `plans/forms-and-roles-v2.md`, lives in the
repository and does not ship with the binary.
