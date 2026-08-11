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
fig form new -O name=deploys -j        # mint state → {"form_id":"@a1b2c3",…}
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

Deeper: `skills/figaro/reference/forms.md` (all semantics, both
personas), `forms-design.md` and `roles-design.md` (design), and
`plans/forms-and-roles-v2.md` (the governing brief). Study and cast
(`fig study`, `fig cast`) are specified there but not yet built.
