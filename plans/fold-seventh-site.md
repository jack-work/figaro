# The seventh fold site

Filed 2026-08-18 by 6defe6f9, on 091d162e's ruling, as the follow-up to
the stage 1 record correction.

## What happened

Stage 1 (`form.Fold` / `form.FoldRender`) consolidated the sites that
folded form patches forward. Its commit message says seven. It converted
**six**: projection (3), anthropic, anthropicsdk, openaichat. Copilot's
`inputFor` loop in `internal/provider/copilot/responses.go` was never
touched.

The commit message was written from a list that was not checked against
the diff. The record is corrected in the follow-up commit and in
`fold.go`'s doc comment; this file is the work the correction defers.

## Why copilot is actually different

The other three log a render failure and continue:

    rendered, err := form.Render(patch, snap, tmpls)
    if err != nil { slog.Warn(...) } else { ...emit... }
    snap = snap.Apply(patch)

Copilot returns:

    rendered, err := renderResponsePatch(patch, snap, templates)
    if err != nil { return nil, err }

`FoldRender` takes an `onErr` callback and continues, which matches the
three and not the fourth. So conversion is a design change to the
abstraction, not a copy-paste.

## The real question, which is not "convert it"

Two answers, and they point in opposite directions:

1. **Teach `FoldRender` to abort.** An `onErr` returning bool, or a
   variant returning `(Snapshot, error)`. Cheap, and preserves every
   caller's current behaviour.
2. **Converge on one policy.** Four encoders disagree about whether a
   template failure is fatal. That disagreement is invisible today and
   was invisible to the author of stage 1, which is the argument that it
   should not exist. If log-and-continue is right, copilot is the
   outlier; if abort is right, three encoders silently ship a message
   missing a reminder the board says is there.

(2) is the better question and it is now more urgent than when it was
filed: **copilot is the LIVE provider.** Every aria in this campaign is
speaking through the one encoder that aborts.

## Not to be retrofitted into stage 1

Stage 1 is scored on a sabotage-proven fixture (allocations identical,
ns within 2.2%, sabotage moved B/op +11.5 MB). Reopening it to absorb a
design change would destroy the one clean measurement the consolidation
has.

## Also open, and adjacent

`copilot` has **no patch-ordering guard**. anthropic (added after a
canary passed), anthropicsdk (pre-existing) and openaichat (added in
2fc29fb0) all assert that a patch renders against the board BEFORE it.
Copilot asserts nothing, and it is both the unconverted site and the
live provider. Whoever takes this writes that guard first, before
touching the fold.
