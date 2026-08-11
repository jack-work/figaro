# Form projection: follow-ups

Filed 2026-08-03 alongside the fix for "every provider round-trip re-sent the
whole form". That fix is in; these are the things it deliberately did NOT
do, left here so the next aria to touch this code can pick them up.

Context for the fix itself:
`~/notes/figaro-backup/20260803-120630/REGRESSION-form-resend.md`
(measurements, decoded wire bytes, and the mechanism).

## What shipped

`provider.Form` now asks for an absolute range, `PatchesBetween(after,
upTo]`, instead of the implicit `PatchesUpTo(version)`. The projection carries
`LastFormVersion` across a warm start, so a resumed pass renders exactly the
patches a cold walk would. `patchCursor` still walks forward once and its index
only advances: no per-entry scan, no binary search.

The bug was that a *fresh* cursor (index 0) was handed to a projection that
warm-starts at `previous.Entries`. The entry index was preserved; the cursor's
was not. An absolute range cannot express that mistake, which is the point.

## 1. Stop materialising the whole patch list on every Send: the real win

**CLOSED 2026-08-11.** `Form.Patches()` is gone; `Form.PatchesBetween(after,
upTo]` answers by binary search into the published array and returns a capped
sub-slice, with no copy and no retained cursor. Measurements, the real-store
probe and the traps are in `plans/form-view-perf.md`. The paragraphs below are
the filing, kept for the reasoning.

`Agent.formAccessor()` calls `backend.FormPatches(a.id)` once per
provider Send, and that returns **a copy of every patch the aria has ever
had**:

```go
return append([]VersionedPatch(nil), c.patches...), nil   // xwal_backend.go
```

A tool loop is many Sends, so a long-lived aria copies its entire patch history
several times per turn to render, typically, one patch or none. It is O(total
board) work on a path invariant #14 says must be O(delta).

Better shape: hand the projection a view that can answer `(after, upTo]`
without copying: the patches are already ordered by version and held in
`chalkCache`, so a bounded sub-slice under the existing lock would do. Keep the
absolute range in the interface; only the plumbing changes.

## 2. Missing tests: deliberately skipped for turnaround

Verified by live reproduction instead (fresh aria, several tool round-trips,
decode `translations-v2/copilot-messages/<node>/*.jsonl` and count
`system-reminder name=` per record). These are the tests that should exist:

- **Cold equals warm.** Project a log in one pass; project the same log as
  two warm-resumed passes; assert the rendered patch sets per LT are
  identical. This is the invariant that broke and it is cheap to state.
- **A patch is rendered exactly once.** Across a whole projection, the union
  of `msg.Patches` must equal the aria's patch list with no duplicates -
  catches both this regression (all patches, repeatedly) and 072fd24's
  (no patches at all).
- **`LastFormVersion` survives the hand-built projections.** All three
  providers rebuild `IncrementalProjection` by hand after a live append
  (`copilot/responses.go`, `anthropic/anthropic.go`,
  `anthropicsdk/anthropicsdk.go`). Dropping the field there silently
  reintroduces the bug on the live path only: the nastiest possible
  variant, because a cold reopen looks correct.

## 3. Arias already poisoned

The duplication was written into the per-LT translation cache, so affected
arias keep paying it until their cache is invalidated. A `Fingerprint()` bump
clears it (`classDerived` in `store/schema.go`: the cache is regenerated
lazily). Not done automatically here: it re-encodes every aria on next use, and
that is a decision for whoever cuts the release, not for a hotfix.

Worst observed on this box: 48-message arias sitting at 983k–988k context, of
which ~325k+ was duplicated board.
