# PROPOSAL: BARTOLO (`fix/gap-rows`)

Gap contamination: the regression tests, and an attempt to falsify the
convergence claim that failed.

**This branch carries TESTS ONLY. It is RED on its own, by design.** The fix is
BASILIO's, on `fix/resize-dup`. A canary that passes is not evidence, so the red
state *is* the deliverable; see §5 for the verified green.

---

## 1. The bug

The user:

> "The gaps in between nodes are populated with text that shouldn't be there from
> some other line. It can only be cleared by moving the viewport such that the
> corrupt region is no longer visible, and then moving back to that area. It will
> typically be fixed upon return."

---

## 2. Root cause

**`transcript.paint`, `internal/cli/transcript.go`.** `transcript.resize` sets
`t.prev = nil` to mean *"the terminal reflowed under me; I know nothing about the
screen"*, and its comment claimed that produced a full repaint. It did not.
`paint` read the base row as

```go
var old string
if r < len(base) { old = base[r] }
if screen[r] == old { continue }
```

which makes **"no record of this row" indistinguishable from "a record of an
empty row"**. So every row whose new content is the empty string compared *equal*
to a base that did not exist and was skipped entirely. Blank rows are everywhere:
`entryLine` (`internal/cli/transcript_index.go`) returns `""` for row 0 of every
message separator. The terminal, meanwhile, still held its own post-resize
leftovers on those rows: so each separator's blank row kept text from another
line and stayed wrong until the viewport moved far enough to make that row differ
from `t.prev`. "Typically fixed upon return."

I did not derive this; ALMAVIVA did, and BASILIO fixed it. My contribution is
that it is now *pinned*.

**Duplication and contamination are the same pixels.** BASILIO measured the link:
a shrink makes tmux delete *k* rows from the top, so terminal row *r* holds the
old frame's row *r+k*; because the new frame covers mostly the same viewport,
that old line is often *also* rendered legitimately at its own correct row. Of 3
surviving ghosts, 1 was simultaneously on screen for a legitimate reason: the
user sees it twice. "Lines are duplicated" and "gaps hold text from another line"
are one fault described from two ends.

---

## 3. What I built

### Test 1: `TestTranscriptPaint_ResizeLeavesNoStaleRows` (VT, deterministic, 0.4 s)

`internal/cli/transcript_resize_paint_test.go`. Asserts the invariant the repo
already believes in and that **every** candidate fix must satisfy:

> **After every paint, the terminal's grid equals `t.prev`.**

`t.prev` *is* the painter's claim about the screen; the whole diffing strategy
rests on it. The assertion names no implementation: not `base == nil`, not a
sentinel, not a full-repaint flag, not `\x1b[2J`, and calls the real entry point
`tr.resize(w, h)`. That mattered: BASILIO's landed fix is a **third** shape,
neither of the two I first proposed, and the test needed no change at all.

Two design points worth defending:

- **It models no terminal behaviour.** BASILIO's formulation, adopted verbatim:
  scribble every cell with a marker the painter has no record of, *then* resize,
  then require convergence. `resize()` discards `t.prev`, and the contract it
  takes on is *converge from an arbitrary prior state*. A scribble is harsher than
  anything a real terminal does (tmux truncates on a width change, slides rows on
  a height shrink: both leave some cells right by luck), so a pass here implies a
  pass on any real resize. My first version modelled truncation; this is strictly
  stronger and carries no liability. It also caught more: **5** divergent rows at
  100→72 where the model found 3.
- **Asserted after every frame**, not just the last: painter invariant #5. A
  transient glitch self-heals on the next op, and the user's own complaint is that
  his next scroll repairs the damage. A settled-screen assertion cannot see that.

**Why it is a new file and not another `step()` in `TestTranscriptPaint_MatchesNaiveRepaint`:**
`naivePaint`, the reference oracle in that test, is a diff painter too and carries
the **identical hole**: with a nil/short prev it also skips every empty row.
Comparing the optimized painter against it compares two implementations of the
same mistake, and they agree perfectly. My reference (`vtFromRows`) is an
*unconditional* repaint of every row, which cannot skip anything. **A reference
oracle that shares the bug under test is how eight green tests once certified
broken code.**

### Test 2: `TestTranscriptPaint_RealTerminalResize` (real pty, ~2 s)

`internal/cli/transcript_resize_tmux_test.go`. `transcript_paint_tmux_test.go`
already replays the pager's escape stream into tmux and compares against
`tr.prev`; its premise is exactly right and it is how the suffix-update erase-order
bug was found. **The one thing it never did was span a resize**: the only event
where the terminal changes its own grid with no application involvement.

The stream is split in two with the resize **between** the halves, in the real
order: the terminal's grid changes first (it has already happened by the time
SIGWINCH is delivered), then the application repaints. Synchronised by marker
file, not by sleeping: this is the one instant whose ordering *is* the test.

This is the case that would have caught the shipped bug. It also **validates
Test 1's original width model** against reality: both flagged the *same three
rows* (15, 30, 33), same text, same count, both scroll-region settings.

### Test 3: `TestTranscriptPaint_GesturesKeepBelief` (§4)

### `scripts/paint-gapcheck.sh`

A structural, comparison-free detector for the pty side: a separator is a blank
row then a full-width rule (`dimTransRule`), so every pure-`─` row must be
preceded by a blank one. **It exits 2 for VACUOUS rather than reporting a pass** -
its first run against a real pane examined *zero* rules and cheerfully said "0
contaminated" about a frame I had already proved contaminated, because the
viewport sat entirely inside one long tool output. A verdict on nothing is not a
pass.

---

## 4. I tried to falsify the convergence claim and could not

This was my other charge, and the honest answer is a **negative result**.

The jog-and-diff oracle cannot settle it: it compares the painter **against
itself**, so damage the repair gesture does not repair leaves suspect and "truth"
equally wrong, the diff empty, and the verdict CLEAN. That blind spot is precisely
where a second root cause would hide, and the user said "**typically** fixed upon
return". So I moved the hunt into the VT harness, where `t.prev` is absolute
ground truth and every frame is checked directly.

`TestTranscriptPaint_GesturesKeepBelief`: **156 frames per parametrisation, 312
total, no resize anywhere**: covers everything the pty sweep could not reach:

| hunting ground | result |
|---|---|
| `gg` / `G` | clean |
| batched wheel bursts (23 up, 11 down in one frame) | clean |
| nav/arrow cluster incl. Home/End/PgUp/PgDn | clean |
| `/` search, then `n`/`N` ten times | clean |
| `!` status, `?` help, queued panels open+close | clean |
| selection ×6, expand, collapse | clean |
| verbosity toggle | clean |
| 80-step climb forcing page landings mid-view | clean |
| **live streaming turn**, 20 deltas, following *and* detached, then sealed | clean |

**Conclusion: the convergence claim survives.** BASILIO's single fix covers both
user reports, and there is no second root cause among the gestures ALMAVIVA's pty
sweep could not reach. The test is kept so the claim stays under guard.

Panels are the sharpest of these: they change the body's row budget exactly as a
resize does, *without* the terminal reflowing underneath. They stay clean, which
localises the fault precisely to the terminal-reflow case rather than to any
recomputation of the layout.

---

## 5. Canary: the evidence, both directions

Against **pristine `paint/base`** (this branch, tests only):

```
viewport holds 5 intentionally-blank rows at 100x40
after width shrink 100->72: row 15 [STALE TEXT]
  t.prev claims: ""
  screen shows:  "¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤¤"
after width shrink 100->72: 5 of 40 rows disagree with t.prev at 72x40
```

and the real pty, where tmux did the resizing:

```
5 intentionally-blank rows before the resize; comparing 40 rows at 72x40
row 15 [STALE TEXT]
  t.prev claims: ""
  tmux shows:    "   │      8  internal/cli/transcript.go:56: some captured tool output li"
row 30 [STALE TEXT]  t.prev claims: ""  tmux shows: "   past the right margin. The quick brown fox …"
row 33 [STALE TEXT]  t.prev claims: ""  tmux shows: "   margin. The quick brown fox jumps over the …"
3 of 40 rows disagree with t.prev after a real 100x40 -> 72x40 resize
```

Both fail **identically with scroll regions ON and OFF**, which independently
locates the fault in the plain diff rather than in `planScroll`, and corroborates
BASILIO's finding that stale rows *relocate* under SU/SD but never multiply.

Applying BASILIO's landed fix (`git diff paint/base fix/resize-dup --
internal/cli/transcript.go`, his commit `90c4dac`) to this branch locally:

```
--- PASS: TestTranscriptPaint_ResizeLeavesNoStaleRows (0.40s)
--- PASS: TestTranscriptPaint_RealTerminalResize (2.12s)
ok  github.com/jack-work/figaro/internal/cli  2.534s
```

I then reverted it, so this branch stays tests-only. Cross-reference: BASILIO's
own real-pty A/B, md5s printed so the arms provably differed: before
`0e04173e36e9` 3 contaminated, after `c1330222cc02` 0 contaminated.

---

## 6. Build and test state

`go build ./...` and `go vet ./...` are **green**. `go test ./...` is green
**except** my three new resize assertions, which fail by design because the fix is
not on this branch. Verified: nothing else in the suite fails, and the probe/fix
does not break any pre-existing test (`RealTerminalReplay`, `MatchesNaiveRepaint`,
`UsesScrollRegion` all still pass with it applied).

**For the merge:** `fix/gap-rows` + `fix/resize-dup` together are green. That is
the combination to present; neither alone is.

---

## 7. Risk

- **Test 2 needs tmux** and skips cleanly without it, as its neighbour does. It
  adds ~2 s to `go test ./internal/cli/` and is skipped under `-short`.
- **Test 2 is the least hermetic thing I added.** It skips rather than fails on
  every environmental surprise (no tmux, wrong pane geometry, resize not applied,
  pane never produced output). That is deliberate, a flaky *failure* teaches
  people to ignore the suite: but it means a broken environment reads as a skip.
  Watch for it silently skipping forever; the fixture guard (§8) is the defence.
- **`scribbleUnknown` is harsher than reality.** If a future fix legitimately
  relies on the terminal preserving cells across a resize, this test would fail on
  correct code. I think that reliance would itself be a bug (it is unspecified
  across emulators), but it is a judgement, and it is the one place this test
  could cry wolf.
- `resizeGrid` and `scribbleUnknown` are test-only methods on `vtScreen`, shared
  with the existing paint tests. Additive; nothing existing changed.

---

## 8. What I did NOT do

- **I wrote no fix**, deliberately, per the split.
- **I did not audit** `predBuf` / `keysOld` / `keysNew` / `screenSpare` /
  `rowCache` / `cacheW` / `prefix` invalidation on resize: BASILIO's lane.
- **I did not test the inline/incipit painter**, only the pager. The same
  `var old string` pattern may exist elsewhere; I did not look.
- **I did not reproduce on a real aria in a real pane myself.** ALMAVIVA and
  BASILIO both did (aria `8566c903`); my pty coverage is the synthetic in-repo
  fixture. After the credential incident I removed my copy of the master's
  history rather than keep it for a capture I did not need: my tests are entirely
  synthetic and require no seeded store, no credentials, and no provider.
- **`paint-gapcheck.sh` has not yet caught anything.** It was vacuous on the one
  viewport I tried it against. It is a sound detector with no scalp; treat it as
  unproven.
- **I did not test a resize *during* a live turn**: streaming and resizing are
  each covered, but not simultaneously. That is the most plausible remaining gap
  and I would look there first if a report survives this fix.

---

## 9. Decisions for the user

**None from me.** I found no product decision in this work, and I want to be
explicit that this is a positive finding rather than an omission:

- The fix has **no user-visible trade-off**. BASILIO measured it: +44 bytes per
  frame, +1.2%, **once per resize**, because every non-blank row was already being
  retransmitted and only the blank ones are added, at CUP+EL each. The alternative
  (`\x1b[2J`) would also have worked and would *flicker*: the existing comment
  suppressed it for that reason. So there was never a choice between two
  defensible renderings, only a measurement somebody had to take.
- My tests assert an invariant the codebase already held, so they relax nothing.

The one thing worth a human's attention is a **documentation** matter, not a
decision: `planScroll`'s "a mis-detected shift costs bytes, never correctness" is
true but has an **unstated precondition**: it holds only while `t.prev` actually
equals the terminal. That precondition was silently false after every resize, and
the comment gave a reader confidence the code had not earned. BASILIO found it;
neither of us changed the comment. Somebody should.

---

*Sospettoso fino alla fine: e per una volta, con nulla da sospettare.*
