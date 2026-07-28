# Patch proposal — a twelfth trap for `tmux-testing`

**For the master to apply, or not.** This is a *proposal*, not an edit.
`~/.config/figaro/skills/tmux-testing.md` is the master's own configuration, and
editing a human's skills behind his back is not ours to do. ROSINA's ruling.

Found by **BARTOLO** while writing the regression tests for the resize/gap bug.
It is the best single piece of evidence produced during the paint hunt, because
it explains why an existing, well-written, still-passing test could never have
caught the bug it was built to catch.

---

## Where it goes

In `tmux-testing.md`, section **"Eleven traps, each of which produced a confident
wrong answer"** — append as **trap 12**, and change the heading to *"Twelve
traps"*. It also deserves a one-line pointer from the **"Before you believe a
test"** list, since that list already asks *"Does the double call the real
function?"* and this is the same question asked of the *oracle* rather than the
double.

---

## The prose to add

> **12. A reference oracle that shares the bug under test agrees with it
> perfectly.** `TestTranscriptPaint_MatchesNaiveRepaint` diffs the real painter
> against `naivePaint`, a deliberately simple reference. It has caught real
> defects. It could not, structurally, ever have caught the resize/gap bug —
> because **`naivePaint` is also a diff painter, and it also skips a row whose
> new content is empty when the previous frame is short or absent.** Both sides
> made the identical mistake, so both sides produced the identical frame, and the
> test passed with the bug fully present in both. The remedy is that a reference
> must be *dumber* than the thing it checks, not merely *different*: BARTOLO's
> replacement is an **unconditional repaint** — every row, every frame, no diff,
> no state — which cannot share a diffing bug because it does not diff.
>
> This is the sibling of the rule this skill is built on. *"Every test double
> that diverged from production diverged by being tidier than reality"* warns
> about the double. Trap 12 warns about the **oracle**: a double that is *too
> similar* to production is as blind as one that is too tidy. When you write a
> reference implementation, ask what class of bug it is **incapable** of having.
> If the answer is "the same class as the code under test", it is not an oracle,
> it is a mirror.

## One-line addition to "Before you believe a test"

> - Could the oracle have the same bug? A reference that shares an algorithm with
>   the code under test agrees with it *because* of the bug, not despite it. See
>   trap 12.

---

## Evidence, so the claim is checkable rather than asserted

| | |
|---|---|
| the shared defect | both the painter and `naivePaint` read a missing base row as `""` and then skip a row that compares equal, so a legitimately-blank row is never painted |
| the real painter | `transcript.paint`, `internal/cli/transcript.go` — `var old string; if r < len(base) { old = base[r] }; if screen[r] == old { continue }` |
| the oracle | `naivePaint`, `internal/cli/transcript_paint_test.go` |
| the test that could not fail | `TestTranscriptPaint_MatchesNaiveRepaint` — still green, with the bug present on both sides |
| the remedy | BARTOLO's reference: unconditional repaint (see `internal/cli/transcript_resize_paint_test.go` on `fix/gap-rows`) |
| what the bug actually was | the user saw stale text in the gaps between nodes after a resize, and duplicated lines — one root cause, two reports |

**Canary, both directions** (BARTOLO, quoted from `proposals/BARTOLO-gap-rows.md`):
pristine `paint/base` → VT harness 5 of 40 rows stale, real tmux 3 of 40 rows
stale; with BASILIO's fix applied → both pass. Identical failure with scroll
regions on and off.

---

## A second, smaller candidate — offered, not pressed

While hunting the same bug ALMAVIVA built a **jog-and-diff oracle**: capture the
suspect frame, move the viewport away and back to the same offset, capture again,
diff. It found the bug, and it is genuinely useful. **BARTOLO then showed it has a
blind spot, and the blind spot is the same shape as trap 12:** it compares the
painter *against itself*, so any damage the repair gesture does not repair leaves
suspect and truth **equally wrong**, the diff empty, and the verdict **CLEAN**.
That is exactly where a second root cause would hide — and exactly what the
user's word *"typically* fixed upon return" left room for.

It is not retired, because a documented blind spot beats a silent replacement.
It is written up with the limitation attached in `PAINT-REPRO.md` §5. Whether
that belongs in the skill as prose, or only as this cross-reference, is a
judgement call about how general it is — it may be too specific to figaro's
pager to earn a numbered trap. **We are not asserting it does.**
