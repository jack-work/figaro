# The range store

Status: **design, approved to build.** This file is the interface contract.
Implementers code against this document, not against a conversation.

## Why

The client's model of a conversation is a list plus a pile of booleans:

- `aria.Client.closed []Message` — one implicit contiguous run
- `transcript.pages []transcriptPage` — a **second copy** of the same messages
- `checkOlder` / `checkNewer` / `noMoreOlder` — one bit per edge, standing in
  for "is there more, and where"
- `heldOpen *aria.Message` — a frozen snapshot, needed only because the two
  copies disagree about what is closed

Four consequences, all of which we have hit:

1. **The detached tail freezes.** `openMessage()` returns `heldOpen` while
   `!follow`, because `client.Open()` is the open SUFFIX and nodes leaving it
   become closed messages the pager's frozen window does not hold. Rendering
   live would make content vanish, so it renders stale instead.
2. **`gg` means "top of the buffer"**, because a second range cannot be
   represented. There is nowhere to put "the head of the aria" while also
   holding the tail.
3. **Eviction is bottom-only** (`trimClosed` + `closedFloor`). A long aria
   cannot forget its middle, because forgetting the middle would make a hole
   and there is no hole.
4. **A pending prompt is invisible.** Between submit and the drain's
   classification there is no state to render, so a queued steer shows nothing
   for as long as the current tool round takes.

All four are the same missing idea.

## The idea

> The client's knowledge is a **set of contiguous intervals** over `(turn,
> node)` space, not a list.

Everything else follows. Gaps are what lies between intervals. Eviction and
never-fetched become the same state. The open turn keeps its own machinery,
unchanged.

## Coordinates

`aria.Anchor{Turn, Node}` already exists and is the address: turn ids ascend,
node ids are positional within a turn (`Nodes[i].ID == From+i`), nothing is
ever renumbered. LT is metadata and MUST NOT appear here — a tool node spans
two LTs and one LT can carry both a tool result and a steer.

Ordering is lexicographic on `(Turn, Node)`. Implement `Anchor.Less`,
`Anchor.Next`, `Anchor.Prev` once; nothing else may open-code the comparison.

## Types

```go
// Range is a contiguous, fully-materialized run. From/To are inclusive and
// describe the SLICE COVERAGE, not just the first and last message: a range
// asserts that it holds every node between them.
type Range struct {
    From, To Anchor
    Msgs     []Message // contiguous; identity is (Turn, From) per Message's doc
}

// Store is the client's whole model of one aria.
type Store struct {
    ranges  []Range   // sorted by From; non-overlapping; NEVER adjacent
    more    More      // is there anything beyond the outermost edges
    open    *openTurn // the ONE streaming suffix — unchanged from today
    pending []Pending // submitted, not yet classified by the drain
}
```

### Invariants

1. `ranges` is sorted by `From` and strictly non-overlapping.
2. **No two ranges are adjacent.** If `a.To.Next() == b.From` they coalesce
   immediately. Therefore a gap always contains at least one real node, and
   "is there a gap here" is a pure function of the ranges.
3. Every `Range.Msgs` is contiguous with no holes. A caller handed a `Range`
   may assume adjacency; that assumption is the whole point of the type.
4. `open` is disjoint from every range: nodes below `Live.From` are released
   INTO the head range, not held in both.

Invariant 3 is the one that matters. The bug class this design exists to
prevent is **fabricated adjacency** — returning a flat `[]Message` spanning a
hole, so the caller believes two messages are neighbours when a hundred turns
sit between them. `Message`'s own doc records three separate bugs from
mistaking `Turn` for an identity; this is the same disease at range scale.

## The two verbs

```go
// Query reports what the store HOLDS over [from,to]. It never fetches and
// never blocks.
func (s *Store) Query(from, to Anchor) []Segment

// Segment is a contiguous run, optionally followed by a hole.
type Segment struct {
    Msgs []Message // contiguous, may be empty
    Gap  *Gap      // non-nil => a hole follows this segment
}

// Ensure fills every hole in [from,to], fetching as needed, then Query over
// the same interval returns exactly one Segment with a nil Gap.
func (s *Store) Ensure(ctx context.Context, from, to Anchor) error
```

A caller that does not care about gaps writes:

```go
for _, seg := range store.Query(a, b) { use(seg.Msgs) }
```

…ignores `.Gap`, and is **never lied to** — it simply gets less. A caller that
needs completeness calls `Ensure` first. Gap-blind by default, gap-aware by
choice. There is no third mode and no flag.

`Ensure` MUST be cancellable and MUST report progress for the unbounded case
(see "jump to the beginning" below).

## Operations

| operation | effect |
|---|---|
| live append | extends the head range's `To`, or folds into `open`. **Never** makes a gap. |
| `ReadBefore(anchor)` | yields an interval; merge it |
| jump to X | read around X; if it does not touch an existing range, a gap appears |
| merge | overlapping or adjacent ranges coalesce (invariant 2) |
| evict | drop or trim a range; **a gap appears** |

Eviction and never-fetched are the same state. That is not a coincidence to be
tolerated — it is the point. Retention stops being a special case and becomes
"keep the ranges nearest the viewport."

## Gaps and rendering

A gap renders as **exactly one row**.

Not a proportional placeholder. Paging-library placeholders work because item
height is known; here a row count is only knowable by rendering, and a gap of
twelve turns might be forty rows or four thousand. One sentinel row is the
honest representation:

```
   │ …the last thing in range 1
   │
   ├──────── 13 turns not loaded ────────      <- one row, selectable
   │
   │ the first thing in range 2…
```

Consequences to accept deliberately:

- The footer's `190–226/250` becomes **rows we hold**, not rows that exist.
  Mark it (`≈`) or drop the total. Do not print a number that is false.
- A gap row is a legitimate selection and navigation target.
- Binding a gap row is the fetch trigger; with a prefetch distance the
  sentinel usually never paints.

### When a gap can actually appear

- mid-history eviction (impossible today; trim is bottom-only)
- a jump to an arbitrary anchor (`fig show <id>:<turn>`, a permalink)
- a search landing far from the viewport
- `ClippedHead` — **already on the wire**; today the client papers over it by
  flooring `emitted`
- reconnect where catch-up cannot span the missing interval in budget

Explicitly NOT: normal streaming, normal scroll-up, tail following. Those
extend contiguously and coalesce.

If nobody jumps and nothing is evicted, there is exactly one range forever and
no gap ever renders. **The model degenerates to today's behaviour**, which is
the migration story: correct before it is useful.

## Pending

```go
// Pending is a prompt that has been submitted but not yet classified by the
// drain. It has NO server coordinate, because only the drain can assign one —
// it alone knows whether a turn was in flight when the prompt came off the
// queue.
type Pending struct {
    Text string
    At   time.Time
}
```

Pinned after the head range, rendered in its own style. On acknowledgement it
resolves exactly one of two ways:

- **inquiry** — acquires a turn id, merges into the head range
- **steer** — becomes a node inside `open`

Both are "acquire a coordinate and move". Neither is a special case of the
other, and the client MUST NOT guess which will happen: a prompt sent while a
turn runs becomes a steer, and that classification happens server-side, at the
drain, after our submit returns.

## What incipit requires of this

Stated by the user, and binding:

- a message to an **idle** figaro must feel like **call–response**: the
  question, then the reply. No preamble, nothing above it.
- a message to a **running** figaro gets **a little immediate context**, and
  that fetch must **seed the store**, so the pager does not re-page to render.

The discriminator is the EVENT, not a state flag: if the arriving turn carries
our own inquiry, our prompt opened the turn (call–response). If it does not, we
are joining a turn already in flight (orientation). State can race; the event
cannot. This is the same inquiry-vs-steer split the drain makes, observed from
the stream.

Seeding is not a second fold. In a range store, seeding is `merge(range)` —
there is no event stream to double-emit, which is what made the old
`OnClosed`-fires-and-`Freeze`-prints trap possible.

## Migration

Land it **behind the existing API**. Phase 1 ships `Store` with `View()`,
`Open()`, `OnClosed`/`OnLive` preserved by a shim, and no renderer changes.
That constraint is what makes the change reviewable and revertable. Consumers
move afterwards, one at a time.

Order, and why:

1. **Store + tests**, behind the shim. Nothing else moves.
2. **Transcript reads from the store.** Delete `pages`, `heldOpen`, `tailRev`,
   `committedW`, `checkOlder/checkNewer/noMoreOlder`. Bug B (the frozen
   detached tail) should DISSOLVE here, not be fixed: with one owner, released
   nodes land in the head range, which IS the window, so there is nothing to
   freeze. If it does not dissolve, the store is wrong — stop and say so.
3. **Gap rendering + Ensure-on-bind**, prefetch distance.
4. **Pending**, and the submitted→committed→acked lifecycle.

## Open questions — decide before coding the affected phase

- **What should `gg` mean?** "Top of what I hold" (today) or "the beginning of
  the aria"? The second needs `Ensure` to be interruptible and
  progress-reporting, and makes the gap row a landing target rather than only a
  trigger. This is a product decision, not a technical one.
- **Coalescing across a streaming turn.** A range whose `To` sits inside the
  open turn has a moving boundary. `Live.From` is the natural fence; the merge
  needs care and its own tests.
- **Selection across a gap.** Does `^N` at a gap edge jump, or load-then-move?

## Testing standard

Non-negotiable, and inherited from the scars in `@skills.tmux-testing`:

- Property tests on the range algebra: merge is associative and commutative
  over disjoint inputs; no operation may produce overlapping or adjacent
  ranges; `Query` never returns a `Segment` whose `Msgs` span a hole.
- Every fix carries a **canary**: revert it and quote the failure. An assertion
  that has never failed is not evidence.
- Anything that paints gets a real pty, control AND experiment, with the
  binary named by absolute path and its md5 quoted (`tmux new-session -e
  PATH=…` is silently ignored; it has produced a confident wrong answer here
  before).
- Never test against the live daemon.
