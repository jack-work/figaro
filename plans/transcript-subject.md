# The transcript's subject

> **STATUS: SCOPING. Nothing here is built, and one section needs a
> conversation before anything is.** Written 2026-08-23, after Gluck read
> [transcript-composer.md](transcript-composer.md) and reframed the work:
>
> > *"I think perhaps I want a full design, but the pattern of switching between
> > arias in transcript mode doesn't exist. I think in terms of work we need to
> > implement this pattern first. […] This is a fundamental switch to the cli and
> > I want to be careful about introducing it."*
>
> He is right, and it reorders everything. This file is the larger body of work;
> the composer plan becomes its last and smallest phase.

## The finding that reorders it

Every feature under discussion is blocked on one missing primitive.

| what Gluck wants | what it needs |
|---|---|
| fork at a node and follow the branch | change the transcript's aria |
| attend a **role**, and follow the bearer when it is recast | change the transcript's aria |
| `:send <aria-id> -- "text"` from the pager | address another aria |
| `:open <id>` | change the transcript's aria |

**The transcript cannot change its aria.** The id is baked in at construction,
at three layers: the connection (`sdk.DialAria` on one per-aria socket), the
store (`aria.Client`, ranges keyed by that aria's coordinates), and the view
(window anchor, row cache, selection, and the status line's own metrics).
Nothing in the program can move it, and `figaro listen <id>` is the only door.

So the insert-mode composer is **not** the first piece of work. It is the last
one, and by then it is small.

## 1. The subject is a REFERENCE, not an id

The generalisation that makes role-following free rather than special:

> The transcript does not show an aria. It shows a **subject**: a reference that
> RESOLVES to an aria, and that may resolve to a different one later.

| subject | resolves through | changes when |
|---|---|---|
| `aria <id>` | itself | never |
| `role @form` | the form's `target-aria` key | someone runs `figaro cast` |
| `attend` | this shell's binding | someone runs `figaro attend` |

Role-following is then not a feature, it is the second row of a table. The
machinery is already here: `target-aria` is an ordinary form key
(`study_hub.go` patches it on `cast`), the CLI already renders a role as
`role → <aria>` (`manage.go:394`), and **the form-delta renderer already has a
special case for `target-aria` changing** (`formdeltas.go:125`). A client that
studies the role form is told, in the ordinary delta stream, the moment the
bearer changes. What is missing is only the *act*: switch.

Gluck's own words for the bug this fixes: *"Today, the transcript retains the
former role bearer."*

## 2. What a switch swaps, and what it may keep

```go
// switchSubject repoints the transcript at whatever ref resolves to now.
// EVERY path that changes what is on screen goes through here: :open, a fork
// we follow, a role recast, an attend elsewhere.
func (in *interactiveInput) switchSubject(ref subject) error
```

Three layers change: **connection** (dial the new aria's endpoint, close the
old), **store** (`aria.Client`), **view** (anchor, row cache, selection, status
metrics). The interesting question is the fourth thing:

> **What may the new subject keep from the old one?**

| switch | may keep |
|---|---|
| same aria | everything (it is a no-op, and must be cheap enough to prove it) |
| a fork of what we are showing | **every turn below the fork base** |
| an unrelated aria (role recast to a stranger) | nothing |
| two arias sharing a distant ancestor | their common prefix |

All four rows are one rule: **keep the common prefix of the two lineages.**

## 3. And this is the part that needs a conversation

Gluck:

> *"the data structure that embeds the transcript window should retain the
> common prefix if a fork is used. I suspect that logic may need be encoded into
> the ui ir api. We will want to talk about this."*

He is right that it does not exist, and right about where it belongs.

**The client cannot compute a common prefix, because it does not know lineage.**
It knows an aria id and a bag of turns. Two arias could share nine tenths of
their history and the client has no way to be told so.

**The server already knows, and already relies on it.** From e54b6299:

> `Angelus.composeTurns(node, fromLT, toLT)`, keyed by node and backed by the
> store, answers for a live aria, a dormant one, and an ANCESTOR NOBODY HAS
> OPENED, which is exactly the read a fork's inherited prefix makes.
> `TurnCache.put` now skips any turn below its node's fork base: **the prefix is
> read through tree's lineage walk instead of copied.**

That commit makes, internally, precisely the claim the client needs: *for turns
below the fork base, the branch's composed turns ARE the ancestor's.* The UI IR
API simply never says it out loud.

### The invariant that makes the client's half trivial

The same commit's canary:

> THE FORK BASE IS SNAPPED DOWN TO A TURN BOUNDARY, and the canary says that
> line is load-bearing: a fork cutting mid-turn leaves the child a turn made of
> the ancestor's opener and its own continuation, same turn id, different
> content.

Because the base is snapped, **the shared prefix is coordinate-identical**: same
turn ids, same node ids, same bytes. So the client's retention rule needs no LT
translation and no new key, the row cache is already keyed by `(turn, node)`:

> **Keep `turn < baseTurn`. Drop at-or-above.** The turn you forked at is the
> turn that differs.

### Three shapes for the API, and the one I would take

**A. Lineage rides the read.** `aria.Page` grows a field:

```go
// Ancestry is one link of the chain: the node this aria's history is inherited
// from, and the FIRST TURN this aria owns. Turns below base are the ancestor's,
// byte for byte, which is what lets a client keep them across a switch.
type Ancestry struct {
    Node string `json:"node"`
    Base uint64 `json:"base"` // first turn this aria owns; 0 = a root
}
```
carried root-ward as a slice. The client computes the common prefix locally.
**No extra round trip**, the switch already performs a read, so there is no
window in which the client holds the page but not the lineage. Static per aria,
so it can ride only the first page of a connection.

**B. An explicit `figaro.lineage(id)` RPC.** Cleaner separation, no page bloat,
one more round trip at switch time and a second thing to keep coherent with the
tree.

**C. `figaro.retainable(from, to) → turn`.** The server answers the client's
question directly. **Reject**: it encodes a *client cache policy* on the server,
and the server would have to know what the client is holding to answer honestly.
The seam is in the wrong place.

**Recommendation: A**, with the type defined once so B remains available if page
size ever matters. The deciding argument is the no-window property: a client that
can hold a page whose lineage it has not yet learned will eventually render one
aria's rows under another's coordinates, and that is the fabricated-adjacency
bug class the range store exists to prevent, at aria scale.

**Questions for that conversation, in the order I would ask them:**

1. Is `Ancestry` a *chain* (root-ward slice) or just the immediate parent? A
   chain lets two cousins share a grandparent's prefix; a parent link only
   serves the direct fork case. The direct case is 95% of the value.
2. Does the CONTINUATION of an interior fork carry ancestry too? It is also a
   child of the frozen parent, so following it should be as cheap as following
   the alternative.
3. Is `Base` a turn or an LT on the wire? Turn is what the client can use
   without translating; LT is what the store natively holds. I want turn, and I
   want the snap to happen server-side where its canary already lives.
4. Does a **promote** (`figaro promote`) rewrite ancestry under a client that is
   holding it? If so the client needs to be told, or its retention is a lie.

## 4. Command mode, and converging the input controls

Gluck:

> *"I think we should support a command mode that lets us switch first. […]
> Perhaps we should focus on converging the multiple input controls that we
> already have. E.g. search via /."*

### One line editor, three acceptors

Today there are three input surfaces, two built and one proposed, and each
hand-rolls its own byte handling:

| surface | today | on accept |
|---|---|---|
| `/` search | `query += string(b)`, **ASCII only** | run a search |
| `:` jump | `jumpQuery += string(b)`, **ASCII only** | go to a coordinate |
| `i` composer | *proposed* | send a prompt |

They differ **only in what the buffer means when it is accepted**. So: one
editor, three acceptors, and the composer is the same widget with more rows.

**The editor should be `bubbles/textarea`, driven headless.** Gluck asked for
exactly this, *"Ideally we can use the same control for the insertion mode as
we do for compose"*, and it is achievable: a `textarea.Model` is an ordinary
value with `Update(msg)` and `View()`. It does not need a bubbletea program; the
transcript can feed it decoded keys and paint its `View()` into the footer
stanza. Then `composePrompt` and insert mode are literally the same control.

Two things this buys immediately:

- **The emacs/bash keys Gluck asked for, for free.** The vendored default
  `KeyMap` is already `ctrl+u` delete-before-cursor, `ctrl+k` delete-after,
  `ctrl+w` delete-word-back, `ctrl+a`/`home` line-start, `ctrl+e`/`end`
  line-end. Verified in `bubbles@v0.21.1-…/textarea/textarea.go:80-88`.
- **The non-ASCII bug dies.** Measured through the real dispatcher: typing
  `café` into the `/` box yields `query == "caf"`, both hand-rolled boxes
  reject `b >= 0x7f` and swallow the character silently. Every non-ASCII search
  in this program has always been wrong.

The risk to prototype early: `View()` renders its own cursor and styling, and
the transcript's painter owns the grid and diffs frames. If textarea's output
cannot be embedded cleanly, the fallback is a small rune-buffer widget of our
own with the same keymap, but *try the real thing first*, because "the same
control as compose" is worth a genuine attempt.

### `:` becomes a command line, exactly as vim's does

The jump box is subsumed with no user-visible regression, because **vim already
did this**: `:12` goes to line 12, `:w` writes. So `:12`, `:12.3` and `:0` stay
coordinate gotos, and everything else is a verb.

### The command language IS the CLI

> *"E.g. : colon for command mode -> :send <aria-id> -- "text" (same cli
> semantics)."*

This is the fundamental switch, and the care it deserves is one rule:

> **The transcript's command line runs the CLI's own parser. It is not a second
> dialect.** One grammar, two front doors.

What that costs, and it is the real work of this phase: the CLI's verbs are
today fused to their process wrapper, `WithAngelus(...)` for the connection,
`die()` for errors, `os.Stdout` for output. In-process they must instead return
errors and render into the pager. So the item is **separate every verb's body
from its wrapper**, which is a refactor with no user-visible change and a large
blast radius. It is also the thing that makes `:send` and `figaro send` provably
the same command, which is the whole point.

A first cut needs only the verbs that make sense with a transcript on screen:

| kind | verbs |
|---|---|
| move the subject | `:open`, `:attend`, `:fork`, `:cast` |
| speak | `:send`, `:queue` |
| navigate | `:12`, `:12.3`, `:0` |
| state | `:set`, `:unset`, `:state` |
| session | `:list`, `:kill` |

## 5. Ordering

Gluck's instinct, subject first, then command mode, then insert mode, with one
addition at the front, because it unblocks both of the others and fixes a live
bug on its way past.

| # | phase | why here | proves it |
|---|---|---|---|
| **0** | **converge the editor**: headless textarea, `/` and `:` become its one-line case | prerequisite for 4 and 5; deletes two hand-rolled paths | search for `café`, fails today |
| **1** | **the subject**: `switchSubject`, `aria <id>` only, as a true reload | the missing primitive; everything else consumes it | `:open <id>` moves the transcript |
| **2** | **lineage on the wire**, then retention across a switch | §3's conversation, then the client rule | count reads across a fork-follow: the prefix is not re-read |
| **3** | **role subjects**: resolve `target-aria`, follow on recast | falls out of 1 + the study feed already streaming | recast a role; the transcript moves |
| **4** | **command mode**: the CLI grammar in-process, `:send` first | needs 0 for the editor and 1 for `:open` | `:send <id> -- "x"` ≡ the shell's |
| **5** | **insert mode**: `i`, the box, `f`/`^F` fork intents | now a thin consumer of 0 and 4 | the composer plan's phases |

**Phase 5 is small if 0–4 land**, which is the argument for this order: insert
mode becomes "the editor from phase 0, with `maxRows 4`, whose default acceptor
is `:send` on the current subject". The feature Gluck asked for first arrives
last and cheapest, and nothing is built twice.

## 6. Gluck's rulings, recorded

From the round of questions on 2026-08-23:

1. **Esc keeps the box VISIBLE**, exits insert mode, and restores the scroll
   keybinds. (`^[` works by construction: it *is* `0x1b`, the same byte Esc
   sends, vim users say "ctrl-[" for exactly this reason.) The controls inside
   the box are **emacs/bash**, as `composePrompt` already is.
2. The box shows **one line by default and at most four** (revised down from
   five).
3. **`Alternative`** is the fork we attend and display.
4. A fork intent is **pinned**, but pressing `f`/`^F` again on a newly selected
   node **re-aims the intent and keeps the draft**. So the buffer survives
   re-aiming; only the target moves.
5. Sending mid-turn gets **no warning and no confirmation**: `Qua` goes out and
   the drain decides whether it opens a turn or becomes a steer. Confirmed.

And an answer owed to Gluck, who asked what I meant by "the inline view": **yes,
incipit mode**, the streaming view you get before the pager is up, where a
completed turn freezes into ordinary scrollback. It is the surface the shipped
smoke case `TestSmoke_LettersAreKeybindingsNotText` guards, which is why the box
does not exist there.

## 7. Open questions

Beyond §3's four, which are the ones that need the conversation:

1. **Does a retained prefix retain the SCROLL POSITION?** Fork at turn 47 while
   reading turn 12: I think you should still be looking at turn 12, because the
   rows under your eye did not change. That is a stronger claim than "the cache
   is warm" and it is the one a reader will actually notice.
2. **What happens to a role subject when the role form is deleted?** A tombstone
   arrives and the subject no longer resolves. Freeze on the last bearer and say
   so in the status row, or fall back to `attend`?
3. **Does `:open` push a stack, so `:back` exists?** Browsing a fork tree wants
   it. It also wants a `:tree` that is `figaro ls` rendered in the pager.
4. **Does `listen` still exit when its subject dies**, if the subject is a role
   that might be recast to a living aria a moment later?
5. **One process, one subject?** Or can the pager hold several and switch
   between them with the connection kept warm, which is `:back` made cheap, and
   an argument for not closing the old socket immediately.
