# Amendment — the intrinsic, the identity segment, and the paged mem log

Ratified 2026-09-09 by Gluck, amending
`plans/reactive-queue-and-state-forms.md`. Where the two disagree, this wins.

---

## 1. The primitive: **intrinsic form**

> we should come up with a primitive name for builtin forms like these, that
> are effectively bound to another form.

**An intrinsic form is a form the harness publishes as PART OF a host rather
than alongside it.** Derived, never persisted, dead when its host is.

"Intrinsic" carries the property that matters: these are not attached to an
aria, they are properties *of* one — the way a turn's disposition is a property
of the turn rather than a document about it. Nothing mints them, forks them, or
sets into them.

It earns its place by the contrast with the primitive already in the tree.
`store.Libretto` (`internal/store/libretto.go:50`) is *"a derived form with
exactly one source"* — refcounted, stumped, and **durable**. That is nearly
this shape, and the difference that matters is persistence:

> **A libretto is written down. An intrinsic form is only ever live.**

A reader who knows one now knows what the other is *for*, which is the whole
job of a vocabulary word.

- Plural: **intrinsic forms**, or **intrinsics** where the noun is unambiguous.
- An intrinsic form has a **name** (`queue`, `runtime`) and a **host** (a form).
- It is not mintable, forkable, bindable, or listed by `form ls`.

*(An earlier draft of this plan called them "intrinsic forms", after basso intrinsic form.
Gluck rejected the operatic naming: the metaphor taught the wrong first guess —
"continuous" is a quarter of the meaning and not the load-bearing quarter,
while "bound and ephemeral" is the whole of it. The word is gone from the tree.)*

## 2. The segment grammar, and `/state` as its identity

```
<host>/<name>
```

| address | resolves to |
|---|---|
| `<id>` | **shorthand for `<id>/state`** |
| `<id>/state` | the host's own form |
| `<id>/runtime` | the runtime intrinsic |
| `<id>/queue` | the queue intrinsic |

`state` is **reserved and implied**. It is not a intrinsic; it is the segment
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
   host is simply the segment that is not a intrinsic.
2. **The wire loses its special case too.** `rpc.FormDelta.Intrinsic string`,
   `omitempty`; `""` means `state` means the host's own form — which is
   exactly what every delta on the wire means *today*. So the field is
   backward-compatible **by the same rule that makes the shorthand work**,
   rather than by a coincidence of encoding. (This replaces the plan's
   `Facet` field; `intrinsic` is the word, and the empty value now has a
   meaning rather than being merely a default.)

The turn-disposition intrinsic is therefore **`/runtime`**, not `/state`. Its
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

Approved as written: the two intrinsic forms and their key sets, the inbox as the
one publisher, the widened `QueueState`, retention of `committed` items,
`turnStatusSending`/`turnStatusAccepted` with a distinct glyph family, the
death of the poll, and the deletion of
`proposals/status-queue-proposal.md` §2.3.

---

## 5. Implementation notes, written as it landed

### What the canary proved

`TestAMessageIsNeverNowhere` was reverted against the original code (drop the
text/state from `liftLocked`'s `promptRef`) and failed with exactly the
reported symptom:

```
message 1 was in a state and is now in NONE: it exists nowhere a client
could see it. States so far: [queued]
the journey was [queued committed], wanted [queued committing committed]
```

That is the bug, reproduced from its own description. An assertion that has
never failed is not evidence; this one has.

### The identity segment held its promise

`<host>` is parsed by `splitIntrinsic` into `(host, "")`, and `""` means the
host's own form everywhere: in `openFormView`'s filter, in `FormRequest`, in
`FormDelta`. So the wire field is backward compatible **by the same rule that
makes the shorthand work** rather than by a coincidence of encoding, exactly as
§2 predicted. No schema bump, no tape re-record.

### tmux was not in the dev shell

Found while wiring the smoke case: `mkFigaroShell` carried `go`, `gopls`,
`gotools` and `benchstat`, and no tmux. The smoke suite *skips silently*
without it (`tmuxsmoke_test.go:208`), so the suite was green, unrun, and
indistinguishable from green and run — the same failure mode the `-count=1`
warning in the tmux-testing skill exists to prevent, reached by a different
road. Added to `buildInputs`.

### The bug the unit tests could not see

`fig form show <id>/runtime` answered `{}` on a live aria, while every unit
test passed.

`model` is in the system-managed catalog, and `CheckWritable` refuses an
*unprivileged* write to one. The runtime intrinsic publishes `model`. So the
patch was refused **whole** — not just that key — every publish failed, and
each failure was a `slog.Warn` nobody reads.

The catalog exists to stop a *human* typing `figaro set model=…` into a board
the harness owns. A intrinsic has no user-writable path at all, so the check had
nothing to protect and could only refuse. It writes privileged now, as
`Libretto` already did, and a refusal is an **error**: a intrinsic that cannot
write is not degraded, it is absent, and the status bar it feeds silently
reverts to guessing — the exact behaviour this change removes.

**Why the tests missed it is the more useful half.** The test agent has no
model: `currentModel()` returned `""`, the key was skipped, and the one key
that could fail was never written. *A fixture tidier than production certifies
production untested* — the tmux-testing skill's first line, reached from a
direction it does not list. The regression test now names a model explicitly
**and asserts the fixture carries it before proceeding**, so it cannot pass
vacuously the way its predecessor did.

Found by probing a real daemon in the dev shell, not by reading. Nothing about
the code looked wrong.

### The `Q` trap in the drawer smoke case

The first draft of `TestSmoke_OpenQueueDrawerStaysCurrent` pressed `Q` to open
the drawer and then declined with "the drawer did not open". It *had* opened:
`commandSend` calls `openQueueFromKey` by itself when the daemon reports the
turn active, so the `Q` **toggled it shut**. The test was testing its own
keystroke. Recorded in the case, because the next person will reach for `Q`
too.

---

## 6. The open questions, answered

The parent plan's §5 asked four. Three are settled by the implementation.

**1. What the distinct indicator means.** Settled as: it runs from submit until
*authoritative* state arrives, then the bar follows the form. Implemented as a
rank rather than a sequence — `sending` is the client's own **hypothesis**, and
`turnStatus.authoritative()` says which states outrank it. That framing turned
out to matter: `beginTurn` fires from `openInline`, *after* the submit, so on a
fast aria the daemon's "thinking" lands first and an unconditional assignment
put the bar back on the arrows with a tool running above it. A sequence would
have hidden that; a rank makes it impossible.

**2. Committed-item retention.** Settled as the split §2.4 proposed: retained by
count in the form (`committedRing`), and the client drops the row when its own
transcript has adopted the turn — `in.hasTurn(r.Turn)`, a fact it knows,
against a turn id the daemon publishes. No timer anywhere.

**3. Facet spelling.** Settled: **intrinsic**, `<host>/<name>`, `state`
reserved and implied. Not in `form ls`.

**4. Should `/runtime` absorb the metrics?** Still open, and now the only thing
left polling: `startPagerClock` pulls the capacity figure and the mantra every
fourth tick with a backward read of one message. It is the same disease and the
same cure, and the key set has room. Deliberately not done here — the change is
already wide, and metrics move on a different clock from disposition.

### What the pty found that nothing else could

Both bugs in this change were found by driving a real daemon and a real
terminal, and neither was visible to a green unit suite:

| bug | how it looked | how it was found |
|---|---|---|
| the runtime intrinsic was **empty** | `fig form show <id>/runtime` → `{}` | probing a live daemon in the dev shell |
| the bar showed **sending** with a tool running | `⇉ · <id> · …` above `⠹ $ for i in …` | reading a *declined* smoke case's capture |

The second is worth dwelling on: the case that surfaced it was testing
something else entirely and had **skipped**, not failed. The evidence was in
the capture it printed on its way out.

### One failure that was not mine, and how that was established

`TestSmoke_SteerOrderMatchesShow` failed in the final full run
("steer marker appears 0 times, want exactly 1") having passed in the one
before it. Re-running reproduced it, which ruled out a one-off.

**A/B against a genuinely different binary.** The current test code was run with
`FIGARO_SMOKE_BIN` pointed at a build of `52cfa1bf` — the store-only commit,
before any intrinsic, any client mirror, any status change. Distinct md5, and
`--version` confirmed `figaro 52cfa1bf` in the log rather than being assumed.
It failed with the identical message.

Two different binaries, one failure ⇒ the failure is not in the difference.
This is the skill's trap 11 run in the honest direction: *"two arms that produce
identical output are more often one binary than one bug"* — so the arms were
proven different first, and only then was the shared failure believed.

The case is flaky in this environment (it passed once, failed twice, with
identical code) and is left as found. Its neighbour
`TestSmoke_DetachedTailAdvancesAndScreenHoldsStill` declines for a
already-documented reason (`Ctrl-T did not open the pager`).
