# The two-participant write, for Gluck's review

You approved this **conditionally**: *"I think I'm comfortable with two-participant
write, but not two phase commit. I need to see the code to decide"*
(`answers-forms.md`, non-blocking §4). It has been built since `ac3314bc` and
nobody ever put it in front of you. Here it is. Five minutes.

## The rule

Two nodes must end consistent: the **libretto** (a refcount) and the
**observer's board** (`system.studies`, a set). Two actors, two logs, no
shared transaction. Not two-phase commit — crash-safe by **ordering**, the
same idiom the delete path already uses:

```
study: libretto FIRST (retain), board SECOND (declare)
drop:  board FIRST (undeclare), libretto SECOND (release)
```

Both orders leave, on a crash, a refcount that is **too high**. Too high
delays reclamation; too low reclaims a copy a live observer still needs. One
is a leak, the other is data loss, and `ReconcileLibrettos` **recomputes**
each count from the boards, so it repairs the leak — and, being a recompute
rather than an adjustment, it repairs an under-count too.

## What it looks like (`internal/store/study.go`)

```go
// study
lib := b.libretto(sourceFormID)          // mint if absent, seed, follow
for attempt := range studyAttempts {
    studies, version := b.studiesAndVersion(observerID)
    if slices.Contains(studies, sourceFormID) {
        return studies, false, nil        // idempotent: not a second reference
    }
    if !retained { lib.Retain(); retained = true }   // ONCE, not per attempt
    if err := b.setStudies(observerID, next, version); err == nil {
        retained = false                  // the declaration owns it now
        return next, true, nil
    } else if !errors.Is(err, ErrFormMoved) {
        return nil, false, err
    }
    backoff(attempt)
}
// deferred: if `retained` is still true, hand it back
```

Three things in there are load-bearing and each was a bug first:

1. **Retained ONCE, not per attempt.** Eight concurrent casts were paying a
   retain and a release each, per attempt.
2. **The deferred hand-back.** A study that fails must not leave a count only
   a sweep can explain.
3. **The version guard** (`setStudies` is conditional on the board version).
   Without it, two arias studying different forms at once overwrite each
   other's declaration, because `system.studies` is a read-modify-write.

## The three decisions that are actually yours

**1. The retry budget went 5 → 32** (`studyAttempts`). Not a tuning choice: it
follows from a semantic change you got for free. Taking `cast` off the actor
loop (the self-cast deadlock) replaced *serialization* with *optimism*, and
optimism has to be sized for the contention it now meets. Eight concurrent
casts of one figaro exhausted five attempts and failed with "the board would
not hold still", **losing studies for roles that had already been pointed at
the caster**. Each attempt costs one fsync on conflict, so a generous budget
is cheap and only spent under contention that used to be impossible.

*The question*: are you happy that a verb which used to be serialized is now
optimistic-with-retry, or do you want the study set serialized somewhere?

**2. It is best-effort through an optional interface.** A backend without
librettos (ephemeral) keeps the plain board write; a libretto that cannot be
reached does not block a declaration, because the board is the authoritative
fact and the sweep recomputes from it.

*The question*: is "the declaration wins, the count is repaired later" the
trade you want, or should a study fail when its copy cannot be minted?

**3. `system.studies` is `KeySystemManaged`** and written through
`ApplyFormEffectPrivilegedIf`. A hand-written `fig set system.studies` is
refused. Import lifts the key out of the imported patch and replays each id
through the VERB rather than gaining privilege, so an importer still cannot
write `system.cwd`.

*The question*: none, unless you disagree — but it changed what `fig set` can
do to a board, so it is worth knowing.

## One defect found while writing this up (fixed, `internal/store/study.go`)

`DropForm`'s retry loop `break`s on success and `continue`s on conflict — and
**fell through on exhaustion**, releasing the libretto anyway. That takes a
reference off a copy a board still names: the under-count that the whole
ordering exists to prevent, and the one the sweep cannot distinguish from a
legitimate observer. It also returned `changed: true`, lying about it.

It refuses now, with the reference intact, and says so. Verified red: without
the guard, the test reports a successful drop and the count comes down.

Rare — 32 consecutive conflicts — but the ordering discipline is *entirely*
about what happens in the rare case, so a fall-through in the one branch
where the participants can disagree is exactly the wrong place to have one.
