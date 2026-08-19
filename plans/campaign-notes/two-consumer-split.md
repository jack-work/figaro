# The two-consumer split: killing the quadratic by not creating it

For Gluck's review. Written 2026-08-18 by aria 091d162e (role @980dc16c),
from Gluck's own observation: "the ui ir should be translating the message
deltas into its own string, not the quadratically expanding message. I
should think they ought to be separate consumers."

______________________________________________________________________

## THE DEFECT, MEASURED

`internal/figaro/turn.go:1601`, `asm.addText`, on the streaming path of
every turn:

```
if n > 0 && s.msg.Content[n-1].Type == kind {
    s.msg.Content[n-1].Text += text
    return
}
```

Consecutive same-kind deltas coalesce into one block. Go strings are
immutable, so `+=` allocates a new string of the FULL accumulated length
and copies it, per delta. Sum over N deltas is w\*N(N+1)/2.

Confirmed by closed-form model, no fitted parameter (baseline from the
first point; the other two are pure prediction):

```
deltas   measured B/op    model 170,912 + 64*N(N+1)/2    error
    16         179,616                      179,616      baseline
    64         318,816                      304,032      +4.9%
   256       2,447,121                    2,276,256      +7.5%

marginal: 2,900 B/delta (16->64) then 11,084 B/delta (64->256)
allocations 600 -> 635 -> 821: LINEAR allocs, QUADRATIC bytes
```

In production: a 10 KB reply streamed over 1,000 deltas allocates and
copies ~5 MB of intermediate strings to produce 10 KB. Tool output is
clamped by tailBound. PROSE IS NOT.

It survived the entire memory campaign because the campaign measured the
COMPOSE path and nothing priced the ROUND LOOP.

## THE REAL FINDING: THE INFORMATION IS DESTROYED, THEN RECONSTRUCTED

The wire already speaks deltas. `Server.delta()` calls
`streamed("markdown", old.Markdown, n.Markdown)`, which calls
`livedoc.Diff(ov, nv)` and sends a SPLICE. So the round trip is:

1. the provider hands us a DELTA
1. asm.addText concatenates it into the full string -- O(N) copy
1. compose.Nodes copies that string into livedoc.Node.Markdown
1. Server.delta() DIFFS it against the previous full string to
   recover the splice
1. the wire sends the delta

WE HAD THE DELTA AT STEP 1. Steps 2-4 exist to reconstruct it. The
quadratic is not a wasteful accumulator; it is the price of throwing
information away and paying to infer it back.

This also reframes the campaign's own result: incremental composition
memoised a derivation that should not have been a derivation. It made
re-deriving the region 26x cheaper. This says the streaming field should
not be re-derived at all.

## THE FIX: TWO CONSUMERS OF ONE DELTA STREAM

```
consumer      needs                 when
-----------   -------------------   --------------------------------
the MESSAGE   the complete text     once, at round end: for the
                                    provider and the durable IR
the UI IR     the delta             per event: appended to the node's
                                    own text; the wire wants a splice
```

They have different lifetimes and different truths. The message is a
RECORD; the node is a PROJECTION with a live tail. Today the node is
derived from the record every frame, which is why the record must be
complete every frame, which is why the concatenation happens per delta
at all.

Split them and three things fall out at once:

- the quadratic disappears (the message is assembled once, at round end)
- livedoc.Diff becomes unnecessary for the streaming field
- the aliasing hazard never arises: the node owns its buffer and
  nobody re-derives it

### WHY NOT THE OBVIOUS FIX

The first proposal was "accumulate in a strings.Builder". Rejected, and
the reason is worth keeping:

- Builder.String() is unsafe.String over the live buffer (verified in
  /usr/lib/go/src/strings/builder.go). A published node's Text would
  ALIAS a buffer the drain loop is still appending to; when grow()
  finds spare capacity it does not reallocate, so it writes into the
  array a subscriber is reading. That breaks cached_log.go's
  published-window law one package over, INTERMITTENTLY -- only when
  capacity happens to suffice.
- It also keeps the derivation. The quadratic would be gone; the
  reconstruct-the-delta round trip would remain.

TODAY'S QUADRATIC IS BUYING CORRECTNESS: `+=` yields a new immutable
string every delta, so the published node holds something nobody mutates.
Any fix must replace that guarantee, not merely remove the cost.

## WHERE IT LANDS

NOT a patch on feat/layered-cache. It becomes its own stage in the
state-door sequence, because it rewrites driveOneRound -- the same path
stages 2, 3 and 4 rewrite, and the path 3a9225b1 has just built a
b.N-independent instrument for.

- this should just be a patch on feat/layered-cache. I know it perverts the meaning of the branch name, but this is becoming an all encompassing feature branch. Everything should go on it. I want an all encompassing feature branch that I merge all at once. This is the place for it.

Proposed sequence position: AFTER stage 2 (one door) and BEFORE stage 3
(the door records decisions). Stage 2 settles who writes; this settles
what the writers hand the projection; stage 3 then stamps decisions at a
door whose consumers are already separated.

## WHAT IT NEEDS BEFORE CODE, in this campaign's own discipline

1. HAZARD TEST FIRST: the node's incrementally-built text must equal the
   wholesale composition AT EVERY FRAME, not merely at seal -- the same
   oracle shape that guarded incremental composition, and for the same
   reason: a projection that converges only at seal renders correct
   transcripts over wrong live frames.
   - when the message is sealed, the UI IR can recompute the IR message and replace it if it differs with the incrementally generated step before the turn is closed.
1. THE ALIASING LAW, asked as a CAPACITY question, because no comparison
   of contents can see it. The node now owns a growing buffer; whatever
   it hands out must be immune to later appends. Precedent:
   region_memo_test.go asserts cap == len for exactly this reason.
1. REACHABILITY PROOF before any before/after: break the changed line,
   show the round-loop benchmark moves. Standing rule after six
   instances of instruments not reaching the code.
1. PRE-REGISTER THE MECHANISM, NOT THE MAGNITUDE. Four predictions in
   this campaign were spent learning that reading source tells you what
   is WRITTEN and escape analysis tells you what is ALLOCATED. Run
   `go build -gcflags=-m` before predicting allocations.

## PREDICTED EFFECT, pre-registered

On BenchmarkRoundLoopDeltas256 (the axis where the quadratic dominates):

```
B/op    2,442,966  ->  under 400,000    (the quadratic term dies;
                                         the ~170 KB baseline and the
                                         per-event costs remain)
allocs        812  ->  roughly unchanged, possibly slightly higher
                       (one node-buffer append per delta replaces one
                       string realloc per delta)
ns/op                  falls, but it is NOT the claim
```

FALSIFIERS, both directions:

- if B/op falls but allocations fall a LOT, something other than the
  concatenation changed and it must be found before claiming;
- if B/op still grows superlinearly in the delta count, the message is
  still being accumulated somewhere and the split is incomplete;
- if the 16-delta case gets WORSE, the per-frame materialisation costs
  more than the per-delta copy it replaced at small N, which is a real
  outcome and would argue for a threshold rather than a rewrite.

## LEDGER EXPECTATION

Net-negative or neutral in lines. livedoc.Diff's use for the streaming
field goes away; asm.addText's coalescing branch simplifies; the node
gains a buffer and a materialise-on-handoff step. Do not promise a
number: measured deletions only.

## OPEN QUESTION FOR GLUCK

Does the durable IR still need the message assembled per round, or can
the record itself carry the blocks the node already holds? If the latter,
the message assembly disappears entirely rather than moving to round end
-- a larger change, and it touches the provider encoders, so it is not
folded in here without a decision.

- need to understand this better
