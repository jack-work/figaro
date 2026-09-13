# Prefix retention across a subject switch

> **STATUS: measured, designed, not yet built.** Written 2026-09-12 on
> `feat/prefix-retention` (off `fix/quote-review-cli`). Section 1 is what a hop
> costs today, taken in a real pane. Section 2 is the design, and nothing in
> the client is written until it is read. Section 3 is the same measurement
> after.
>
> Gluck's brief, which is the whole specification:
>
> > *"It is clearly very very slow to transition between nodes, and we end up
> > loading pretty much identical bytes into the prefix. If we are jumping to a
> > fork point, the entire prefix is immutable [...] So when hopping between
> > forks, not only do we not have to reload any of the prefix, we don't even
> > have to adjust scroll position if the fork point is on screen."*

This is [transcript-subject.md](transcript-subject.md) §3 and
[transcript-command-mode.md](transcript-command-mode.md) phase 4, with the
measurement they were missing.

## 1. What a hop costs today

### The instrument

Two scripts, both hermetic (no credentials, no network beyond loopback, no
tokens, nothing under `~/.local/state/figaro` touched):

- `scripts/prefixcorpus.sh [turns] [forkturn] [reply_bytes]` builds the
  fixture against `scripts/fake-gateway-bulk.py`, which answers with a
  paragraph of the size you ask for rather than "Ecco fatto": an aria whose
  every turn is twelve characters has no prefix worth retaining.
- `scripts/prefixhop.sh` drives `figaro listen` in a tmux pane, types
  `:attend` four times, and reports per hop from the **tape**
  (`listen --record`, every JSON-RPC message with the time it crossed).

The fixture: parent of **40 turns** at ~3.2 KB of prose each, a **branch**
forked at `:21` with three turns of its own, and a **cousin** forked at the
same `:21` with three of its own. 1.2 MB of store. Turn 21 is the first turn
the parent does not share with either branch, and the branches share turns
1..20 with each other.

Two traps the harness had to grow past, both of which produced a confident
wrong answer first:

- **Stillness is not arrival.** At two stable polls of 100 ms the capture came
  back with the previous aria's body under the new aria's footer. The cousin
  hop repainted 2.2 s in, when its last read landed. Every number taken off
  that capture was a lie. The harness now settles, waits, and settles again.
- **A hop does not own the next hop's reads.** Closing the window at "the
  screen settled plus half a second" charged each row with the following hop,
  which read 196 KB for every switch, the sum of two. A hop owns its burst:
  the frames up to 600 ms of quiet.

### The numbers

`b04f2120`, pane 120x51, hot daemon, all four arias resident.

| hop | reads | read bytes | calls | of which shared prefix |
|---|---|---|---|---|
| startup (open the parent) | 3 | 127,905 | 5 | n/a |
| parent → branch | 2 | 68,299 | 4 | 53,585 (83%) |
| branch → parent | 2 | 124,119 | 4 | 53,585 (44%) |
| parent → cousin | 3 | 72,064 | 5 | 53,585 (76%) |
| cousin → parent, parked on the divergence | 3 | 127,905 | 5 | 53,585 (44%) |

**Every hop re-reads the whole window, and between 44% and 83% of what comes
back is bytes the client already holds, byte for byte, under the same
coordinates.** The shared figure is exact: it is the serialized size of the
parts whose turn is below 21, taken from the tape.

Wall clock on loopback with a warm daemon is small (10 to 180 ms), and it is
not the number to design against. Two others are:

- **The first visit to a dormant aria took 2.24 s** on the wire, measured
  before everything was resident: the daemon composes that aria's whole window
  from the log on the way. A hop that asks for only the suffix asks it to
  compose only the suffix.
- The page is **one read of the tail window**, not of the history: 124 KB here
  because 40 turns of prose fit inside the budget. On a longer aria the hop
  cost is bounded by the window, and the retainable fraction is then decided by
  where the divergence sits relative to that window, which is the design's
  whole subject.

### What the screen does

Parked on turn 20 (the last turn the cousin and the parent share, on screen,
byte-identical in both), `:attend` the parent:

```
MOVED: 32 rows differ
```

The window jumps to the live tail. **There is no case today in which the
screen stays put**, because `transcript.retarget` clears the window, the row
cache, every derived index, and sets `follow = true`.

### Two defects the measurement found

1. **The sticky header keeps the previous aria's question.** After
   `parent → cousin` the header read `branch turn 1` while the footer read
   `cc4e0afc` and the wire had delivered `cousin turn 1`.
   `transcript.retarget` clears `rowCache` and does not clear `stickyCache`,
   which is keyed the same way, by `(turn, node)`, and therefore collides
   across arias exactly as the row cache would. Fixed first, on its own, with
   its own canary: it is a ship-blocking bug in the pending release and it is
   not this branch's subject.
2. **The pager re-reads one message every 1.8 s forever** (3.7 KB a time) for
   the metrics on the status bar. Noted, not fixed here.

## 2. The design

### The law

> **A fork point is sealed. Everything below it can never change.**

That is not an assumption about usage, it is what the store does: `ForkAt`
seals the cut, the parent's segments are truncated at `forkBase - 1`, and a
read below the base resolves through the ancestor rather than through a copy
(`internal/store/disk/log.go`, `TurnCache.put` skipping below the base). The
fork base is snapped DOWN to a turn boundary, so the shared prefix is
**coordinate-identical**: same turn ids, same node ordinals, same bytes.
Verified on the fixture: turn 20's node hashes to `89836b3f8f9f` under the
parent and under the branch.

Everything below rests on that one sentence. If it is ever false, retention is
a lie, so it is the thing the canaries check.

### 2.1 The operation: `figaro.lineage`

A new read on the angelus door. It is not a page field.

```go
// LineageRequest asks for one aria's ancestry, and optionally where that
// ancestry parts company with another aria's.
type LineageRequest struct {
    FigaroID string `json:"figaro_id"`
    Against  string `json:"against,omitempty"`
}

// Link is one node of the chain, root first. Base is the first TURN this node
// owns; 0 is a root. Turns below Base are the previous link's, byte for byte.
type Link struct {
    Node string `json:"node"`
    Base uint64 `json:"base"`
}

type LineageResponse struct {
    Chain []Link `json:"chain"`
    // Divergence is the first turn the two arias do NOT share, and Ancestor is
    // the deepest node they do. Set only when Against was. Divergence 0 means
    // they share nothing (unrelated arias, or one is the other's ancestor at
    // the root).
    Ancestor   string `json:"ancestor,omitempty"`
    Divergence uint64 `json:"divergence,omitempty"`
    // Epoch is the topology form's revision. A client that holds a retained
    // prefix re-asks when it changes; see 2.5.
    Epoch uint64 `json:"epoch"`
}
```

**Why an RPC rather than `Ancestry` riding the first page** (option A in
transcript-subject.md §3, which I am overruling with its own argument):

- The objection to an RPC there was the *no-window property*: a client that can
  hold a page whose lineage it has not learned will eventually render one
  aria's rows under another's coordinates. That window does not exist here,
  because the call is made **before the new subject's first page is folded**,
  and the fold is fenced by the subject generation anyway. The order is:
  ask, truncate, then read.
- The objection to `retainable(from, to)` was that it puts a client cache
  policy on the server. `lineage` does not: the divergence turn is a fact about
  the tree, the same fact `figaro status` already prints as `forked-from
  <id>:21`. What the client does with it stays the client's.
- It is **cacheable**, which the page field is not. A chain is immutable for
  the life of the node. A reader hopping around a fork tree with `^O`/`^I`
  pays one call per aria ever, not one per page.
- Gluck asked for exactly this: *"The figaro server should support an operation
  that returns the least common ancestor of two nodes, and it should be called
  always."* `Against` is that operation. The chain is what makes it cheap the
  second time.

**The walk.** `XwalStore.Lineage(id)` already exists and already returns
`[]fwtree.Ref{Node, Base}` root-first, off `trunks.ListLight()`, which is in
memory. It is the walk the composed cache uses to read an inherited prefix.
Two things have to change:

1. **Base must be a TURN, not an LT.** The tree records `BranchedLT`; the
   client's coordinates are turns and the row cache is keyed by turn. The
   conversion is `turns.At(parent messages, BranchedLT)`, which is what
   `status.go` already does to print `forked-from <id>:21`. It costs one read
   of the parent's IR, so it is **memoized per node** in the angelus: a fork
   base never moves, so the memo never needs invalidating within an epoch.
   Measured on the fixture: `BranchedLT 39` maps to turn 21, and the branch's
   own first turn is 21. Where the stamped turn id on the record at the base is
   non-zero, that is the answer without any walk at all; the walk is the
   fallback for logs written before turn ids existed.
2. **The chain must be filtered to conversations.** The genesis root and the
   outfit stump are in the walk and are not readable history.

The LCA of two chains is the longest common prefix, and the divergence is the
minimum `Base` of the first link at which they part. Two cases, both real:

| shape | chains | divergence |
|---|---|---|
| parent and its branch | `[P]` and `[P, B]` | `B.Base` |
| two cousins | `[P, B]` and `[P, C]` | `min(B.Base, C.Base)` |

The cousin row is why `min` and not "the base of the one that is deeper": B
forked at 21 and C at 30 share turns 1..20 only, and a client that kept through
29 would render C's turns 21..29 as B's. The fixture covers the equal case;
the test covers the unequal one.

**Cost.** The chains come from a map already in memory, the base memo is a
hash lookup after first sight. One RPC, no log read in the steady state.

### 2.2 What the client keeps, and what it drops

The switch keeps its shape from `transcript-command-mode.md` §3 (the generation
fence stays verbatim, it is the reason any of this is safe), and stops minting a
fresh `aria.Client`.

Let `D` be the divergence turn. **Keep every turn strictly below `D`, drop at
or above.** The turn you forked at is the turn that differs.

| holder | today | with retention |
|---|---|---|
| `aria.Client` + range store | fresh, empty | kept; truncated at `D` |
| the open tail (streaming suffix) | fresh | **always dropped**: it belongs to the aria we left |
| `emitted`, `inquiry` maps | fresh | entries below `D` kept |
| `transcript.rowCache` | cleared | entries with `turn < D` kept |
| `transcript.stickyCache` | **not cleared (bug)** | entries with `turn < D` kept |
| `openMemo` | cleared | cleared: it memoizes the live turn |
| `expanded`, `selection`, `visual` | cleared | refs below `D` kept |
| window floor `from`, `offset`, `follow` | reset to tail | see 2.3 |
| `index`, `lineKey`, `frameRefs`, `prev` | cleared | cleared: they are per frame anyway |
| jump, search, completions | cleared | cleared |
| queue mirror, intrinsic forms, status | reset | reset: they are per aria |

The new primitive on the client is one method, and it is the mirror of
`EvictBefore`, which already exists:

```go
// RetainBelow drops every message at or above turn D, the open tail included,
// and keeps everything below it. The prefix a fork shares with its ancestor is
// sealed and coordinate-identical, so what is kept is not a guess about the
// new subject: it IS the new subject's history, already held.
func (c *Client) RetainBelow(turn int)
```

`Store.Evict(from, to)` already takes an arbitrary interval, so the store half
is `Evict(Anchor{Turn: D}, +inf)` plus `ResetOpen` plus `SetMore(After: true)`.

**When `D` is 0 (unrelated arias), this is exactly today's behaviour**: keep
nothing, mint fresh. One path, two ends of one range, and the unrelated case is
not a special case.

### 2.3 The two screen states

The reader's position is the reader's, and the bytes above the divergence are
the same bytes. So:

- **The window's floor is below `D`, and the pager is not following the tail.**
  Change nothing: not `from`, not `offset`, not `follow`. The rows above the
  divergence are already painted and already cached; the frame after the switch
  repaints only what is at or below `D`. This is Gluck's *"we don't even have
  to adjust scroll position if the fork point is on screen"*, and it covers the
  strictly-stronger case where the whole window sits inside the shared prefix
  and **nothing on screen changes at all**.
- **Otherwise** (the pager is following the tail, or the window sits entirely at
  or above `D` and so shows only what the two arias do not share): jump to the
  new subject's live tail and follow, which is what happens today.

`follow` is the tell, and it is already a field: following means "I am at the
live edge", and the live edge of the new subject is somewhere else by
definition. A reader who has scrolled has turned follow off.

### 2.4 The reads, and the parallelism

A switch today is: resolve the spec (angelus), dial the aria's socket, read the
window, read metrics, seed the intrinsic forms. With retention it becomes:

```
        resolve(spec) ──┬── dial ─────────────┐
                        └── lineage(new, old) ─┴── truncate ── read the SUFFIX
```

- `lineage` goes to the angelus, which is already being asked to resolve the
  spec, and it does not depend on the dial. It is issued **concurrently with
  the dial**, so on the happy path it costs nothing: the dial is the longer of
  the two.
- The suffix read cannot start before the truncation, which cannot start before
  the lineage answer, so that edge is real. It is one read either way; what
  changes is its size.
- The old connection is closed as it is today, after the new one is up, and
  every handler stays fenced by `subjectGen`. **The drain of the old connection
  is where a retained store could still be corrupted**, and the fence is why it
  is not: a frame from the old aria arriving during the switch is dropped on
  the generation, not folded into a store that now holds two arias' coordinates.
  That check moves from "is this my connection" to load-bearing; it gets a test
  of its own rather than a comment.

**The suffix read needs a floor on the wire.** The pager's read is backward
from the tail with a byte budget, and in the fixture that single read covers
all 40 turns, prefix included. So `rpc.ReadRequest` grows one field:

```go
// Floor stops a backward read: no part below this anchor, and More.Before is
// set instead. It is how a client that already holds the prefix asks for only
// what it does not have.
Floor aria.Anchor `json:"floor,omitempty"`
```

honoured in `PaginateBefore` (stop the walk, set `More.Before`), so both doors
and both sources (live agent and store) answer the same way. Without it the
saving is the paint and the fold but not the bytes, and Gluck asked for the
bytes.

### 2.5 When the past changes, and when it is deleted

Two different questions with two different answers.

**Can the retained bytes go stale?** No, by the law: below a sealed fork point
nothing is writable. The operations that look like they rewrite the past do
not:

- `promote` moves the presentation edge only. History does not move, ids do not
  change, and the same turns read the same bytes.
- `normalize` and the delete-repair copy an inherited prefix into the aria that
  borrowed it. Same turn ids, same bytes: the retained prefix stays true.
- `kill <ancestor> -r` removes the descendants too, so there is nobody left
  holding a retained prefix of it.

**The invalidation handle** is therefore not for the bytes but for the
*lineage*: `Epoch`, the topology form's revision, rides every lineage answer.
A client that holds a retained prefix compares the epoch it was given with the
one on the next answer; if they differ, it re-asks rather than reusing a cached
chain. That is the whole handle, and it is cheap because it is a number.

**When the LCA is deleted while the pager is open**, it blows up, and that is
expected. What must not happen is a repaint of a conversation nobody asked for.
Driven in a pane, on the branch, with the ancestor's whole subtree killed under
it: the screen did not move a cell, the history stayed, nothing crashed, and
the pager left cleanly on `q`. The store makes the softer half of this true by
itself, because you cannot delete only an ancestor: a kill takes the subtree,
or a survivor absorbs the prefix first, byte for byte, and the retained copy
stays correct. So:

- The next read fails. The pager already has a gap sentinel and a status note
  for a failed page; it says so there.
- **The history in memory stays.** Nothing evicts on an error. The rows are
  cached, the store holds them, and the screen does not move.
- The aria we are on is gone, so the switch that was in flight fails and the
  subject does not change. That is the existing `retarget` error path, which
  returns before touching the renderer.

### 2.6 Hot trees: parking the subject you just left

Retention as described above makes ONE hop cheap: the one whose target shares a
prefix with what is on screen. Gluck wants the tree hot:

> *"Consider keeping recently visited trees in memory for a while, keyed by
> jumplist locality, so hopping between hot transcripts via attendance is
> instant; measure the memory cost and bound it."*

The jumplist already exists (`in.jumps`, written by every attend, fork and
`^O`/`^I`), and it is exactly the locality signal: the arias a reader is moving
between are the arias the jumplist holds.

**The shape.** A `subjectCache` on `interactiveInput`, keyed by aria id,
holding what a switch would otherwise throw away: the `aria.Client` (store,
inquiries, emitted counts) and the reader's posture in that aria (window floor,
offset, follow, and the row and sticky caches). A switch PARKS the outgoing
subject there instead of dropping it, and ADOPTS the incoming one if it is
parked.

- **Parked and still true**: adopt the store and the posture, issue one read
  for what arrived since (the tail), and paint. Where nothing has arrived, the
  hop costs zero reads and the reader lands where they left off, which is
  better than the live tail for a reader browsing a tree.
- **Parked, and it is also a fork relative**: the two mechanisms compose, and
  the screen wins. If the window on screen stands in the prefix the two arias
  share, it STAYS, and the position remembered from the last visit is
  discarded: the rows are the same rows, and honouring the memory would move a
  screen that did not need to move. This was found by measuring rather than by
  thinking: the first cut restored the remembered position, and the parked hop
  went from byte-identical to 28 rows moved.
- **Not parked**: §2.2, unchanged.

**Why the memory is not what it looks like.** A `Message` holds `Nodes
[]livedoc.Node`, and a node's payload is strings. Parking N arias of one tree
does not copy the prose N times: the slice headers are copied, the strings are
shared, and the strings are the bytes. The cost of a parked subject is its
bookkeeping (ranges, messages, cached rows), not its content, and the cached
ROWS are the part that is genuinely per aria. That is a claim, so it is
measured rather than asserted: a heap profile with 1, 2 and 4 parked subjects
of the fixture tree, reported in §3.

**The bound**, as built: **three arias**, least recently visited evicted, and
**only the newest keeps its drawn rows**. The measurement (§3) is why the
second half exists: the rows of a 40-turn aria cost five times the
conversation in them, and they are the one thing on the shelf that can be
rebuilt from what is beside it. The aria a reader is most likely to return to
is the one they just left, and it is the one that keeps them.

A parked store needs no bound of its own: the pager's retention had already
trimmed it while it was on screen.

A parked subject is dropped outright when its aria is killed, when the lineage
epoch changes, and when the pager leaves.

**What this is not, yet.** One shared prefix per TREE (every aria of a fork
tree reading one copy of the turns below their common base) is the fully
factored answer and it is a bigger change: it needs the wire to say which node
owns each turn, so that a shared store can be masked per chain. If the heap
measurement in §3 says the parked copies are expensive, that is the next step,
and §2.1's chain is already the mask it would need. Do not build it before the
number says so.

### 2.7 The canaries

Each names the thing it would catch, and each must have been seen to fail.

| canary | asserts |
|---|---|
| `fork-follow reads no prefix` | after `parent → branch`, no read's page carries a part below `D` |
| `parked window is byte-identical` | pane rows above the divergence are the same bytes before and after the hop |
| `unparked window follows the tail` | with `follow` on, the hop lands at the live tail |
| `cousin keeps exactly the common prefix` | `B(21)` and `C(30)`: the store holds turns `< 21` and nothing between 21 and 29 |
| `unrelated aria keeps nothing` | `D == 0` leaves an empty store, as today |
| `the sealed prefix is byte-identical` | the composed nodes of turn `D-1` hash the same under both arias (the law itself) |
| `an old frame does not land in a retained store` | a frame from the previous generation is dropped, not folded |
| `sticky header follows the subject` | the defect in §1 |

The first four are `scripts/prefixhop.sh` in CI shape; the rest are unit tests
in `internal/cli` and `internal/livelog/aria`.

## 2.8 How this gets built

- **Branch**: `feat/prefix-retention`, cut from `b04f2120`, which is now the
  head of `deltas-and-forks` (renamed from `fix/quote-review-cli`). It merges
  INTO `deltas-and-forks`, never into main.
- **Rebase onto `feat/transcript-metrics`** when the metrics fork lands, and
  emit its metric lines from every path added here: the hop is split by phase
  (resolve, dial, lineage, truncate, suffix read, first paint) and that split
  is precisely what this work claims to change.
- **Parallel**: the server half (§2.1) and the client half (§2.2, §2.3) share
  only the response type. They go to two arias on two worktrees as soon as the
  type is committed, and the wire test is written against the type rather than
  against either implementation. The measurement harness is already independent
  of both.
- **Live testing** runs in the nix dev shell, on the isolated store the corpus
  script builds, with a private tmux server (`tmux -L`), and every daemon it
  starts is stopped.

## 3. What a hop costs after

Measured on the honest fixture (the gateway that reads chunked bodies, the
corpus that asserts every turn has a reply), **three runs per arm against one
corpus**, both binaries named by absolute path so the arms cannot be the same
build. Before is `25a53567`, the head this work was cut from; after is
`9038c93f`.

### Bytes on the wire

| hop | before | after | saved |
|---|---|---|---|
| startup (open the parent) | 136,871 | 129,835 | the metrics probe, not the prefix |
| parent to branch | 84,720 | **11,190** | 87% |
| branch to parent | 133,085 | **4,021** | 97% |
| parent to cousin | 88,485 | **11,457** | 87% |
| cousin to parent, parked on the divergence | 136,871 | **536** | 99.6% |
| ten seconds idle | 18,933 | **1,342** | 93% |

Identical across all three runs of each arm, to the byte.

### Time to the first frame carrying content

Measured from the START of the hop (a mark span is stamped when it CLOSES, and
measuring from there hid the paint that happens inside it: the first cut of
this table was wrong for that reason). Medians of three runs, milliseconds.

| hop | before | retention only | retention + the paint fixes |
|---|---|---|---|
| parent to branch | 13.6 | 12.4 | **5.9** |
| branch to parent | 12.1 | 12.4 | **4.4** |
| parent to cousin | 12.2 | 11.0 | **1.6** |
| cousin to parent, parked | 12.8 | 13.5 | **5.5** |

**The middle column is the bench's finding, reproduced.** Retention cut the
bytes by 5 to 250 times and did nothing for the time to paint; on the shelf
path it was slightly worse. Two causes, both of them about the frame and not
about the read:

1. **The first frame waited for a read it did not need.** A hop onto a relative
   or back onto a parked aria arrives holding the window; it painted only when
   the seed read landed. It now paints what it kept first and reads afterwards.
2. **The frame-rate ceiling caught the switch.** The pager paints at most every
   8.3 ms, which is right for a stream and wrong for an answer: once the switch
   itself cost 0.3 ms instead of 7, its frame began landing INSIDE the previous
   frame's window and waited the ceiling out. The measured 13.5 ms was 5 ms of
   work and 8.3 ms of waiting to be allowed to paint. A switch's first frame is
   now exempt, and a batch (mid-keystroke) still holds it.

The hop's own span got LONGER in the last column (6.5 ms to 10.8 ms for a hop
to the branch) because the paint now happens inside it. That is the trade: the
work the reader waits for moved from after the frame to before it.

### The shelf

`TestPark_TheShelfCostsWhatItHolds`: three parked arias of the fixture's size,
rows drawn, **1.28 MB total, 0.43 MB each**, down from 2.20 MB before the rows
were dropped from all but the newest. Dropping the shelf returns the heap,
which the same test asserts. A hop onto a warm aria is 2.5 us and 7 allocations
(`BenchmarkPark_AdoptIsNotARead`).

### The screen

```
KEPT: every shared row is byte-identical across the hop
  the divergence row, which must change: > input
```

The comparison is over the shared content only: it cuts at the rule that opens
the divergent turn's block, because that turn's voice header carries the fork
glyph and belongs to the turn that differs. On the before arm the same check
reports 34 shared rows and all 34 moved.

## 4. What was found on the way

Three defects, all of them in the switch path, none of them this branch's
subject. The first two are landed as their own commits so they can be taken
ahead of the rest.

1. **The sticky header kept the previous aria's question** (`889c77d6`).
   `retarget` cleared the row cache and not the sticky cache, both keyed by
   `(turn, node)`. Seen in a pane: the header read `branch turn 1` under a
   footer reading `cc4e0afc`.
2. **The status bar paid for a message it threw away** (`543ff397`), 3.7 KB
   every 1.8 seconds for the life of a session, because a page budget is BYTES
   and "read one message backward" spends it on the whole last message.
   Metrics ride any page, so the probe is now a forward read from past the
   tail, which can return none: 269 bytes.
3. **The seam read itself back.** Found by rerunning the measurement after the
   rebase, which is the only reason it was found at all: on some hops the
   saving came back as a history read of the prefix, 120 KB of it. The store
   could not tell that the retained turn 20 and the arriving turn 21 touch,
   because a turn's extent is only recorded when the turn arrives whole and
   the cut had thrown that record away. A cut PROVES the turn below it is
   complete (the range it cut held every anchor past it), so the truncation
   now says so. Without it the pager draws a gap sentinel over history it is
   holding and fills it from the wire.
4. **A hole was filled from the aria we had left.** The pager fills the gap
   under its window on a worker; a switch can land while that read is in
   flight, and the page it brings back carries the old aria's coordinates. The
   notify pump was fenced by the subject generation and this path was not.
   It matters more with retention, because the store it would land in is no
   longer empty, so the fabricated rows would sit in the middle of real ones.
   Fenced at both ends, with the test that fails without it.
