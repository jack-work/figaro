# Transcript paging policy — measurements and the geometry we chose

*Axis D of the transcript-performance work: the policy layer (what the pager
holds, when it fetches, what it throws away), as distinct from the per-frame
cost of turning a message into rows.*

**This document has been re-derived on the merged stack** (axis C's allocation
surgery + axis A's viewport virtualization + this axis). D's solo findings are
kept where they still hold and marked where the merge invalidated them — in one
case the conclusion survived, the *reason* for it did not, and the constant it
picked moved. See "What the merge changed" at the end for the short version.

All numbers: go1.26, Ryzen 7 5800X, `internal/cli`, terminal 100x40, heavy
messages (thinking block + 3 wrapped paragraphs + a bash node with a large
captured output + a short closing paragraph ≈ 45 rendered rows each). The
machine was shared during the merge run, so wall-clock figures are minimum-of-7;
the *counters* (fetches, refetches, re-renders, retained rows) are deterministic.

## The instruments

`transcript_journey_bench_test.go` adds what the shared scroll rig cannot see:

- **`pagingHarness`** drives the transcript exactly as the input loop does
  (`key` → `pageCursor` → `ReadBefore` → `applyPage`), serves history from
  memory with optional injected latency, and derives fetch / eviction / refetch
  counters by diffing the retained page set. Node re-renders are counted by
  wrapping the `NodeView`, so no production code carries instrumentation. The
  merge added a **peak retained-bytes** sampler (`rowCacheFootprint` after every
  `applyPage`), because once the frame is O(viewport) memory is the only cost
  that still scales with the window.
- **`BenchmarkTranscriptJourney`** — scroll back 120 messages and return.
- **`BenchmarkTranscriptFollowFrame` / `LiveStream`** — frames while following
  the live tail.
- **`BenchmarkTranscriptGeometry{Frame,Follow,Journey,Enter,LRU}`** and
  **`TestTranscriptGeometryDepthReport`** — the sweeps that picked the constants.
  `Follow`, `Enter` and the depth report were added by the merge, for reasons
  the next section explains.

## Finding 1: the per-frame paging rebuild is real but tiny — *and the merge made it matter*

`lines()` ran `if t.follow { t.resetToTail() }`, so every live frame rebuilt the
page window: `client.View()` (which copies *and sorts* the retained closed set),
`describePage` (FNV over every LT), a fresh `[]transcriptPage`, and
`pruneCaches` (a map over every retained message).

Measured on D solo: **3960 ns and 13 allocations per frame**, against a frame
that cost **3.9 ms**. That is 0.1%, and D reported honestly that the hypothesis
"this is the lag" was wrong.

On the merged stack the same 3960 ns would have been **a third of a 12 µs
frame**. D's second justification — "it is a *fixed* cost per frame, O(retained
tail); once the frame is O(viewport) that is a large fraction of it" — turned
out to be exactly right, sooner than expected. It is also why a follow frame
still allocates only 1.2 KB instead of 7.6 KB.

Fix: `aria.Client.ClosedRevision()` + `Open()`; the transcript remembers the
revision its window was built from and no-ops the rebuild while it holds.

### The merge's own correction: one authority, not two

D's `tailRev` answers "are `t.pages` still a pristine snapshot of the client's
closed tail?". A's line index had a *separate* staleness notion — a per-frame
shape diff deciding whether to refill `lineLT`. Two independent checks over the
same fact is how this file grows a bug: a page set that moves without the index
noticing leaves `lineLT` (resize anchoring, `viewportAnchor`) describing a window
that no longer exists.

They are now one signal. `transcript.invalidateWindow()` is the single authority:
every mutation of `t.pages` goes through it, it clears `tailRev` (so the page
layer rebuilds) **and** bumps `windowRev` (which `buildIndex` records, so the
index always refills). The shape diff survives only for the changes the page set
cannot see — a width change, an expanded tool, the open message growing a token.
`TestMergedPageMutationAlwaysReachesTheIndex` walks every route that moves
`t.pages` (page in older, evict, page in newer from the LRU, `G`, resize) and
checks `lineLT` against an independent recomputation from the index each time;
`TestMergedFollowFrameLeavesTheWindowAlone` checks the signal stays *silent*
when nothing changed, which is the half that keeps D's optimization alive.

Line-space size likewise has one spelling now: `t.index.total`. `pageCursor`
used to read `len(t.lineLT)`, which is only *incidentally* the same number.

## Finding 2: message count is the wrong unit for the window

This is where the lag actually lived, and it is a policy problem. It survives the
merge unchanged.

The old geometry was 3 pages × 30 messages. A message is 4 rows or 400, so:

| aria | retained window |
|---|---|
| one-line answers | ~180 rows |
| heavy tool output | **4137 rows** (≈ 100 screens) |

The window is now expressed in **rows**: `transcriptWindowRows` is the budget
across all retained pages, and the per-fetch message count is derived from the
rows-per-message of the aria being read (`pageMessages()`, clamped to
`[6, 30]`) and carried on the page request. Light arias never reach the budget
and keep exactly the old 30×3 geometry.

### The merge makes the measurement exact

D estimated rows-per-message by averaging `len(rows)` over `t.rowCache`, and
listed "a per-page row count would make the geometry exact" as future work. The
merge does not need a new count — **A's line index already has it**, per message,
recomputed every frame as a side effect of drawing. `heldWindow()` reads it.

That is not a cosmetic swap. The row-cache average was wrong in two directions
that D's own changes created:

- since rows follow payloads into the LRU (finding 3), `t.rowCache` holds up to
  `transcriptPayloadLRULimit` extra pages of *history* — messages the window does
  not hold — so the geometry was tuned on the wrong population;
- it charged a flat `+3` separator to every message including the first.

The index also knows *which* entry is the open message, which the row cache never
did. That matters twice: the budget excludes it (it is a live message, it is not
retained, and it changes height every token), and `tuneTail` can finally
distinguish "the retained history is over budget" (committed rows) from "the
viewport looks half-empty" (all rows, live message included). D drove both off
the single number `len(t.lines())`, so a 400-row streaming reply could push the
window past `budget*5/4` and shrink the retained history out from under it.
`TestMergedGeometryMeasuresTheWindowNotTheRowCache` and
`TestMergedOpenMessageIsExcludedFromTheBudget` pin both.

### Why 1200 was right for D solo, and why the merged answer is 2400

**D's reason for 1200 no longer exists.** D measured frame cost as almost exactly
linear in retained rows (~3 µs/row: ~300 rows → 0.89 ms, 1377 rows → 4.22 ms) and
picked the knee of that curve. Axis A's line index removed the curve. On the
merged code (`BenchmarkTranscriptGeometryFrame` / `GeometryFollow`):

| window rows | scroll frame | live-follow frame |
|---|---|---|
| 825 | 11.5 µs | 14.3 µs |
| 1101 | 11.2 µs | 14.7 µs |
| 1791 | 11.6 µs | 13.6 µs |
| 2343 | 11.5 µs | 14.5 µs |
| 4137 | 12.7 µs | 14.7 µs |

Flat, both scrolling and following, over a 5× range of retained rows. (Follow is
the one worth checking: A left exactly one O(retained rows) step on the frame
path — `rebuildLineLT`, which a streaming open message triggers every frame — and
it is an int store per row, lost in the noise even at 4137 rows.)

What is left is churn versus retained memory. The 120-message round trip
(`GeometryJourney`), payload LRU at 12:

| budget | window rows | fetches | refetched msgs | node re-renders | journey allocs | peak retained | cold `enter` |
|---|---|---|---|---|---|---|---|
| 300 | 825 | 26 | 30 | 648 | 4.68 MB / 20547 | 1434 KB | 3.58 ms |
| 600 | 825 | 26 | 30 | 648 | 4.68 MB / 20547 | 1434 KB | 3.67 ms |
| 1200 | 1101 | 16 | 0 | 544 | 3.95 MB / 18410 | 1912 KB | 3.80 ms |
| 1800 | 1791 | 10 | 0 | 572 | 4.04 MB / 19138 | 2278 KB | 4.39 ms |
| **2400** | **2343** | **8** | **0** | **612** | 4.20 MB / 19889 | 2438 KB | 4.79 ms |
| 4800 | 4137 | 4 | 0 | 600 | 3.74 MB / 17415 | 2389 KB | 6.55 ms |

(300 and 600 are identical because `transcriptMinPageSize` floors a page at 6
messages: on a heavy aria every budget below ~800 rows produces the same window.
The sweep keeps 300 so that floor is visible rather than hidden.)

Read alone this table says 1200 again — it is where refetch churn hits zero. But
that is a **fixture artifact**, and `TestTranscriptGeometryDepthReport` shows it:

| journey depth | 300 | 600 | 1200 | 1800 | 2400 | 4800 |
|---|---|---|---|---|---|---|
| 60 — fetches / refetched | 10/0 | 10/0 | 8/0 | 5/0 | 4/0 | 2/0 |
| 120 — fetches / refetched | 26/30 | 26/30 | 16/0 | 10/0 | 8/0 | 4/0 |
| 240 — fetches / refetched | 66/150 | 66/150 | 46/120 | 24/52 | **16/0** | 8/0 |
| 240 — node re-renders | 1608 | 1608 | 1504 | 1300 | **1156** | 1080 |
| 240 — peak retained | 1433 KB | 1433 KB | 1911 KB | 3106 KB | 4062 KB | 4301 KB |

The churn threshold tracks **how far you scroll**, not the budget. The pager
retains the window plus `transcriptPayloadLRULimit` pages of it — about 5× the
budget in rows — so 1200 bought a churn-free journey of ~120 messages and nothing
more. Beyond that depth a bigger window is not just fewer fetches, it is *less
CPU*: at depth 240, 2400 does 16 fetches instead of 46, refetches nothing instead
of 120 messages, and re-renders 1156 nodes instead of 1504 (−23%).

**2400** is the choice:

- it doubles the free journey to ~240 messages, which is the difference between
  "go back to what we discussed this morning" being free and costing 120
  refetched messages plus 350 extra prose renders;
- it costs ~1.6 MB more retained rows on a deep trip and ~1 ms more on Ctrl-T
  (cold enter renders one page: 3.80 → 4.79 ms). Both are smaller than **one**
  pre-merge frame's 4 MB of allocation;
- the upper bound is principled rather than arbitrary: at 4800 the derived page
  size saturates the `transcriptPageSize` ceiling (1600/45 > 30), so the
  rows-based geometry degenerates into the message-count geometry it replaced.

The knee moved because the constraint moved. It was CPU-per-retained-row; it is
now bytes-per-retained-row, and bytes are much cheaper than microseconds were.

### Cold start, inverted

Unchanged by the merge, and it is why the budget can be raised at all. With a
cold row cache the pager cannot know how tall the messages are, and the old code
guessed the *ceiling* (30 messages): opening the pager on an aria of 400-row tool
dumps rendered thirty of them to paint one screen. It now starts at the floor (6)
and `tuneTail` grows the window into the row budget once the first frame has
measured it — so `enter` costs *one page*, not one window, and the 3.80 → 4.79 ms
above is the whole price of doubling the budget.

Invisible by construction: the tail window is anchored at the newest message with
the viewport pinned to the bottom, so growth and shrink only change what is
retained *above* the screen (`TestTranscriptRowBudgetShowsIdenticalFrame`
compares the visible rows against a pager with an unbounded budget).

## Finding 3: eviction threw away the expensive half

`trimPages` did `rememberPayload(page)` immediately followed by `dropPage(page)`.
The payload LRU exists precisely so that turning around at a page boundary costs
no I/O — and then `dropPage` deleted the page's `rowCache` entries, so the return
trip served the messages from memory and re-rendered every one of them. The cheap
thing was retained and the expensive thing discarded.

Rows now follow payloads: `dropPage` skips messages whose payload the LRU still
holds, and rows are released when the payload leaves the LRU (or `pruneCaches`
runs — it learned about the LRU too). Expansion state rides with the rows,
because cached rows are rendered *with* the expansion state and the two must
never disagree.

### The LRU sweep at the new budget

D sized the LRU at 12 pages (4 windows) from a 120-message trip at budget 1200,
where 3 → 25 fetches / 72 refetched / 920 re-renders and 12 → 16 / 0 / 632. At
budget 2400 that sweep no longer discriminates, because the pages are twice the
size (`GeometryLRU`, depth 120):

| LRU pages | fetches | refetched msgs | re-renders | peak retained |
|---|---|---|---|---|
| 3 | 11 | 34 | 816 | 1625 KB |
| 6 | 8 | 0 | 612 | 2438 KB |
| **12** | **8** | **0** | **612** | 2438 KB |
| 24 | 8 | 0 | 612 | 2438 KB |

Read at depth 120 this says "6 is enough". It is kept at **12** because the
budget was raised on the strength of the depth-240 result, and that result needs
it: window (51 messages) + 12 × 17-message pages = 255 messages of retained
history, which is what makes a 240-message journey churn-free. 6 would retain 153
and put the churn back. The sweep must be read at a depth that exercises it —
that is what `TestTranscriptGeometryDepthReport` is for.

The memory ceiling this implies is worth stating plainly:
`(1 + LRU/pageLimit) × transcriptWindowRows` = **5 × the budget in rows**, so
~12000 rows ≈ 4.3 MB at the shipped constants.

## Finding 4: the fetch was both late and blocking

- **Late.** `pageCursor` refused an older page until the viewport was already
  inside the last screen of retained rows, so the RPC was issued at the instant
  the user hit the wall. Now armed `transcriptPrefetchScreens` (2) viewports
  from either edge.
- **Blocking.** The input loop called `pageTranscript()` inline after every key
  and wheel event, and the `ReadBefore` ran on that goroutine — so during a
  fetch the pager processed no keys and painted no frames. The scroll froze and
  then jumped. Fetches now run on a single-flight worker that, when a page
  lands, re-asks the pager whether the viewport has since moved close to another
  edge and chains into the next page.

`TestTranscriptScrollDoesNotBlockOnHistoryFetch` injects a 750 ms history read
and asserts ten scroll keys are still consumed in under half that. Against the
synchronous implementation it fails with `offset 0 after 755ms`.

Note that the async worker weakens the *latency* argument for fewer fetches
(they are off the critical path now) but not the *work* argument: each fetch
still costs an RPC, a `describePage`, an `applyPage` under the render mutex, and
whatever re-rendering the arriving page needs. That is why the geometry table
above is read on re-renders and retained bytes, not on fetch count alone.

## Finding 5: `describePage` hashing is not a problem

`describePage` FNV-hashes the LTs of a page so an evicted page can be verified
when refetched (`pageDesc.equal`). It is called once per fetch and once per tail
rebuild — 30 iterations of an 8-byte write, ~200 ns, immeasurable next to the
page's row rendering. Now that the tail rebuild is revision-guarded it runs a
handful of times per turn. **No change made**; the verification it enables
(rejecting a stale page mid-search) is worth far more than the hash costs.

## Net effect on the merged stack

Minimum-of-7, `-benchtime 200x -cpu 1`, shared machine. "pre-merge" is
C+A (`perf/transcript` at 42c6bd0); "D solo" is `perf/d-paging`, which has
neither C's row primitives nor A's index.

| benchmark | D solo (`perf/d-paging`) | pre-merge (C+A) | **merged** |
|---|---|---|---|
| ScrollHeavy/out20 | 1149 µs · 1.12 MB · 2610 | 16.1 µs · 1144 B · 17 | **15.2 µs · 1160 B · 17** |
| ScrollHeavy/out200 | 1102 µs · 1.12 MB · 2609 | 10.7 µs · 1144 B · 17 | **10.3 µs · 1160 B · 17** |
| ScrollBurst (wheel flick) | 26.6 ms · 26.7 MB · 62524 | 267 µs · 27.5 KB · 408 | **246 µs · 27.8 KB · 408** |
| RenderHeavy/out20 | 1125 µs · 1.06 MB · 2563 | 15.7 µs · 1144 B · 17 | **14.7 µs · 1160 B · 17** |
| RenderHeavy/out200 | 1078 µs · 1.06 MB · 2563 | 10.3 µs · 1144 B · 17 | **9.7 µs · 1160 B · 17** |
| LinesHeavy | 1071 µs · 1.06 MB · 2532 | 10.8 µs · 302 B · 0 | **5.7 µs · 179 B · 0** |
| ScrollHeavySearch | 1224 µs · 1.25 MB · 2905 | 18.4 µs · 2328 B · 28 | **19.8 µs · 2344 B · 28** ⚠ |
| HeavyEnter (Ctrl-T) | 10.19 ms · 4.87 MB · 76591 | 3.14 ms · 817 KB · 2088 | **1.87 ms · 493 KB · 1215** |
| FollowHeavy | — (bench is axis A's) | 13.3 µs · 7636 B · 27 | **10.3 µs · 1176 B · 18** |
| LiveHeavy | — (bench is axis A's) | 15.2 µs · 9767 B · 40 | **12.1 µs · 3085 B · 29** |
| Journey/out20 (120 msgs, round trip) | 2.02 s · 1.92 GB · 5.59 M | 27.9 ms · 5.18 MB · 21465 | **33.4 ms · 4.20 MB · 19889** ⚠ |
| Journey/out200 | 1.93 s · 1.93 GB · 5.59 M | 28.9 ms · 5.18 MB · 21465 | **30.5 ms · 4.20 MB · 19889** |

Journey policy counters (same trip):

| | fetches | refetched msgs | node re-renders | keys |
|---|---|---|---|---|
| D solo | 16 | 0 | 544 | 542 |
| pre-merge (C+A, old message-count geometry) | 4 | 0 | 840 | 545 |
| **merged** | **8** | **0** | **612** | **614** |

### The two ⚠ lines

**`ScrollHeavySearch` +7%** (18.4 → 19.8 µs; reproduced interleaved, min-of-3
across three alternating runs, so it is not drift in the machine's load). It is
*not* a new code path:

- the painted frame is byte-identical — 37 rows, 11 matches, 1186 escape
  sequences, 28 allocations, in both trees;
- the same frame *without* a search highlight (`ScrollHeavy/out200`) is faster
  after the merge, and forcing the merged window to the pre-merge size does not
  move the search number (20.1 µs), so it is not the geometry either;
- the CPU profiles are the same shape, and the extra ~1 µs lands proportionally
  in `visibleIndex`, `skipANSI` and `uniseg.StepString` — functions the merge
  does not touch, running on identical input.

That is code-layout/inlining drift from a larger `transcript` struct and a few
more methods in the package. Worth recording, not worth chasing.

**`Journey` +20% wall on out20** (27.9 → 33.4 ms) while doing *less work*:
−27% node re-renders (840 → 612), −19% allocated bytes, −8% allocations. This is
the rows-based geometry doing exactly what it was built to do. The pre-merge tree
still has the message-count window (3 × 30 = 4137 rows for this aria), so a
120-message journey needs only 4 page turns; the merged window is 2343 rows and
needs 8, and the return leg costs 69 more keystrokes-with-repaint. Journey wall
is monotone in window size and nothing else — the merged tree at budget 4800
(≈ the old geometry) runs the same trip in 24.4 ms, i.e. *faster* than pre-merge.

So this is the price of the smaller retained window, and it buys the −40% Ctrl-T,
the −85% follow-frame garbage, and a pager whose frame cost no longer depends on
what the aria happens to contain. It is also 2.5× better than it would have been
at D's solo budget of 1200 (16 fetches).


## What the merge changed, in one list

1. **`transcriptWindowRows` 1200 → 2400.** D's knee was the knee of *frame cost
   vs retained rows*; A flattened that curve, and the merged constraint (churn vs
   retained memory, crossover set by scroll depth) supports twice the window.
2. **The geometry reads A's line index, exactly**, instead of averaging the row
   cache — which, after D's own LRU change, is no longer the retained window.
   D's "not done, worth doing" item is done.
3. **`tuneTail` distinguishes committed rows from total rows**, which only the
   index makes possible, fixing a case where a long streaming reply could shrink
   the retained history.
4. **One authority on "the retained window changed"** (`invalidateWindow` →
   `tailRev` + `windowRev`), replacing D's page-level check and A's index-level
   shape diff sitting side by side with nothing tying them together.
5. **New instruments**: `GeometryFollow` (the budget-sensitivity of the one
   remaining O(rows) frame step), `GeometryEnter` (the cost that *does* scale
   with the budget now), peak-retained-bytes in the journey harness, and
   `TestTranscriptGeometryDepthReport` (which showed the 1200 knee was a fixture
   artifact).

## Behaviour changes an integrator must know

1. **The retained window is smaller than the old message-count geometry for
   heavy arias, but larger than D solo shipped** (~2343 rows vs D's ~1101 and the
   original 4137). The footer's scroll total (`48–97/97`) is relative to the
   retained window, so those numbers changed.
2. **Expansion state survives a round trip.** Rows and `expanded` are dropped
   together (they must be: cached rows are rendered *with* the expansion state),
   so a tool you expanded stays expanded when you scroll past it and back.
   Previously page eviction silently collapsed it.
3. **Pages are applied from a worker goroutine** under the shared render mutex,
   like the existing historical-search worker. `applyPage` no-ops when the pager
   is closed; the 5 s per-fetch timeout is unchanged.
4. **`t.index.total` is the line-space size.** `len(t.lineLT)` happens to equal
   it, but `lineLT` is only refilled when the index shape moves; do not reach for
   its length.

## Not done, worth doing

- `interactiveInput.enterTranscript()` still performs two *synchronous* RPCs
  before the pager opens. On a local daemon that is milliseconds, but it is the
  same class of bug as the scroll stall: the pager could open on the model it
  already has and fold the reads in when they land.
- `rebuildLineLT` is the last O(retained rows) step on the frame path, and a
  streaming open message triggers it every frame. It is currently ~1 µs of a
  14 µs frame, so it is not worth fixing — but it is the step that would make
  the budget matter again, and it is trivially fixable (the open message is the
  *last* index entry, so only its own lines need refilling).
- `pageMessages()` derives one page size from the window average. Now that the
  index carries a real row count per message, `trimPages` could enforce the row
  budget directly and let pages be whatever size they happened to arrive at.
