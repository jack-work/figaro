# PROPOSAL — table wrap and prose expansion

Branch **`feat/table-wrap`** off `main` at `5069adf`. Three commits:

| | |
|---|---|
| `7ae3ab3` | `test(render)` — the failing regression, no fix |
| `21f92fe` | `fix(render)` — glamour v0.8.0 → v1.0.0, width recalibration, `vendorHash` |
| `461d8a7` | `feat(cli)` — the `nodeExpandable` / `renderNode` seam and prose's collapsed form |
| `46d2e23` | `fix(cli)` — collapse only in the transcript (the `^O` measurement, §6) |

`go build ./... && go vet ./... && go test ./...` green (35 packages, 0 FAIL).
`nix build .#figaro` green.

---

## 1. The bug

> "aria prose in transcript and incipit mode when rendered in a table is always
> clipped. The text should wrap ideally so the whole text can be read."

Real, and worse than reported: the text was not clipped, it was **destroyed** —
and destroyed *before* either view saw it.

## 2. Repro

### Unit level, at the `render.Prose` boundary

`internal/render/table_wrap_test.go`, which is a commit of its own so it can be
seen failing. On `main`, **171 cell words lost** across three tables at widths
30–80. The narrowest case that loses anything is not narrow at all:

```
render.Prose("| state | meaning |…", 80)     // glamour v0.8.0
  |  state  │meaning                                                               |
  |  ───────┼────────────────────────────────────────────────────────────────────  |
  |  dormant│not loaded in memory; nothing is running and the aria costs nothing   |
  |         │                                                                      |   <- "to keep" is GONE
  |  active │the inbox is non-empty, so a turn is in flight right now              |
```

Note the row widths: **80 columns at width 80**. Nothing overflowed, so
`clipToWidth` never fired. The bug report's finger pointed at the wrong place.

### Real terminal, real binary, both views

`/tmp/susanna/repro.sh` — private tmux server, isolated
state/runtime/config, pane read back at 80×60, scrollback captured, daemon and
server both killed on exit. Arms proven distinct by md5 (trap #11):
`e8fcae54…` before, `f1af5391…` after. Pager chrome asserted **0** in the
incipit capture and **1** in the transcript capture (trap #3) — so the absence
of "to keep" is a real absence and not content sitting above a pager window.

**BEFORE** — identical loss in incipit, `show` and the transcript pager:

```
  state  │meaning
  ───────┼────────────────────────────────────────────────────────────────────
  dormant│not loaded in memory; nothing is running and the aria costs nothing
         │
  active │the inbox is non-empty, so a turn is in flight right now
```

**AFTER**:

```
   state   │ meaning
  ─────────┼──────────────────────────────────────────────────────────────────
   dormant │ not loaded in memory; nothing is running and the aria costs
           │ nothing to keep
   active  │ the inbox is non-empty, so a turn is in flight right now
```

Captures: `/tmp/susanna/repro/{before,after}/out/{incipit,show,transcript}.txt`.

## 3. Root cause — one paragraph

`render.Prose` → `renderMarkdown` → glamour, and the loss is inside glamour.
**`ansi.TableElement.setStyles` (glamour v0.8.0, `ansi/table.go`) gives every
table cell a lipgloss style with `Inline(true)`**, which disables word wrap in
the cell render. Meanwhile lipgloss/table — resolved to **v1.1.0** by our
module graph, above the v0.12.1 glamour asks for — sizes each row to the
*wrapped* height of its content (`table/resizing.go`, `detectContentHeight`) and
then, because glamour never sets the `Wrap` flag, renders each cell in
`constructRow` as `ansi.Truncate(cell, cellWidth*height)` inside
`.Height(height).MaxWidth(cellWidth)`. So a cell needing three lines gets three
lines of *space*, one line of *text*, and two blanks; `MaxWidth` throws the rest
away. `internal/cli`'s `clipToWidth` is innocent — the characters were already
gone — which settles the question the brief posed: **the fix belongs in
`internal/render`, and both views get it for free.**

## 4. The fix

**glamour v0.8.0 → v1.0.0.** Upstream fixed exactly this: v0.10.0+ sets
`Inline(false)` and threads lipgloss/table's new `Wrap` flag through a
`WithTableWrap` option (default `true`). We pass `WithTableWrap(true)`
explicitly — see decision **D2**.

**Width recalibration, and it is not cosmetic.** glamour moved which side of the
word-wrap budget the dark style's 2-column document margin falls on:

| glamour | `WithWordWrap(n)` yields rows of | our compensation |
|---|---|---|
| ≤ v0.8.0 | `n + 2` columns | `wrap = width - 2` |
| ≥ v0.10.0 | `n - 2` columns | `wrap = width + 2` |

Left alone, **every paragraph in figaro would have lost 4 columns**. With
`width + 2`, rows come back at exactly `width` — byte-identical to before, which
is why `transcript_frames.golden` and its cell-level SGR proof pass **untouched**
across a major dependency bump. Measured across widths 8..140 on tables with
CJK, code spans and three columns: no table row exceeds `width` at this bias.

**`flake.nix` `vendorHash`** moves with `go.mod`:
`sha256-X/mdBj8snxUtLjvimWH5FohO6rHi7NdxYfPaia5eJxQ=`. `nix build .#figaro`
verified — without this, every dev shell breaks.

## 5. The expansion seam

Implemented with the names and signatures the brief specified, unchanged:

```go
func nodeExpandable(n livedoc.Node, width int) bool
func renderNode(n livedoc.Node, width, bashCap int, tick uint64, verbose, expanded bool) []string
```

`ariaView.RenderExpanded` feeds the transcript's existing `t.expanded[ref]` into
**both** caps — a tool's output cap as before, and now prose's table cap. One
flag, one gesture, two kinds of node.

**Files I touched, so ROSINA can see the boundary:** `internal/render/{render,table}.go`,
`internal/cli/nodes.go`, and eight lines of `internal/cli/livelog_bridge.go`
(`ariaView.RenderExpanded` only). I did **not** touch
`transcript_selection.go`, the input loop, or anything else in the transcript.

**What "collapsed" means for prose.** The wrap fix makes a table readable; it
also makes it *tall* (a 4-row table: 6 rows at 80 columns, 17 at 40). So
`clampTables` limits each rendered table to `proseTableCapDefault` = **12**
physical rows and replaces the remainder with a dim `… +N more table lines`. It
keeps the **head**, unlike a tool's output cap which keeps the tail: the end of
a command is the interesting part, but a table without its header row is not a
table. Returns the same backing array when nothing overruns, which is every
ordinary width.

`render.TableSpans` finds tables in already-rendered rows and requires a
**center rule** (`┼`), not merely a column rule (`│`) — glamour draws a
blockquote, which is how *every thinking and steering node* is rendered, with a
leading `│` on each line, so the looser test would have declared every thinking
block a table. `TestTableSpans_GlyphsMatchGlamour` renders a real table and
fails loudly if a future glamour changes its border set.

## 6. The incipit question — answered, and the first answer was wrong

**The incipit does not collapse. Only the transcript does.** This is the brief's
second option — *"it simply wraps better and leaves expansion to the pager"* —
and I arrived at it by trying the first one and measuring it fail.

My first answer was `^O`: the incipit has no selection, but it does already have
a "reveal more about this node" gesture, `^O` re-renders the live unit, and tools
there already respond to it. Driven in a real pty, **it does not work**. A table
clamped to `… +4 more table lines` was *still clamped* after `^O`:

```
--- COLLAPSED (default) ---
   forked   │ the trunk was branched in two, and this is one of the resulting
            │ children
  … +4 more table lines
--- EXPANDED (^O) ---
   forked   │ the trunk was branched in two, and this is one of the
            │ resulting children
  … +4 more table lines          <-- unchanged
```

Because flushed nodes are frozen in scrollback and never re-rendered
(architecture.md invariant #2), `^O` reaches only the still-live tail — and by
the time a table is on screen its prose node has usually been flushed. **A
collapsed form nothing can un-collapse is not a preview, it is data loss.**

So the rule became: *the collapsed form exists exactly where there is a gesture
to undo it.*

| surface | collapses? | why |
|---|---|---|
| incipit (`ariaView.Render`) | **no** | appends into native terminal scrollback, which the terminal scrolls; nothing can un-collapse a flushed node |
| `show` (`renderNodeList`) | **no** | a one-shot dump the reader scrolls; no viewport to husband, no gesture to undo |
| transcript (`ariaView.RenderExpanded`) | **yes** | managed viewport, a selection, an `expanded` map, and ROSINA's click |

`TestSurfaceContract_OnlyTheTranscriptCollapses` pins all four cells, and its
doc comment records the failed `^O` attempt so nobody re-derives it.

Re-verified in the same pty after the change. The incipit renders all seven
table rows with no hint row anywhere, and **one transcript frame shows both
halves of the contract at once** — the turn's inquiry in full, then the agent's
prose node clamped:

```
    state    │ meaning                                        <- the INQUIRY,
   ──────────┼──────────────────────────────────────────────     never clamped
    dormant  │ not loaded in memory; nothing is running and
   … (all seven rows) …
    orphaned │ the parent trunk is gone, so nothing addresses

    state    │ meaning                                        <- the agent's
   ──────────┼──────────────────────────────────────────────     PROSE NODE
    dormant  │ not loaded in memory; nothing is running and
   … (five rows) …
    forked   │ the trunk was branched in two, and this is one
   … +4 more table lines
```

Captures: `/tmp/susanna/repro/expand/out/` and
`/tmp/susanna/repro/tclamp/out/transcript.txt` (pager chrome present:
`? help` on the footer row, so this is genuinely the pager).

**Consequence for ROSINA:** prose expansion is *only* reachable through your
gesture. Until your branch lands, a clamped table in the transcript cannot be
opened at all. If that gap is too long, `proseTableCapDefault =
proseTableUncapped` (one constant) disables the collapse until you land.

## 7. Tests, and they have all failed

**The wrap regression** (`internal/render/table_wrap_test.go`) — canaried by
being committed *before* the fix. On `7ae3ab3`, 171 words lost:

```
table_wrap_test.go:64: two columns, prose in the second @ width 80: cell word "keep" lost
table_wrap_test.go:88: one long cell @ width 30: cell word "w05" lost
...
```

**The painter invariant** (`TestProse_TableRowsHoldPainterInvariant`, widths
8..120) passed *before* the fix, deliberately — it is the guard a wrapping
change is most likely to trip, so it had to be in place first. It still passes.

**The expansion tests**, canaried by setting `proseTableCapDefault` to
`proseTableUncapped`:

```
--- FAIL: TestNodeExpandable
    nodes_expand_test.go:52: prose with a table taller than the cap: nodeExpandable = false, want true
    nodes_expand_test.go:52: thinking with a tall table: nodeExpandable = false, want true
--- FAIL: TestRenderProseNode_CollapseHidesAndExpandRestores
    nodes_expand_test.go:96: expanded (17 rows) must be taller than collapsed (17 rows)
```

`TestNodeExpandable_AgreesWithTheRender` checks the predicate against the two
*renders* rather than a hand-written table, because a predicate that claims
"expandable" while both renders are identical is exactly the silent no-op click
it exists to prevent. `TestClampTables_HoldsPainterInvariant` re-asserts
invariant #1 over the one row this package writes itself — the hint row —
across widths 26..120 in both states.

## 8. Risk

- **A major dependency bump** brings goldmark 1.7.4→1.7.13 and chroma
  2.14→2.20 with it, so *code-block syntax highlighting and markdown edge
  cases* can shift in ways no test in this repo pins. Mitigated: the full
  suite is green including the byte-level frame goldens and the SGR proof; the
  paragraph/CJK/code-span row widths are byte-identical.
- **glamour v1.0.0 pins lipgloss to an unreleased pseudo-version**
  (`v1.1.1-0.20250404203927-76690c660834`). Both v0.10.0 and v1.0.0 require it;
  there is no released escape. See **D1**.
- **A new truncation exists where none did.** Prose was never capped before.
  It only bites on a table over 12 rows, and it announces itself — but it is a
  behaviour change, and **D3** is the switch.
- **`TableSpans` is structural, not semantic.** It reads glamour's output
  rather than the markdown. A one-column table is not recognised (no `│` to
  cross, so no `┼`); the failure mode is "we leave it alone".

## 9. What I did NOT do

- **Did not touch `main`**, and did not touch `transcript_selection.go`,
  `toggleSelectedTools`, or the input loop. The gesture is ROSINA's.
- **Did not run the tmux smoke suite.** It drives a real provider and costs
  tokens on every case; the two scripts above are targeted equivalents built
  from the same recipe, and I would rather ROSINA decide whether to spend the
  suite. `FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v`
  remains unrun on this branch — **say the word and I will run it.**
- **Did not write our own table renderer.** It was the no-bump alternative:
  intercept table blocks and render them on `lipgloss/table` with `Wrap(true)`,
  which we already have in the graph. It means re-implementing inline markdown
  inside cells (code spans, emphasis, links) — i.e. re-implementing glamour's
  `TableCellElement`. Available if **D1** goes against the bump; expect a day,
  not an hour.
- **Did not make the new hint searchable.** The transcript's search predicate
  matches a tool's `… last 10 of 42 lines` string (`transcript.go:2058`); the
  prose `… +N more table lines` hint is not matched. Small, isolated, and it
  belongs next to the search work rather than here.
- **Did not clamp the inquiry.** A node is the agent's output and may be
  summarised; the question is the user's own text. See **D6**.
- **Did not change `show`'s verbosity.** It passes `verbose: true`
  unconditionally (`aria.go:197`); `renderNodeList` now also passes
  `expanded: true` unconditionally, so `figaro show` renders every table **in
  full, always** — see §6 and **D5**.
- **Did not fix widths below 26 columns.** See §11.

## 10. The taste call, with both options captured

The wrap style is one flag in one place (`internal/render/render.go`):

**Option A — `WithTableWrap(true)` (what is on the branch).** Cells wrap; a
table grows taller and loses nothing.

```
   state   │ meaning
  ─────────┼──────────────────────────────────────────────────────────────────
   dormant │ not loaded in memory; nothing is running and the aria costs
           │ nothing to keep
   active  │ the inbox is non-empty, so a turn is in flight right now
```

**Option B — `WithTableWrap(false)`.** Cells stay on one line, truncated with a
visible `…`. Compact, honest about the truncation (unlike v0.8.0's silent
blanking), and a table always costs exactly one row per markdown row.

**Option C — B for narrow panes, A above a threshold.** Not built. It is a
width test around the same flag, so it stays cheap.

And the cap, orthogonally: `proseTableCapDefault = 12` vs `proseTableUncapped`.

## 11. The residual: below 26 columns

Post-fix, measured at the `render.Prose` boundary, on the three test tables:

| width | cell words lost | rows for a 2-row table |
|---|---|---|
| ≥ 26 | 0 | 11 → 4 |
| 24 | 1 / 13 | 14 |
| 20 | 3 / 13 | 20 |
| 16 | 9 / 13 | 23 |
| 12 | 11 / 13 | 39 |
| 10 | 13 / 13 | **67** |
| 8 | 13 / 13 | 4 |

Below ~26 columns glamour's table geometry squeezes columns below one word wide
and both loses text *and* inflates absurdly. No real terminal is 24 columns, and
the transcript renders at `t.w - 2`, so the floor is around a 28-column pane —
but it is not zero, and expansion cannot help because the loss is inside
glamour either way.

The clean answer, if the user wants one: when a table's rendered form would be
lossy, **abandon the grid** and render each markdown row as a small labelled
block (`dormant` / `  meaning: …`), which wraps to any width without loss. That
is a real feature with a real look, so it is **D4** and not something I took.

---

## Decisions for the user

| # | Decision | My recommendation |
|---|---|---|
| **D1** | Take the **glamour v0.8.0 → v1.0.0 bump**, accepting that it pins lipgloss to an unreleased pseudo-version (`v1.1.1-0.2025…`)? The alternative is hand-rolling a table renderer (§9). | **Take it.** It is upstream's own fix for exactly this bug, the suite including byte-level goldens is green, and the pseudo-version is unavoidable in any glamour that wraps. |
| **D2** | **Wrap** (A), **truncate with `…`** (B), or **width-dependent** (C)? §10. | **A.** It is what "so the whole text can be read" asks for; B is one flag away if tall tables annoy. |
| **D3** | Keep the collapsed table cap at **12 rows** in the transcript, or `proseTableUncapped`? | **Keep 12.** It never bites at ordinary widths and it is what gives prose an expandable form at all — set it uncapped and `nodeExpandable` correctly reports prose as never expandable, which leaves ROSINA's gesture with only tools again. But see §6: until her branch lands, a clamped table cannot be opened, so **uncapped is the right interim** if the branches will not land together. |
| **D3b** | Should the incipit also get a collapse, via some gesture that can actually reach a flushed node? | **No.** §6 — scrollback is not a scarce viewport, and invariant #2 means there is no honest way to un-collapse there. |
| **D4** | Widths below ~26 columns still lose text (§11). Leave it, or build the linear no-grid fallback? | **Leave it for now**, revisit if anyone actually works in a 24-column pane. |
| **D5** | `figaro show` renders every table in full (it hardcodes `verbose: true`, and `renderNodeList` now passes `expanded: true` regardless). Right? | **Right.** `show` is a dump you scroll; hiding rows there helps nobody and nothing could reveal them. |
| **D6** | Should a turn's **inquiry** ever be clamped? Today it never is. | **Never.** figaro should not truncate the user's own words. |
| **D7** | `nodeExpandable` says a **tool** is expandable whenever it has output — the same liberal test `toggleSelectedTools` used, so ROSINA's generalization is behaviour-preserving. Tighten it to "output actually exceeds the cap"? | **Tighten it later, not in this branch.** It is one line, but it changes what a click on a small tool does and that belongs with the gesture. |
| **D8** | Spend the **tmux smoke suite** (real provider, real tokens) on this branch before merge? | **Yes, once**, since this touches everything that paints. I did not spend it unasked. |

*— SUSANNA*
