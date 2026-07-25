# Transcript paging policy — measurements and the geometry we chose

*Axis D of the transcript-performance work: the policy layer (what the pager
holds, when it fetches, what it throws away), as distinct from the per-frame
cost of turning a message into rows.*

All numbers: go1.26, Ryzen 7 5800X, `internal/cli`, terminal 100x40, heavy
messages (thinking block + 3 wrapped paragraphs + a bash node with a large
captured output + a short closing paragraph ≈ 46 rendered rows each).

## The instruments

`transcript_journey_bench_test.go` adds what the shared scroll rig cannot see:

- **`pagingHarness`** drives the transcript exactly as the input loop does
  (`key` → `pageCursor` → `ReadBefore` → `applyPage`), serves history from
  memory with optional injected latency, and derives fetch / eviction / refetch
  counters by diffing the retained page set. Node re-renders are counted by
  wrapping the `NodeView`, so no production code carries instrumentation.
- **`BenchmarkTranscriptJourney`** — scroll back 120 messages and return.
- **`BenchmarkTranscriptFollowFrame` / `LiveStream`** — frames while following
  the live tail.
- **`BenchmarkTranscriptGeometryFrame` / `GeometryJourney` / `GeometryLRU`** —
  the sweeps that picked the constants.

## Finding 1: the per-frame paging rebuild is real but tiny

`lines()` ran `if t.follow { t.resetToTail() }`, so every live frame rebuilt the
page window: `client.View()` (which copies *and sorts* the retained closed set),
`describePage` (FNV over every LT), a fresh `[]transcriptPage`, and
`pruneCaches` (a map over every retained message).

Measured: **3960 ns and 13 allocations per frame**, against a frame that cost
**3.9 ms**. That is 0.1%. The hypothesis that this was "the lag" is wrong, and
the profile agrees — a follow-frame CPU profile is 53% `decorateNodeRow` →
`clipToWidth` and 17% `renderProseNode`, with `resetToTail` not appearing.

It was still worth fixing, for two reasons that are not about its own cost:

1. It is a *fixed* cost per frame, O(retained tail). Once the frame is
   O(viewport) (sibling axes), 4 µs is ~8% of a frame.
2. It reallocated `t.pages` every frame, so the page window had no stable
   identity — nothing keyed on a page could be cached across frames.

Fix: `aria.Client.ClosedRevision()` + `Open()`; the transcript remembers the
revision its window was built from and no-ops the rebuild while it holds.
Now **5.2 ns, 0 allocations**.

## Finding 2: message count is the wrong unit for the window

This is where the lag actually lived, and it is a policy problem.

The old geometry was 3 pages × 30 messages. A message is 4 rows or 400, so:

| aria | retained window |
|---|---|
| one-line answers | ~180 rows |
| heavy tool output | **4137 rows** (≈ 100 screens) |

Every frame materializes the whole window to show 37 rows of it, so the frame
cost was a function of the *aria's content*, not of the viewport. Frame cost is
almost exactly linear in retained rows (~3 µs/row on this machine — see
`BenchmarkTranscriptGeometryFrame`):

| window rows | frame |
|---|---|
| ~300 | 0.89 ms |
| ~400 | 1.18 ms |
| 1377 | 4.22 ms |

So the window is now expressed in **rows**: `transcriptWindowRows` is the budget
across all retained pages, and the per-fetch message count is derived from the
*measured* rows-per-message of the aria being read
(`pageMessages()`, clamped to `[6, 30]`) and carried on the page request. Light
arias never reach the budget and keep exactly the old 30×3 geometry.

### Why 1200 rows

The same 120-message round trip at each budget (`GeometryJourney`, payload LRU
sized at 12):

| budget | wall | fetches | refetched msgs | node re-renders | allocs | frame |
|---|---|---|---|---|---|---|
| 600 | 1.50 s | 26 | 30 | 744 | 4.91M | 0.89 ms |
| **1200** | **1.85 s** | **16** | **0** | **632** | **5.64M** | **1.18 ms** |
| 2400 | 4.75 s | 8 | 0 | 664 | 12.56M | 4.22 ms |

1200 is the knee: zero refetch churn, minimum re-renders, 2.6× less CPU and
allocation than 2400. Dropping to 600 saves 20% CPU but doubles the fetch count
and brings refetch churn back.

### Cold start, inverted

With a cold row cache the pager cannot know how tall the messages are, and the
old code guessed the *ceiling* (30 messages): opening the pager on an aria of
400-row tool dumps rendered thirty of them to paint one screen — 35 ms and 15 MB
for one keystroke. It now starts at the floor (6) and `tuneTail` grows the window
into the row budget once the first frame has measured it. This is invisible: the
tail window is anchored at the newest message with the viewport pinned to the
bottom, so growth and shrink only change what is retained *above* the screen
(`TestTranscriptRowBudgetShowsIdenticalFrame` compares the visible rows against
a pager with an unbounded budget). `HeavyEnter`: 35.3 ms → 10.4 ms.

## Finding 3: eviction threw away the expensive half

`trimPages` did `rememberPayload(page)` immediately followed by
`dropPage(page)`. The payload LRU exists precisely so that turning around at a
page boundary costs no I/O — and then `dropPage` deleted the page's `rowCache`
entries, so the return trip served the messages from memory and re-rendered
every one of them. The cheap thing was retained and the expensive thing
discarded.

Rows now follow payloads: `dropPage` skips messages whose payload the LRU still
holds, and rows are released when the payload leaves the LRU (or `pruneCaches`
runs — it learned about the LRU too). On the journey: **840 → 600 node
re-renders** (−29%).

Because rows-based pages are small, the LRU has to hold more of them to span the
same history (`GeometryLRU`, 120-message trip):

| LRU pages | fetches | refetched msgs | re-renders |
|---|---|---|---|
| 3 | 25 | 72 | 920 |
| 6 | 22 | 48 | 824 |
| **12** | **16** | **0** | **632** |
| 24 | 15 | 0 | 600 |

12 (= 4 windows) eliminates refetch churn on a 120-message round trip; 24 buys
almost nothing more. Cost is memory only: ~96 messages of payload plus their
rows, well under a megabyte — less than *one* old frame's allocation.

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

## Finding 5: `describePage` hashing is not a problem

`describePage` FNV-hashes the LTs of a page so an evicted page can be verified
when refetched (`pageDesc.equal`). It is called once per fetch and once per tail
rebuild — 30 iterations of an 8-byte write, ~200 ns, immeasurable next to the
page's row rendering. Now that the tail rebuild is revision-guarded it runs a
handful of times per turn. **No change made**; the verification it enables
(rejecting a stale page mid-search) is worth far more than the hash costs.

## Net effect

| benchmark | before | after |
|---|---|---|
| ScrollHeavy/out20 | 4.30 ms · 4.03 MB · 9513 allocs | 1.18 ms · 1.12 MB · 2613 |
| ScrollHeavy/out200 | 4.05 ms · 4.03 MB · 9513 | 1.10 ms · 1.12 MB · 2612 |
| ScrollBurst (wheel flick) | 99.7 ms · 96.5 MB · 228164 | 26.3 ms · 26.7 MB · 62579 |
| RenderHeavy/out20 | 4.32 ms · 3.97 MB · 9463 | 1.13 ms · 1.06 MB · 2565 |
| LinesHeavy | 4.02 ms · 3.96 MB · 9427 | 1.11 ms · 1.06 MB · 2532 |
| ScrollHeavySearch | 4.61 ms · 4.52 MB · 10622 | 1.28 ms · 1.25 MB · 2909 |
| HeavyEnter (Ctrl-T) | 35.3 ms · 15.1 MB · 279797 | 10.4 ms · 4.87 MB · 76603 |
| Journey (120 msgs, round trip) | 5.71 s · 5.94 GB · 15.48M | 2.07 s · 1.93 GB · 5.59M |
| FollowFrame | 4.13 ms · 3.98 MB · 9480 | 1.28–1.52 ms · 1.06 MB · 2565 |

## Behaviour changes an integrator must know

1. **The retained window is smaller for heavy arias** (~400 rows at the tail
   instead of 1377). The footer's scroll total (`48–97/97`) is relative to the
   retained window, so those numbers are smaller, and scrolling up crosses page
   boundaries sooner — which is why prefetch shipped in the same stack.
2. **Expansion state survives a round trip.** Rows and `expanded` are dropped
   together (they must be: cached rows are rendered *with* the expansion state),
   so a tool you expanded stays expanded when you scroll past it and back.
   Previously page eviction silently collapsed it.
3. **Pages are applied from a worker goroutine** under the shared render mutex,
   like the existing historical-search worker. `applyPage` no-ops when the pager
   is closed; the 5 s per-fetch timeout is unchanged.

## Not done, worth doing

- `interactiveInput.enterTranscript()` still performs two *synchronous* RPCs
  before the pager opens. On a local daemon that is milliseconds, but it is the
  same class of bug as the scroll stall: the pager could open on the model it
  already has and fold the reads in when they land.
- Row heights are estimated from a running average over the row cache. A
  per-page row count kept alongside `pageDesc` would make the geometry exact
  (and would let `trimPages` enforce the budget directly instead of relying on
  the page size that produced it).
