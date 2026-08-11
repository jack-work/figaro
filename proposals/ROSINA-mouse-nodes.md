# PROPOSAL: mouse support on transcript nodes (`feat/mouse-nodes`)

**ROSINA**, aria `83ffbbb5`. Worktree `mouse-nodes`, branched from `main` at
`5069adf`. Nothing on `main` was touched.

## What was asked

> "I want a single click to trigger selection of the content block, and a click
> while it's already highlighted to toggle expansion of the text."

## What is here

Two rules, and no third:

```
click a node                    -> select it
click the node already focused   -> toggle its expansion
shift+click                      -> extend the selection (terminal permitting)
wheel                            -> scroll (unchanged, predates this)
```

There is **no double-click timer**. "Click again" is keyed on *selection state*,
not on elapsed time, so there is no clock, no inter-click window to tune, and no
dead zone for a user who pauses mid-gesture. It also composes: the same second
click collapses what it expanded.

## Root of the design: mouse reporting was already on

SGR mouse reporting (`\x1b[?1000h\x1b[?1006h`) is enabled by the pager's own
`enter()` and parsed by `internal/livelog/render/mouse`: the **wheel already
worked**. So this is not "add mouse support"; it is "the pager receives clicks
today and throws them away." Three things were missing:

1. `mouse.classify` mapped button bases 0/1/2 to `Other`, so a caller could not
   say "left button" without remembering a magic number. Now `Left`/`Middle`/
   `Right`, plus `Shift()`/`Alt()`/`Ctrl()` accessors over the xterm modifier
   bits (4/8/16: deliberately asserted in a test, because this binary *also*
   decodes the CSI-u modifier mask, which is 1/2/4).
2. Nothing mapped a **screen row to a node**. Every other gesture addresses a
   node symbolically (`^N` walks the ref list, `:12.3` names a coordinate); a
   click names a row.
3. Expansion was hard-wired to "is a tool with output" in
   `toggleSelectedTools`.

## The one idea worth reviewing: `frameRefs`

`renderFrame` now records `t.frameRefs`: the node behind each **body row of the
frame it just painted**, alongside the row text it composed, from the same
walk:

```go
t.rowBuf    = t.window(t.offset, t.offset+body, t.rowBuf)
t.frameRefs = t.rowRefs(t.offset, t.offset+body, t.frameRefs)
```

A click resolves **only** through that map. It never re-derives geometry,
because between the paint and the click a live token can arrive, the tail can be
re-tuned, and a panel can change the body height, and *the user pointed at what
they saw*. This is the same staleness `selectNode`'s cold path documents for its
viewport seed, arriving from the other direction.

To keep the row text and the row refs from ever disagreeing about which entry an
absolute line fell in, both go through **one walker**, `forEachWindowRow`;
`window()` was rewritten in terms of it (same behaviour, same buffers). The
per-row cost is one closure call over ≤ body rows.

`lineEntry.refAt(rel)` is the mirror of `entryLine(rel)` and shares its
arithmetic: separator rows resolve to the zero ref, gap sentinels too.

## The expansion seam (shared with SUSANNA, `feat/table-wrap`)

`nodeExpandable(n livedoc.Node, width int) bool` in `nodes.go` is now the single
predicate for "has a collapsed form". `toggleSelectedTools` became
`toggleSelectedNodes` and asks it; the click path asks it for one node. On this
branch the predicate answers exactly what the old inline test answered
(`Type == tool && Output != ""`), so **tool behaviour is unchanged by
construction**.

SUSANNA's branch widens the same predicate to prose whose collapsed render drops
rows (a clipped markdown table). **The two branches are complements and should
land together**: her branch introduces a clamped table, mine is the only gesture
that opens one. Her D3 offers a constant that disables her clamp if mine is not
taken: I recommend against needing it.

`selectRef` already existed in `transcript_jump.go` (the coordinate jump lands on
a node). Rather than add a second "put the selection here", it grew an `extend`
parameter and both gestures use it: which is *why* a clicked selection yanks
correctly: it carries the endpoint hash the copy path verifies.

## Deliberate no-ops (taste, and reversible)

- **A click on chrome does nothing.** Headers, separator rules, the blanks
  between nodes and gap sentinels are half the screen; losing a selection to a
  click that landed one row off is worse than a click that does nothing. It also
  means a stray click does not cancel a search prompt or dismiss a panel: the
  input loop asks `transcriptClickable` *before* it acts.
- **A click never scrolls.** The clicked row is on screen by construction, so
  `ensureSelectionVisible` is not called; calling it would drag the page to the
  far end of a tall node, the exact jump `selectNode`'s cold path was fixed to
  stop making.
- **A second click on a non-expandable node reports no-op** rather than flipping
  a flag that changes no row.
- **Press only.** The terminal reports the release too; acting on both would
  toggle twice per click and so appear never to fire.
- **A click that acts dismisses an open panel**, like any non-panel key.

## Discoverability

The `?` panel gained **one** row: `mouse   click select · click again expand ·
wheel scroll`. It is added by `mouseHelpRows`, *not* by the keymap table, because
that table is keyed by keystroke and a pointer is not a chord: smuggling it in
as a fake chord would buy one line at the cost of the table's invariants (one
help row per binding, openers derived from the rows).

That one row costs one body row while the panel is open, which moved `off=` by
exactly 1 in the 19 `h=true` states of the input-loop oracle. Rebased
individually from the oracle's own report and **documented in its header as the
fifth rebase**, in the style of the four before it. (A blanket bump over every
`h=true` literal also moved rows the panel height does not reach, and broke the
search case: noted there too.)

## Tests, and the canaries that prove they can fail

`internal/cli/transcript_mouse_test.go` (13 cases) and 4 new cases in the mouse
parser. Every assertion goes through `frameRefs` or through the **bytes**, never
through a re-derivation of the offset: two arms that compute the row the same
way are one arm.

Canaried, each reverted afterwards:

| break | what failed |
|---|---|
| `refAt` without the separator adjustment | `TestRowRefsSkipSeparatorsAndGaps` |
| `frameRefs` computed once at pager entry (stale) | `TestClickResolvesAgainstThePaintedFrame` |
| row off by one (1-based `Y` not converted) | 7 cases |
| drop the `Pressed` guard (act on release too) | `TestClickReportsActOncePerClick` |
| remove the `Left` case from the input loop | `TestClickReportsActOncePerClick` |

The first canary run also **found a defect in my own tests**: two of them
`t.Skip`ped on a geometry accident, and the stale-`frameRefs` canary came back
*green* because of it. Both skips are now `Fatal`s that name the fixture as the
suspect: "does the fixture still exercise its own path?" is a question the
skill asks for a reason.

The last two cases are the only ones that can see the press/release rule at all:
they drive `interactiveInput.consume` with the byte pairs a terminal actually
sends.

`go build ./... && go vet ./... && go test ./...` green (35 packages).
Real-pty verification: see `/tmp/rosina-click/` and §"pty" below.

## What is NOT here

- **No drag-select.** That needs button-event tracking (`?1002h`) and a motion
  handler; `shift+click` covers the same ground with no new mode. Worth doing
  later if the user wants it.
- **No clickable chrome.** `? help` and `! status` in the footer are obvious
  targets and are deliberately untouched: they would be a second gesture
  vocabulary and the user asked for one.
- **No middle/right button behaviour.** Classified, unbound. Right-click is the
  terminal's own context menu in most emulators; taking it is user-hostile.
- **No incipit clicks.** Mouse reporting is only on while the pager is up, which
  is also the only place a node has a stable screen row.

## Decisions for the user

1. **Shift+click may never arrive.** Many terminals reserve Shift+click for
   their own text selection while mouse reporting is on. `^N/^P + Shift` already
   extends a selection, so the loss is an affordance, not a capability: but if
   you want shift-click guaranteed, the alternative is a different modifier
   (Ctrl+click) or a sticky "extend" mode. Recommendation: leave it; it works
   where it works.
2. **A click on empty space does nothing** (see above). If you would rather it
   *clear* the selection, that is a two-line change in `clickAt`: say which you
   prefer.
3. **Mouse reporting captures the terminal's own selection.** This is already
   true today because of the wheel, so nothing regresses: but it is the reason
   a user cannot drag-select text in the pager, and `y` (yank) exists to
   compensate. Flagging it because it is the most likely "bug report" this
   feature attracts.
4. **Land with `feat/table-wrap`.** See the seam section.

## pty: the gesture, in a real terminal

Verified against a real pty via ALMAVIVA's `scripts/paintpane.sh` (private tmux
socket, stamped binary invoked by absolute path, `figaro listen` so it costs
**zero tokens**, pager chrome gated, daemon and server both reaped, scratch tree
removed). Binary `d418a9c1077f` = `figaro bbebd8d35d91`, pane 100x40.

The first attempt **verified the wrong thing** and is worth recording: it aimed at
the first row carrying a `│` gutter, which is *also* the thinking-block
blockquote gutter. It clicked a thinking node, which is correctly *not*
expandable: so the run "passed" while proving only the inert rule. Re-aimed at a
row carrying the `… last N of M lines` marker, which is proof the node has
something to reveal:

```
clicking screen row Y=6:   │ … last 10 of 13 lines

                  gutter │ rows   cue ▎ rows   cap marker   ? help
  base                16            0            1            1
  click 1             16           12            1            1     select
  click 2             18           14            0            1     expand
  click 3             16           12            1            1     collapse

click 1 -> click 2:  "│ … last 10 of 13 lines"  becomes
                     "│ origin https://github.com/…(fetch)" + (push) + "---"
                     footer range 861–898/898+  ->  863–900/900+
click 3 == click 1:  IDENTICAL: the collapse restores the frame exactly
click on chrome:     ▎ rows still 12: the selection survives a stray click
```

The cue lands on **12 rows**: the whole tool node including its `✓ bash …`
header, not just the row under the pointer: which is the correct reading of
"selection of the content block".
