# Amendment — the continuo, the identity segment, and the paged mem log

Ratified 2026-09-09 by Gluck, amending
`plans/reactive-queue-and-state-forms.md`. Where the two disagree, this wins.

---

## 1. The primitive: **continuo**

> we should come up with a primitive name for builtin forms like these, that
> are effectively bound to another form.

**A continuo is a non-persistent builtin form bound to a host form, published
by the harness and addressed as a named segment of its host.**

The word is *basso continuo*: the part that sounds continuously beneath the
work, **realized live from figures and never written out in full**, and
meaningless apart from the piece it accompanies. Every clause of that is the
primitive: continuous, derived, ephemeral, bound.

It earns its place by the contrast it draws with the primitive already in the
tree. `store.Libretto` (`internal/store/libretto.go:50`) is *"a derived form
with exactly one source"* — refcounted, stumped, and **durable**. That is
nearly this shape, and the difference that matters is persistence:

> **The libretto is written down. The continuo is realized in performance.**

A reader who knows one now knows what the other is *for*, which is the whole
job of a vocabulary word.

- Plural: **continuos**. (Italian would be *continui*; the codebase is
  English-plural elsewhere — `librettos`, `arias` — so it matches.)
- A continuo has a **name** (`queue`, `runtime`) and a **host** (a form).
- A continuo is not mintable, forkable, bindable, or listed by `form ls`. It
  is a projection its host publishes, and it dies with its host.

Runners-up, recorded so the choice is arguable: **obbligato** (literally
*bound*, and an obbligato part may not be omitted — the most precise word,
but nine letters and easily misspelled) and **gloss** (an annotation bound to
a text; correct and clean, but literary where the neighbours are musical).

## 2. The segment grammar, and `/state` as its identity

```
<host>/<name>
```

| address | resolves to |
|---|---|
| `<id>` | **shorthand for `<id>/state`** |
| `<id>/state` | the host's own form |
| `<id>/runtime` | the runtime continuo |
| `<id>/queue` | the queue continuo |

`state` is **reserved and implied**. It is not a continuo; it is the segment
that names the host itself, and it is elided by convention so that

```sh
fig form show <id>          # identical in every respect to
fig form show <id>/state
```

What "the host's own form" means, per host kind — Gluck's words, and *that's
it*:

- **a bound figaro** → its bound form (its board);
- **an unbound form** → that form's own state;
- **a role** → the role form's own state, **not** its target's.

**The third case is already the behaviour**, which is the best possible news
for it. `openFormView` resolves through `resolveTargetEndpoint`
(`internal/cli/target.go:19`), which does *not* redirect; only
`resolveFigaroTargetEndpoint` (`:80`) calls `redirectRole`. So `fig form show
@role` shows the role form today. `/state` does not change the semantics — it
**names** them, and turns a rule you had to know into one the grammar states.

Two things fall out, and both are why this spelling is right:

1. **The grammar loses its special case.** `<id>` is no longer a different
   kind of address from `<id>/queue`; it is an abbreviation of a sibling. The
   host is simply the segment that is not a continuo.
2. **The wire loses its special case too.** `rpc.FormDelta.Continuo string`,
   `omitempty`; `""` means `state` means the host's own form — which is
   exactly what every delta on the wire means *today*. So the field is
   backward-compatible **by the same rule that makes the shorthand work**,
   rather than by a coincidence of encoding. (This replaces the plan's
   `Facet` field; `continuo` is the word, and the empty value now has a
   meaning rather than being merely a default.)

The turn-disposition continuo is therefore **`/runtime`**, not `/state`. Its
key set is unchanged from the plan's §2.3, minus the name.

## 3. The mem log is paged, not a ring

> the memformlog should have a floor, like one page, however long that is.
> when it fills, the snapshot should be taken, and new appends happen on that
> snapshot, and the old form is held until the floor is reached. It may be
> deleted when the floor is fully subsumed by the new segment file.

The plan proposed a ring that drops old records and forces the reader to
resync over the wire. **That is worse and is withdrawn.** A ring throws away
the ability to answer; a page keeps a base to answer *from*.

```go
// A page is a base and the patches applied since it.
type memPage struct {
    base        form.Snapshot // state as of baseVersion
    baseVersion uint64
    patches     [][]byte
}

// MemFormLog retains at most two: the live page and its predecessor.
type MemFormLog struct {
    live   atomic.Pointer[memPage]
    sealed atomic.Pointer[memPage] // nil once subsumed
}
```

- Appends land on `live.patches`.
- When `len(live.patches) >= memFormPage`: **seal**. `sealed = live`, and a
  fresh `live` opens with `base` = the state at the seal and `baseVersion` =
  the version there. New appends run on that snapshot.
- `sealed` is dropped when `len(live.patches) >= memFormPage` again — i.e.
  when *the live page alone satisfies the floor*. That is Gluck's "fully
  subsumed by the new segment file", stated in the log's own terms.
- Therefore resident patch history is always in **[page, 2·page)**, plus at
  most two snapshots. Bounded, and never below the floor.

**Why this is better than the ring, concretely.** It reconciles the mem log
with the two-tier design `Form` *already* has. `Form` trims its resident
decoded window to `patchWindow` and records the cut in `st.trimmed`; a read
below the cut falls back to `patchesFromLog` (`form.go:190-224`). For a backed
form that fallback hits the durable log. For a mem form it hits `MemFormLog`
— so the floor is exactly what makes that existing fallback *correct* rather
than accidentally-total. And because a dropped page leaves a **retained base
snapshot** behind it, a reader that falls off the end can be reseeded
**locally, from the log**, with no `figaro.form` round trip.

Two consequences to implement rather than assume:

- Expose `Base() (form.Snapshot, uint64)` so the reseed path can take the
  retained base instead of refetching.
- `PatchesBetween` currently returns a bare slice: when `patchesFromLog`
  fails it falls through to `patchRange` and answers **short, silently**
  (`form.go:196-207`). Today that is masked because a mem log never trims. The
  moment it does, a short answer becomes a real gap. The client would catch it
  (`formMirror` resyncs on version discontinuity) but nothing would *say* so.
  **Report it explicitly**; do not lean on the client noticing.

`memFormPage` is the floor, in records. It should be config-visible beside
`memory.form_patch_window` (`config.go:444`), which is the same idea one tier
up and should be documented as such.

## 4. Everything else in the plan stands

Approved as written: the two continuos and their key sets, the inbox as the
one publisher, the widened `QueueState`, retention of `committed` items,
`turnStatusSending`/`turnStatusAccepted` with a distinct glyph family, the
death of the poll, and the deletion of
`proposals/status-queue-proposal.md` §2.3.
