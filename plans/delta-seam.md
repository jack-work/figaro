# The delta seam: one accumulator per owner, one door for the log

APPROVED BY GLUCK 2026-08-18. Written by aria 091d162e (role @980dc16c)
from Gluck's design and this campaign's measurements. Companion note with
the raw numbers: `~/notes/figaro/two-consumer-split.md` and
`~/notes/figaro/addtext-quadratic.md`.

This replaces the "fix the quadratic" framing. The quadratic is a
symptom; the cause is that figaro rebuilds a message nobody asked it to
rebuild, because nothing emits fig IR deltas.

---

## THE DEFECT THAT STARTED IT, measured

`internal/figaro/turn.go:1601`, `asm.addText`, on every streaming turn:

    s.msg.Content[n-1].Text += text

Consecutive same-kind deltas coalesce into one block, and Go strings are
immutable, so each delta reallocates the whole accumulated text.
Confirmed by closed-form model, no fitted parameter:

    deltas   measured B/op   model 170,912 + 64*N(N+1)/2   error
        16         179,616                      179,616    baseline
        64         318,816                      304,032    +4.9%
       256       2,447,121                    2,276,256    +7.5%

Linear allocations, quadratic bytes. A 10 KB reply streamed over 1,000
deltas allocates ~5 MB to produce 10 KB. Tool output is clamped by
tailBound; PROSE IS NOT. It survived the memory campaign because that
campaign measured the COMPOSE path and nothing priced the ROUND LOOP.

## THE CAUSE: INFORMATION DESTROYED, THEN RECONSTRUCTED

The wire already speaks deltas. `Server.delta()` calls
`streamed("markdown", old, new)` -> `livedoc.Diff` -> a SPLICE. So:

  1. the provider hands us a DELTA
  2. asm.addText concatenates it into the full string      O(N) per delta
  3. compose.Nodes copies that string into livedoc.Node
  4. Server.delta() DIFFS it to recover the splice
  5. the wire sends the delta

We had the delta at step 1. Steps 2-4 reconstruct it.

## THE SHAPE, BY OWNERSHIP

    wire events
      └─► PROVIDER: its own native accumulator            (stays, always)
            ├─ per event ──► fig IR DELTAS ──► fanned out
            │                                   └─► UI IR builds its own
            │                                       text; emits splices to
            │                                       subscribers; keeps the
            │                                       accumulated form in
            │                                       memory for new ones
            └─ on close  ──► fig IR MESSAGE + native cache payload
                             handed over ONCE, by normal or premature close

THREE ACCUMULATORS EXIST AND ONLY ONE IS REMOVED:
  1. the provider's NATIVE accumulator (SDK `acc` in anthropicsdk, the
     hand-rolled `nativeMessage` in anthropic, a third shape in
     responses). STAYS WITH THE PROVIDER. It is load-bearing: `acc.ToParam()`
     marshalled IS the AssistantCache payload replayed for prompt caching,
     and only the provider knows its own wire shape. Hoisting it into
     figaro would re-implement three vendors' assembly rules.
  2. `decodeNativeMessage` -- the conversion, not an accumulator. Stays.
  3. `asm` in figaro's drain loop -- a THIRD representation that exists
     only to bridge first-delta to durable-append for the live view.
     DELETED.

## WHAT CHANGES, AND IT IS ONE CHANGE, NOT THREE

### (a) Providers stop owning the log

Five call sites across four providers run an IDENTICAL five-line ritual:

    entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
    if err != nil { return fmt.Errorf("<provider>: append assistant: %w", err) }
    msg.LogicalTime = entry.LT
    bus.PushMessageEnd(string(msg.StopReason))
    bus.PushFigaro(msg, native)

  anthropic 2 · anthropicsdk 1 · copilot/responses 1 · openaichat 1

`copilot/responses.go` is a genuinely different event shape and performs
the same ritual, which is the evidence that the ritual is not
provider-specific. Remove it and a provider's job becomes: turn a wire
stream into fig IR deltas and a native cache payload. No store handle, no
LT stamping, no append error path, no ordering obligation.

### (b) `asm` is deleted

Eight sites in `driveOneRound`: three mutators (`addText`, `toolOpen`,
`toolReady`) and five `asmMsg.message()` reads whose only job is to hand
the in-flight message to `emitLive`. Four disappear when the UI IR
consumes deltas directly. The fifth is the interrupt path -- (c).

### (c) Interrupt becomes a premature close, owned by the provider

Today figaro synthesises a partial message from `asm` and separately
synthesises "interrupted: tool execution was cancelled" results. FIGARO IS
INVENTING PROVIDER-SHAPED CONTENT, which is why that path has 11 repair
sites and still misses the fork case.

Instead: the Bus gains a close -- `Close(reason) (message.Message,
AssistantCache, error)` or equivalent -- and `PushMessageEnd` becomes its
normal-path sibling. Then the interrupt path and the normal path produce a
message THE SAME WAY, differing only in stop reason; the translator
payload for a truncated message is produced by the same accumulator, so it
cannot disagree with the fig IR record; and `asm` loses its last reader.

## INVARIANTS THAT MUST BECOME EXPLICIT

Today these hold by accident of statement order in one goroutine. Under a
fanned-out delta stream they must be stated and tested.

1. NO TRANSLATOR RECORD EXISTS BEFORE ITS FIG IR RECORD HAS AN LT.
   Translation entries carry `FigaroLT = entry.LT` (provider/projection.go),
   and today the fig IR append happens first, sequentially, in the same
   function. FANNING DELTAS OUT ASYNCHRONOUSLY BREAKS THAT SYNCHRONY: the
   provider could no longer stamp its own translation without a round trip.
   THIS IS WHY THE FIG IR SIDE OWNS THE TRANSLATOR LOG -- not tidiness,
   necessity. The translator signals "message complete"; fig IR stamps and
   appends both.
2. THE NATIVE PAYLOAD AND THE FIG IR MESSAGE COME FROM THE SAME
   ACCUMULATOR STATE AT THE SAME INSTANT. Otherwise a truncated close can
   hand over a message and a cache payload that disagree. Unstated today.
3. `PushToolReady` IS OPTIONAL. The Bus doc says providers may omit it and
   the harness falls back to dispatching from the assembled message. Any
   design assuming per-block dispatch MUST keep that fallback or a
   provider that omits it breaks silently.

## WHAT MUST BE PROVEN BEFORE IT IS BELIEVED

  - DELTA-EQUIVALENCE ORACLE: UI IR built from deltas equals UI IR
    composed wholesale, AT EVERY FRAME, not merely at seal. Same guard
    that made incremental composition trustworthy, same reason: a
    projection converging only at seal renders correct transcripts over
    wrong live frames. Keep the wholesale path permanently as the oracle.
  - THE ALIASING LAW, asked as a CAPACITY question, because no comparison
    of contents can see it. The UI IR node now owns a growing buffer;
    whatever it hands out must be immune to later appends. A
    `strings.Builder` alone does NOT satisfy this -- `String()` is
    `unsafe.String` over the live buffer and `grow()` only reallocates
    when capacity runs out, so a published node can be mutated under a
    reader INTERMITTENTLY. NOTE: TODAY'S QUADRATIC IS BUYING THAT
    CORRECTNESS; any fix must replace the guarantee, not just the cost.
    Precedent: region_memo_test.go asserts cap == len.
  - ORDERING INVARIANT TEST for (1), canaried by appending in the wrong
    order.
  - REACHABILITY PROOF before any before/after: break the changed line,
    show the round-loop benchmark moves. Standing rule after six
    documented instances of instruments not reaching the code.
  - ASK THE COMPILER before predicting allocations (`-gcflags=-m`). Four
    predictions in this campaign were spent learning that reading source
    tells you what is WRITTEN and escape analysis what is ALLOCATED.

## MEASUREMENT

Instrument exists: `BenchmarkRoundLoopDeltas{16,64,256}`, plus width and
tools axes, b.N-independent, sabotage-gated. Owned by aria 3a9225b1, which
gates every number; the implementer does not score their own work.

PRE-REGISTERED, at Deltas256:

    B/op    2,442,966 -> under 400,000   (the quadratic term dies; the
                                          ~170 KB baseline and per-event
                                          costs remain)
    allocs        812 -> roughly unchanged, possibly slightly higher
    ns/op                falls, but it is NOT the claim

FALSIFIERS, both directions:
  - B/op falls but allocations fall A LOT -> something other than the
    concatenation changed; find it before claiming.
  - B/op still superlinear in delta count -> the message is still being
    accumulated somewhere; the split is incomplete.
  - the 16-delta case gets WORSE -> per-frame materialisation costs more
    than the per-delta copy at small N. A real outcome, and it argues for
    a threshold rather than a rewrite.

## SEQUENCING

This is a state-door stage, not a patch: it rewrites `driveOneRound`, the
same path stages 2, 3 and 4 rewrite. Position: AFTER stage 2 (one door),
BEFORE stage 3 (the door records decisions). Stage 2 settles who writes;
this settles what the writers hand the projection; stage 3 then stamps
decisions at a door whose consumers are already separated.

## LEDGER

Expect net-negative. Deletions available and MEASURED, not promised: five
copies of the append ritual, `asm` and its eight call sites,
`livedoc.Diff`'s use for the streaming field, and a share of the 11
interrupt repair sites. Do not quote a line count before it is measured.

## OPEN, RESOLVED

An earlier draft asked whether the durable IR still needs the message
assembled per round. MOOT: figaro never assembled it. The provider builds
the fig IR record independently and appends it; `asm` was only ever the
live view's private copy. The question was based on a wrong picture and
the correction narrows this work in its favour -- the durable record and
the provider encoders are NOT touched.
