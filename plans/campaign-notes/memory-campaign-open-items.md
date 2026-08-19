# The memory campaign: what is still open, 2026-08-16

Closed out by aria 091d162e. Everything here is UNBUILT ON PURPOSE,
with the reason and the evidence it would take to change the answer.

## 1. P1/P2 -- why the mutant survives the reattach pty case

STATUS: open, hypothesis intact, LAST in priority because it is a
question about an INSTRUMENT, not about the product.

WHAT IS KNOWN. TestSmoke_ReattachMidStreamMatchesShow passes under a
mutant composer that claims every node is settled. Two hypotheses were
tested and both were wrong:

  - "ticks are the spinner repainting on a timer" -- REFUTED by
    source: tickRe matches `tick-(\d+)`, literal output from the 90-tick
    bash tool, and the case already asserts GROWTH, not liveness.
  - "the memo absorbs the lie" -- REFUTED by measurement: at compose
    level M1 produces 22 split-rule violations and 30 frames with a
    live-map dependency inside the prefix. The composer under M1 really
    does serve stale nodes and really does over-claim the boundary.

WHAT FOLLOWS. If the composer froze and the screen advanced, something
ELSE emitted frames. That is the open question, and it is about the
live path, not the composer.

HOW TO SETTLE IT CHEAPLY: rebuild M1, run the reattach case with
FIGARO_NODE_DEBUG pointed at a directory (turn.go logs every composed
frame there), and diff the composed frames against two pane captures
8 s apart. Composed frames frozen + ticks advancing names the second
emitter outright. Costs real provider tokens, so it wants a deliberate
decision, not a spare moment.

## 2. The translator's derive-and-write-back

STATUS: unbuilt, and it is the ONLY surviving piece of the layered
cache design. Its measurement spec is written in
~/notes/layered-cache-design.md under "THE ONE PIECE LEFT WITH A CASE".
A number first, then a design, then code.

## 3. Scenario 2 (mid-turn steer)

STATUS: unverifiable in the current harness -- it auto-promotes at 101
and 201 with chrome=2. Queued to the CLI/client fold refactor with its
evidence, unchanged.

## 4. DetachedTailAdvancesAndScreenHoldsStill

STATUS: pre-existing failure, attributed by canary to b734fcd0 (both
md5s recorded in ~/notes/figaro/detached-tail-failure.md). Belongs to
the CLI refactor's queue. Not touched by this campaign.

## 5. The memo's blindness to what it renders

STATUS: latent, guarded from a distance, documented. memoKey does not
key on partials, argPartials or timings, and keys Arguments by COUNT
rather than value. Safe today because internal/figaro emits one result
per call in one tic and stamps timings before durability -- both now
asserted where they live (61927498). The design for keying the memo on
what it RENDERS is written in ~/notes/figaro/s6-incremental-prediction.md
section (b), with its trigger: build it when a measurement says partial
invalidation is hot, OR when internal/figaro gains a path that appends
a call's result in more than one message (streaming tool results,
per-call tics). Either flips which option is cheap.
