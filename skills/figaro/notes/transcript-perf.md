# Transcript pager performance: the five-axis campaign, measured end to end

Baseline is `1e643a1` (`test(transcript): heavy-aria scroll benchmark rig`): the
commit that introduced the shared rig, before any of the five axes. Every number
here is from that rig unless stated: `-run XXX -bench 'Heavy|Burst' -benchtime
30x -benchmem -count=8`, AMD Ryzen 7 5800X, go1.26.4, summarized with benchstat.

Three measurement points:

| point | what is in it |
|---|---|
| **base** | `1e643a1`: nothing |
| **pre** | `4953844`: C (allocations) + A (virtualization) + D (paging) |
| **now** | `2298a1b`: the above + B (input coalescing, painter) + E (SGR collapse) |

A fourth, `e6c459a` (B but not E), is used below only to attribute regressions.

---

## The headline: a scroll frame

| metric | base | now | change |
|---|---|---|---|
| `ScrollHeavy/out20` | 4101 µs | 16.4 µs | **250× faster** |
| `ScrollHeavy/out200` | 3899 µs | 15.3 µs | **255× faster** |
| allocated / frame | 3.93 MB | 1.13 KB | **3470× less** |
| allocations / frame | 9513 | 17 | **559× fewer** |
| **bytes written to the terminal / frame** | 12579 B | **70 B** | **180× less** |

The last row is B's, and it is the one you feel over ssh: a one-line scroll used
to retransmit the whole viewport and now moves it with a scroll region and
repaints the two rows that actually changed.

Entering the pager on a heavy aria (`HeavyEnter`): **33.7 ms → 3.13 ms**, and
13563 B → 3727 B of paint.

A 24-event mouse-wheel flick: **24 frames → 1 frame** (`BurstFrames`,
17.4 µs/flick, 1.04 KiB, 15 allocs: the whole flick, not per event).

---

## Full rig, base → pre → now

sec/op (± is benchstat's spread over n=8):

| benchmark | base | pre | now | now vs base |
|---|---|---|---|---|
| `ScrollHeavy/out20` | 4101.15 µs ± 2% | 11.59 µs ± 1% | 16.42 µs ± 7% | −99.60% |
| `ScrollHeavy/out200` | 3899.02 µs ± 2% | 11.53 µs ± 8% | 15.33 µs ± 25% | −99.61% |
| `ScrollBurst` | 94105 µs ± 0% | 252.0 µs ± 9% | 367.3 µs ± 5% | −99.61% |
| `RenderHeavy/out20` | 4035.70 µs ± 1% | 11.04 µs ± 6% | 11.49 µs ± 13% | −99.72% |
| `RenderHeavy/out200` | 3932.76 µs ± 3% | 10.98 µs ± 11% | 11.24 µs ± 5% | −99.71% |
| `LinesHeavy` | 3865.51 µs ± 2% | 6.801 µs ± 12% | 7.159 µs ± 19% | −99.81% |
| `ScrollHeavySearch` | 4382.55 µs ± 3% | 23.75 µs ± 17% | 21.32 µs ± 20% | −99.51% |
| `HeavyEnter` | 33.745 ms ± 2% | 1.739 ms ± 6% | 3.128 ms ± 3% | −90.73% |
| `FollowHeavy` |: | 11.65 µs ± 1% | 12.11 µs ± 12% |: |
| `LiveHeavy` |: | 13.81 µs ± 20% | 22.75 µs ± 17% |: |
| `FindHeavy` |: | 6.587 µs ± 13% | 3.148 µs ± 13% |: |
| `FindRepeatHeavy` |: | 21.40 µs ± 7% | 10.19 µs ± 14% |: |
| `BurstFrames` |: |: | 17.38 µs ± 8% |: |

(`FollowHeavy`, `LiveHeavy`, `FindHeavy`, `FindRepeatHeavy` were added by later
axes and have no base column. `BurstFrames` is B's.)

B/op and allocs/op:

| benchmark | base B/op | now B/op | base allocs | now allocs |
|---|---|---|---|---|
| `ScrollHeavy/out20` | 3934.9 KiB | 1.133 KiB | 9513 | 17 |
| `ScrollBurst` | 94285 KiB | 27.21 KiB | 228166 | 408 |
| `LinesHeavy` | 3868.9 KiB | 1.166 KiB | 9427 | **0** |
| `ScrollHeavySearch` | 4413.5 KiB | 2.289 KiB | 10621 | 28 |
| `HeavyEnter` | 14787 KiB | 748.5 KiB | 279798 | 2098 |

---

## Paint bandwidth (B's benchmark)

`-bench 'FrameBytes|EnterBytes|PaintBytes' -benchtime 200x`. The base column is
E's standalone bytes benchmark replayed on the baseline tree.

| metric | base | now (scroll regions on) | now (`FIGARO_NO_SCROLL_REGION=1`) |
|---|---|---|---|
| `FrameBytes/out20` B/frame | 12579 | **70** | 3127 |
| `FrameBytes/out200` B/frame | 12585 | **70** | 3133 |
| `PaintBytes/up` B/step |: | 155.1 | 2993 |
| `PaintBytes/down` B/step |: | 155.1 | 2989 |
| `EnterBytes` B/enter | 13563 | 3727 | 3727 |
| writes/frame | 1 | 1 | 1 |

Two independent wins stacked here. The fallback path (regions off) is 12579 →
3127 B: that is E's collapse plus B's SGR compaction and suffix updates. Turning
the scroll region on takes it to 70 B, because the rows are moved rather than
sent.

## History journey (D's benchmark)

`-bench TranscriptJourney/out20 -benchtime 3x`, three interleaved runs per point
to control for machine drift:

| point | ns/op | B/op | allocs/op | peak retained |
|---|---|---|---|---|
| pre | 30.2 ms | 4.20 MB | 19890 | 2438 KB |
| B only | 41.0 ms | 4.21 MB | 19893 | 2438 KB |
| now | 40.2 ms | 6.73 MB | 26972 | **816 KB** |

Fetches (8), refetched messages (0), evictions (204) and node renders (612) are
byte-identical across all three: neither B nor E perturbs D's paging policy.

---

## Regressions, all of them

Everything below is measured against **pre** (A+C+D), which is the only fair
comparison for "what did B and E cost".

### 1. A scroll frame costs ~5 µs more CPU: B's painter

`ScrollHeavy/out20` 11.59 → 17.08 µs at B-only, 16.42 µs after E. **+42%.**

Attributed by measurement, not assumption: B-only already shows the whole
regression, and E claws a little back. It is *not* `compactRow`: stubbing
compaction out leaves the number where it is (16.95 µs). It is the scroll-region
planner: `planScroll` fingerprints both frames and searches ±32 shifts, then
predicts and prices the plan.

Note what does **not** regress: `RenderHeavy` (11.04 → 11.49 µs, p=0.57) and
`LinesHeavy` (6.80 → 7.16 µs, p=0.20). Those recompose the same frame, so the
diff finds nothing and the painter does no work. The cost is specific to frames
where the viewport actually moves.

Is the trade good? Switching the scroll region off makes it *slower still*
(19.5 µs) because every row goes out in full: so the planner pays for itself
even on a local terminal in wall time, once the write is counted. And 16 µs is
0.1% of a 60 Hz frame budget. Against 12579 → 70 bytes on the wire, this is the
correct trade and it is not close.

### 2. `ScrollBurst` 252 → 367 µs (+46%): B's painter, and the rig is stale

`ScrollBurst` in the shared rig drives 24 `scrollBy` calls directly, so it paints
24 frames and pays #1 twenty-four times. That is no longer what the pager does:
the input loop brackets a read in a batch and paints once. `BurstFrames` is the
same flick through the real path: 1.000 frames/op, 17.38 µs, 1.04 KiB, 15
allocs. The rig file is off limits by instruction, so the stale number stays;
read `BurstFrames` instead.

### 3. `HeavyEnter` 1.739 → 3.128 ms (+80%): E's collapse

B-only is 1.719 ms, so this is entirely E. It is the collapse being paid on a
cold row-cache fill: +1.4 ms, +267 KB, +882 allocs. Bought with 4.6× smaller
retained rows (journey peak retained 2438 → 816 KB) and 12579 → 3127 B/frame
before the scroll region even engages.

This is exactly the cost E's follow-up would remove: see the note at the
`collapseSGR` call site in `transcript.go` for why it is deferred and what it
would take.

### 4. Journey CPU 30.2 → 40.2 ms (+33%): B's painter

B-only is 41.0 ms, so again the painter, and again it is #1 multiplied by a long
scroll. E is neutral-to-better here (41.0 → 40.2 ms) despite adding 2.5 MB of
transient allocation, because everything downstream handles smaller rows.

### 5. `LiveHeavy` 13.81 → 22.75 µs (+65%): split, B then E

B-only 17.00 µs (+23%), then E to 22.75 µs. The live tail repaints on every
delta, so it pays the painter per frame; E's share is the cache fill for the
messages the benchmark commits as it goes. Mitigated in production by something
this benchmark cannot see: B's 120 fps pacer means a streaming tool pushing
dozens of deltas a second now paints at most 120 frames a second, not one per
delta.

### 6. `FollowHeavy` 11.65 → 12.11 µs (+4%)

p=0.065 and p=0.645 against pre: not significant. Listed for completeness.

---

## Improvements not in the headline

- `FindHeavy` 6.587 → 3.148 µs (**−52%**), `FindRepeatHeavy` 21.40 → 10.19 µs
  (**−52%**), both with 26% less allocation: E's collapse leaves less ANSI for
  the search scan to skip over.
- `ScrollHeavySearch` 23.75 → 21.32 µs (−10%, p=0.38: directionally E's, not
  statistically established at n=8).
- Journey peak retained memory 2438 → 816 KB (**3.0×**).
