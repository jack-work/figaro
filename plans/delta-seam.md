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

---

# AMENDMENT, Gluck 2026-08-18: the union has TWO members, and the
# provider never learns an LT

Settled after the first draft. This replaces the "three event types"
sketch and strengthens invariant 1 from a tested rule into a shape.

## THE CARDINALITY, STATED CORRECTLY

One fig IR message maps to N provider messages. THEREFORE FOR ANY
PROVIDER MESSAGE THERE IS EXACTLY ONE FIG IR MESSAGE. The relation is
many-natives-to-one-record, never the reverse, and the schema already
expresses it: projection.go appends
`Entry[[]json.RawMessage]{FigaroLT: entry.LT, Payload: encoded}` -- one
LT, a SLICE of natives. A tool tic carrying three tool_result blocks
becomes one Anthropic user message or three OpenAI messages; either way
it is one entry at one LT.

So a standalone translator event is not needed. Its "which LT?" is always
answered by the message it accompanies, which makes it a FIELD, not a
union member.

## THE UNION

    FigIRDelta    { block kind, tool id, text or json fragment }
    FigIRMessage  { message.Message, native []AssistantCache }   <- DONE

The fig IR message CARRIES its assistant cache, one or more messages.
That is what `PushFigaro(msg, cache ...AssistantCache)` already does; the
amendment is that it becomes the only completion path and the union's
only terminal member.

## THE PROVIDER NEVER LEARNS AN LT OR A TURN

The provider emits deltas, then emits one message with its natives, in
order. It does not stamp, does not append, does not hold a store handle,
and has no ordering obligation beyond "in order". EVERYTHING COORDINATE
IS STAMPED ON THE FIG IR SIDE, by the code that adds the records:
  - the fig IR append yields the LT
  - the same code stamps that LT onto the translator entry and appends it
  - the same code stamps the TURN (see stage 3, the door records
    decisions -- these are the same door)

CONSEQUENCE FOR INVARIANT 1: "no translator record exists before its fig
IR record has an LT" STOPS BEING A RULE THAT MUST BE TESTED AND BECOMES A
SHAPE THAT CANNOT BE VIOLATED. There is no code path that could emit them
out of order, because only one party has the LT and it holds both
appends. That is strictly stronger than the ordering test the first draft
specified, and the test becomes a regression guard rather than the
guarantee itself.

CONSEQUENCE FOR SendInput: the provider's `FigLog` handle exists mostly to
perform the five-line append ritual and to read history for catchUp. The
append role goes away entirely. Whether the read role can also be handed
over as data is a separate question and is NOT decided here.

## WHAT THIS DOES NOT DECIDE

There are TWO producers of translator records, and only one is the
provider:
  1. the provider, at message completion -- the exact input-ready native
     bytes, which matter for prompt caching and therefore must be the
     provider's own rather than re-encoded;
  2. ProjectIncrementally on the READ path (catchUp) -- everything else:
     user prompts, tool tics, encoded lazily.
That asymmetry is why AssistantCache exists as a special case at all. It
is unchanged by this amendment. If a case is ever found where a provider
must emit a translation with NO corresponding new fig IR record, the
union gains a third member -- but it is not defined against a
hypothetical. A union member that is never constructed is a permanent
invitation to construct it wrongly.

---

# PART II: ONE LOG CONTRACT, AND A DERIVED LOG THAT CARRIES SNAPSHOTS

Gluck's design, 2026-08-18, recorded from his specification. This
subsumes the splice question: with it, the provider holds no conversation
state at all -- no LT, no turn, no projection, no bookmark -- and the four
hand-written acceptAssistantProjection copies delete with nothing to
replace them.

## WHY THE PROJECTION MUST GO

internal/provider/IncrementalProjection is a second in-memory cache of
data the store already caches, with its own hand-maintained bookkeeping
(Entries, LastLT, and five carried version fields). It duplicates the JOB
of the log layer and, because it holds slices returned by the log, it
also PINS payloads the log's window believes it evicted.

RULED BY GLUCK: NOTHING MAY PIN EVICTED BYTES, for any reason. If a
caller needs a record it reads it, and the read brings it back. That
removes the retention question entirely rather than measuring it.

RULED BY GLUCK: cachedLog IS JUST "Log" (or FigLog). That it caches is an
IMPLEMENTATION DETAIL. It is the sole provider of append and read; the
caller does not know or care what is resident.

## THE CONTRACT

One base log for events. Reads take an INDEX SPAN and return the entries;
accessing a span brings those entries into memory, where they live until
eviction. Writes are WAL-shaped. Everything -- fig IR, translator IR,
forms -- is this one structure.

## THE DERIVED LOG: SNAPSHOTS OVER THE SAME STRUCTURE

A second log interface DERIVES the base one and adds folded state. It
EMBEDS the base log and OVERRIDES the equivalent methods: when a segment
file is loaded from the cache, the derived log unmarshals the header
bytes into a snapshot and stores it in ITS OWN cache, governed by THE
SAME EVICTION POLICY as the segment cache.

  - every segment carries a HEADER SNAPSHOT, always brought into memory
    alongside the segment itself;
  - snapshots are evicted WITH their segment: if the segment holding a
    snapshot's LT is evicted, the snapshot goes too;
  - a snapshot may be a segment header, or one built incrementally to
    answer an earlier request in the same iteration.

THE BOUND THIS BUYS, and it is the point of the whole design: BECAUSE THE
HEADER SNAPSHOT IS ALWAYS RESIDENT WHENEVER ITS SEGMENT IS, ANY SNAPSHOT
REQUEST COSTS AT MOST ONE SEGMENT'S WORTH OF PATCH APPLICATION. Never a
walk from the beginning of history.

## THE ITERATOR

Iteration accepts a lambda invoked per entry. That lambda receives a
second lambda, GetSnapshot, which it may call or ignore. GetSnapshot
encloses an implementation that catches the cached snapshot up FROM the
last snapshot it built TO the requested LT.

WORKED EXAMPLE (Gluck's). A form segment holds a header and ten entries,
LTs 10-19. The iteration asks for a snapshot at LT 15 and again at 17:

  LT 15: GetSnapshot takes the HEADER snapshot, already in memory beside
         the segment, and applies 10, 11, 12, 13, 14, 15. Returns it.
  LT 17: applies only 16 and 17 to the snapshot built at LT 15.
  LT 20: the next segment is loaded (or its cached form read). The
         previous segment becomes eligible for eviction.

The segment stays resident for the duration of the iteration that covers
its LTs, which is what makes the one-segment bound hold.

## THE ONE OPEN CHOICE, AND MY RECOMMENDATION

Gluck offered an alternative: give the iterator the LT RANGE up front and
PIN those entries until the iteration completes, even for segments
already passed. He judged it easier and left the choice open.

RECOMMENDATION: DO NOT PIN THE WHOLE SPAN. Pin the CURRENT segment only.
Reason: the provider's encode path iterates the WHOLE conversation, so
pinning the span pins the entire log for the duration of every turn --
which defeats the window on exactly the workload the window exists for,
and re-creates the retention the projection was just deleted for. The
segment-at-a-time rule bounds residency to one segment plus its header
snapshot regardless of conversation length, and the snapshot bound holds
either way.
Span-pinning stays available for a caller that genuinely revisits
indices; forward-only iteration, which is what encoding is, does not need
it. If a measured case appears where re-loading a passed segment costs
more than pinning it, that is the moment to add the option -- with the
number, not before.

## WHAT THIS DELETES

  - IncrementalProjection and its five carried version fields
  - four hand-written acceptAssistantProjection copies (~23 lines each)
    and five call sites
  - the five-line append ritual across four providers
  - the provider's need for a store handle, an LT, or a turn

## WHAT MUST BE PROVEN

  - THE ONE-SEGMENT BOUND, asserted as a COUNT, not a time: a snapshot
    request applies at most one segment's patches. Counting beats timing
    wherever the question is "how many times" -- the pattern this
    campaign already has in countingLog and in the identity oracles.
  - SNAPSHOT/SEGMENT EVICTION LOCKSTEP: a snapshot is never resident
    without its segment. Canaried by evicting a segment and asserting the
    snapshot went with it.
  - THE FOLD IS IDENTICAL to today's walk. Form state rendered into a
    message's wire bytes must be byte-for-byte what the current
    projection produces, or the per-LT cache makes a divergence
    permanent. Equivalence oracle, kept.
  - NOTHING PINS EVICTED BYTES, asserted rather than assumed.

---

# PART III: THE REQUEST BODY, STREAMED WHERE THE TRANSPORT ALLOWS

Gluck, 2026-08-18. Follow-on to Part II with its own gate.

## THE THIRD COPY

Part II removes the projection's index. It does NOT remove this, at
anthropic.go:985 and 1063 and its equivalents:

    body, err := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, "POST", url,
                                             bytes.NewReader(body))

That is a freshly allocated CONTIGUOUS copy of the entire conversation,
built every turn. Real bytes, not shared headers. At send there are
currently three full representations alive: the log's resident window,
the projection's index into it (Part II deletes this), and `body`.

## THE RULING: THE PROVIDER DECIDES ITS OWN TRANSPORT

Streaming is the ideal, but it is a PER-PROVIDER decision and a provider
that must marshal the whole request into memory may do so. The seam does
not impose a transport.

  ANTHROPIC (and the other HTTP providers): HTTP/1.1 CHUNKED TRANSFER
  ENCODING. http.NewRequest takes an io.Reader, so an io.Pipe fed by a
  json.Encoder writing the messages array AS THE ITERATOR WALKS THE LOG
  never materialises the body.

  COPILOT RESPONSES: NOT APPLICABLE, and this was checked rather than
  assumed -- responses.go:191 is `websocket.JSON.Send(conn, request)`
  over golang.org/x/net/websocket. It marshals a whole frame; chunked
  HTTP has no meaning there. Responses keeps its in-memory marshal.

## WHAT STREAMING DOES AND DOES NOT BUY

  DOES: peak becomes O(one segment) instead of O(conversation) per turn.
  DOES NOT: it is not O(1). Record bytes still become resident as the
  iterator touches each segment. For a long aria that is the difference
  between megabytes and kilobytes per turn, which is worth having, but
  the claim must be stated in those terms and not as "no copy".
  UNAFFECTED: prompt caching. The bytes on the wire are identical; cache
  keys are content, not framing.

## THE THREE RISKS, AND THE ONE THAT DECIDES IT

  1. CONTENT-LENGTH DISAPPEARS under chunked encoding. HTTP/1.1 permits
     it; some API gateways and CDNs reject chunked request bodies or
     require a length. THIS IS THE RISK THAT DECIDES FEASIBILITY and it
     is a short experiment against each endpoint, not an argument.
  2. RETRIES RE-WALK. http.Request.GetBody must be set so a retry can
     replay the body; the iterator can re-walk, so this is cheap.
  3. ERROR TIMING SHIFTS. Today a marshal failure happens BEFORE the
     request opens. Streamed, it happens MID-BODY, so the request must
     abort cleanly rather than send a truncated payload that the far end
     might accept.

## TESTING, AS ORDERED

Risk 3 is tested in UNIT TESTS by MOCKING THE UPSTREAM DEPENDENCY and
SIMULATING A THROW mid-body, with TEXT FIXTURES for the payloads. The
assertion is that a mid-stream failure produces a clean abort and a
propagated error -- never a truncated request the far end could accept as
complete. A truncated-but-accepted request would be a silent corruption
of the conversation, which is the worst outcome available here and the
reason this is a unit test rather than a live experiment.

## GLUCK'S STANDING INSTRUCTION ON THIS DESIGN

"Assume my design is ideal, we can make it work, even if it regresses
perf. If it does let me know and we will tune it appropriately, but I
think removing the memory pressure will speed things up and remove code
rather than slow things down."

So a performance regression is REPORTED, not treated as a veto. The
design is not re-litigated on a number; it is tuned. That is a deliberate
inversion of this campaign's usual rule and it is recorded here so nobody
later reads it as the discipline slipping.

---

# PART IV: THE ITERATOR IS THE CANONICAL INTERFACE

Gluck, 2026-08-18: "the log iterator should be the canonical interface
for interacting with any of the logs. All of the logs should use it."

That is broader than the provider path and it is the point of the whole
consolidation. NOT "the provider gets an iterator instead of a slice" but
THE ITERATOR IS HOW ANYTHING TALKS TO ANY LOG. Fig IR, translator IR,
forms, librettos -- one contract, one traversal, one residency policy.

CONSEQUENCE FOR EVERY EXISTING READ SHAPE. Read(), ReadFrom(), ReadPage(),
TailSnapshot(), TailAfter() and the rest are today six ways of asking the
same question, each with its own materialisation and its own residency
behaviour -- and ReadPage(from, 0, n) on a trimmed window ALWAYS falls
through, which is a trap already documented in
~/notes/layered-cache-design.md. Under one iterator they become spans over
one traversal. Callers that genuinely want everything in memory ask for a
span and get it; callers that stream get the same interface and never
materialise.
DO NOT convert them all at once. Land the iterator, move the provider
path onto it (Part II), and convert the rest behind it as each is measured
-- but the DESTINATION is that no caller reaches a log any other way.

## CORRECTION TO PART II's TEST PLAN: THE FINGERPRINT IS THE MECHANISM

An earlier draft called for a permanent equivalence oracle against
today's folding walk. GLUCK CORRECTED THIS AND HE IS RIGHT: the guarantee
belongs in the cache, and it is already there.

Every cached translation entry carries a FINGERPRINT (anthropic's is
"anthropic/" + reminderRenderer + "/v5"), and lookupCached REFUSES any
entry whose fingerprint differs from the current one. So a change to how
form state folds into wire bytes is handled by BUMPING THE VERSION: stale
bytes become unreachable rather than detected. Same move as making the LT
ordering structural -- a rule converted into a shape.

WHAT THE FINGERPRINT DOES NOT DO: tell us the NEW fold is correct. Bump
it and a wrong fold is permanent and consistent -- cleanly wrong,
everywhere, forever. So one property must be proven ONCE, before the
change ships, and it is smaller than an oracle:

  1. FOLD-FROM-HEADER EQUALS FOLD-FROM-ZERO at every LT across a
     multi-segment fixture. This is the associativity the design rests
     on: a header snapshot must be exactly "everything before this
     segment, already applied". If it is not, the one-segment bound is
     unsound and this catches it immediately.
  2. THE FINGERPRINT MOVED. A test that fails if the fold implementation
     changes without the version string changing. Cheap, and it prevents
     the single mistake that would make wrong bytes permanent.

No permanent oracle is needed: the mathematical property is the thing,
not a comparison against an implementation we intend to delete.

---

# PART V: THE INTERRUPT CONTRACT

Gluck, 2026-08-18: "all in flight messages should be written on
interrupt, any half constructed messages ideally should be closed and
written, but all new work should stop then."

THIS IS A USER-VISIBLE BEHAVIOUR CHANGE and it is stated here once so it
does not arrive as a surprise.

## WHAT HAPPENS TODAY

An interrupt DISCARDS the assistant message. driveOneRound guards the
real append with `!a.isInterrupted()`, so a message the provider has
already completed -- streamed, finished, handed over -- is thrown away if
the user pressed escape a moment later. deferredAppendLog exists to make
that possible: it fakes the append, hands the provider a PREDICTED LT
(tail.LT + 1), stashes the entry, and lets figaro decide later whether to
write it.

WHY IT DISCARDS RATHER THAN WRITES, and this is not tidiness: a completed
assistant message can contain tool_invoke blocks. Writing it without
running the tools leaves a tool_use with no tool_result, the next prompt
is refused with a 400, AND THE ARIA IS BRICKED. The same failure as the
fork bug. So discarding was protecting the pairing invariant.

## WHAT HAPPENS UNDER THIS CONTRACT

    IN-FLIGHT MESSAGES ARE WRITTEN. Not discarded.
    HALF-CONSTRUCTED MESSAGES ARE CLOSED, THEN WRITTEN.
    ALL NEW WORK STOPS.

"Closed" is the provider's premature close (Part I): the provider owns
its accumulator, so it produces the message, with its own stop reason,
and its native cache payload from the same accumulator state. Figaro does
not synthesise provider-shaped content -- which is what it does today,
across 11 repair sites, and why that path still misses the fork case.

"All new work stops" means: no further provider round, no new tool
dispatch, no new prompt drained. Cancellation already does this and needs
no new machinery -- turnCtx exists at turn.go:141 and reaches
prov.Send(turnCtx, ...). A context cancel is a request to STOP DOING
WORK; it can never un-produce work already finished, which is precisely
the gap deferredAppendLog was filling.

## WHY WRITING IS NOW SAFE, WHICH IS THE WHOLE PRECONDITION

Because THE DOOR CLOSES OPEN TOOL CALLS IN THE SAME CRITICAL SECTION AS
THE APPEND (Part I, stage 3). An interrupted assistant message carrying
an unanswered tool_invoke gets its closing tool_result stamped as it
lands. The pairing invariant that discarding was protecting is protected
by the door instead -- structurally, at the one place every append passes
through, rather than by refusing to write.

SO THE ORDER IS LOAD-BEARING: this contract MUST NOT ship before the
door. Writing interrupted messages without the door re-creates the
bricked-aria bug deliberately.

## WHAT DELETES WHEN IT LANDS

  deferredAppendLog and its predicted LT (turn_repair.go:258-311)
  the "provider appended more than one assistant message" error
  the appendedEntry.LT != assistantIdx prediction check
  the !a.isInterrupted() append guard
  most of repairTurnTail's synthesis of provider-shaped content

## THE UX, PLAINLY

TODAY: the model finishes a paragraph, you press escape, THE PARAGRAPH IS
GONE.
AFTER: it is kept, properly closed, and visible. Losing completed work to
a race is a worse outcome than keeping it, and the user pressed stop --
not undo.

## PART V, AMENDED: THE DOOR REPAIRS ON THE NEXT APPEND

Gluck, 2026-08-18: "if the interrupt ends up writing a tool message
without the content block that closes it, it should be repaired when the
next append happens. That is the original premise of our changes."

THIS WEAKENS THE REQUIREMENT ABOVE AND IS THE ORIGINAL PREMISE RESTORED.
Part V as first written implied the interrupt path must close open tool
calls AT INTERRUPT TIME, atomically. It does not. The door repairs ON THE
NEXT APPEND -- which is what makes the fork bug die structurally in the
first place, since a fork's birth record IS the next append.

So the interrupt path may write an assistant message carrying an
unanswered tool_invoke. That state is legal ON DISK and becomes illegal
only if it reaches a PROVIDER, and nothing reaches a provider without
passing through an append first. The door closes it then.

CONSEQUENCE: the interrupt path needs no closing machinery of its own. It
writes what it has and stops. Everything that makes the result legal is
already the door's job, and was going to be built anyway.

## COVERAGE GAP, NAMED BEFORE THE CHANGE RATHER THAN AFTER

NOTHING IN THE TREE ASSERTS WHAT HAPPENS TO A COMPLETED ASSISTANT MESSAGE
WHEN THE USER INTERRUPTS, in either direction. Verified:
  TestAgent_Interrupt            asserts the DONE REASON
  TestSmoke_ExitKeysWork         interrupts mid-stream (a `sleep 60` cut
                                 at 12s) and asserts THE PROCESS EXITS
Neither reads the log. So the most user-visible consequence of an
interrupt is unguarded on the eve of inverting it.

REQUIRED BEFORE PART V LANDS: a test pinning TODAY's behaviour -- the
message is DISCARDED -- so that Part V turns it red and whoever lands it
must invert it DELIBERATELY. It must be inverted, not deleted: it would
be the only test that looks at the log after an interrupt.

091d162e attempted this and failed at the test's own vacuity guard three
times; the attempt is not in the tree. Recorded so the next hand skips
the same holes: the agent Config needs a Form (from backend.FormState);
a.Interrupt() called SYNCHRONOUSLY from inside Send DEADLOCKS, because
Interrupt blocks until the turn winds down and the turn cannot wind down
while Send is running; and even in a goroutine the provider's Send did
not complete, cause undiagnosed. The guard behaved correctly every time
-- it refused to report a result from a run where the provider never ran,
which is the only reason the failure was visible rather than green.

---

# PART II STAGE 2, RULED: THE SHAPE OF THE SNAPSHOT SEAM

Aria 7e151902 (role @980dc16c), 2026-08-18, before any stage-2 code was
cut. Three of these correct the stage as WRITTEN above; they are recorded
here as new paragraphs beside it rather than as edits to it.

## THE MECHANISM EXISTS AND THE BOUND ALREADY HOLDS

Part II says "every segment carries a HEADER SNAPSHOT". IT ALREADY DOES,
and not in the place two readers of figwal first looked. The per-baseIndex
WATERMARK FILES (`<chDir>/<base>.jsonl`) are written only at channel
creation, recovery and fork backfill — that reading is correct and it is
not the mechanism. The mechanism is figwal's OPAQUE BLOCK-0 SEGMENT
HEADER: `Options.OnSegmentOpen` puts a log in header mode, `channelOpts`
wires EVERY reducible channel to `reducibleFold`, and `openActiveLocked`
folds `(prevHeader, sealedPayloads)` and calls `WriteHeader` on every
segment creation — which is every rotation. `StateAt(idx)` then folds that
header with `[segBase..idx]` and nothing earlier.

MEASURED, not read (probe /var/tmp/fig-hdr-probe, counting reducer,
figwal v0.18.0 from the module cache, 4 KiB segments, 400 patches, 8
segments): 0 folds during the appends; 400 on the first StateAt (the
deferred rotations settling — each record folded ONCE, ever); then exactly
44 = idx − segBase + 1 at idx 400, 300 and 200; 1 at idx 1; and 44 again
on the first call after Close and REOPEN, so the bound holds cold from
disk. Full account: ~/notes/figaro/segment-headers-already-there.md.

CONSEQUENCE: the write side of this design is ALREADY PAID on every aria
on disk, and figaro has never read it — `StateAt`/`HeaderAt` have zero
non-test callers in `internal/`.

## SO STAGE 2 IS A MEMO, NOT A PERSISTENCE LAYER

figwal re-folds from the header on EVERY `StateAt`; it keeps no memo of
the snapshot it last built. The worked example above (LT 15 then LT 17
costing 6 then 2) is therefore exactly what stage 2 must add, and all it
must add. Nothing new is written to disk.

TWO COUNTS DECIDE WHETHER IT WAS BUILT RIGHT, and neither is a time:

  UNMARSHALS PER SNAPSHOT REQUEST == 1. `formReduce` (xwal_store.go:177)
  unmarshals the state, applies one patch and marshals it back PER RECORD;
  its own comment prices that at 97us decode / 76us encode on a 15KB
  board. Folding through it would pay that per patch. The derived log
  takes the HEADER BYTES, unmarshals ONCE, and folds the segment's patches
  DECODED through `form.Fold`.

  THE MEMO IS REACHED. A second request inside one iteration applies only
  the patches BETWEEN the two LTs. This count must read ZERO before stage
  2 by construction — figwal has no memo — which makes the counter's
  current value a prediction that can be checked, and therefore a counter
  proven able to fail.

## THE FIGWAL CHANGE THIS NEEDS, AND IT IS ADDITIVE

`xwal.Store` exposes `StateAt` but NOT `HeaderAt`; `HeaderAt` and
`SegmentBaseIndexes` live on `disk.Log` and xwal reaches them only for a
count in its stats. Exposing them at the store level is the whole change:
expose what exists, rather than write new watermarks.

## THE PROPERTY THAT WAS NOT WEIGHED: TWO IMPLEMENTATIONS MEET IN ONE
## SNAPSHOT

The header half of every snapshot is folded by figwal through
`formReduce`, i.e. THROUGH JSON. The tail half is folded by figaro through
`form.Fold`, i.e. DECODED. They agree only if `form.Snapshot`'s
Marshal/Unmarshal round trip is EXACT — nil vs empty, key sets, unknown
fields, ordering.

So fold-from-header == fold-from-zero is necessary and NOT sufficient: it
can pass on a fixture that happens not to carry the key the round trip
drops. A RICH-CORPUS ROUND-TRIP IDENTITY belongs beside it, and it comes
FIRST, because its failure is the one that gets STORED under a fingerprint
asserting the bytes are right.

## THE ALIASING QUESTION, ASKED AND ANSWERED IN THE DESIGN'S FAVOUR

Part I had to ask the aliasing law as a CAPACITY question because a
`strings.Builder` can be mutated under a published reader. THAT HAZARD
DOES NOT EXIST HERE: `form.Snapshot` is an immutable persistent tree
(`form/form.go`: a `root *node`, `Clone` is the identity, `Apply` returns
a new snapshot, `FromMap` copies), and `State` publishes it through an
atomic pointer precisely because it is safe to share. A memo may hand the
same snapshot to any number of readers, and a later fold cannot disturb
one already handed out.

WHAT STILL MUST BE COUNTED is the other half of the rule — NOTHING PINS
EVICTED BYTES. A snapshot retains the values folded into it; those values
must be decoded copies, never slices of the log's resident payload. That
is a residency count, not an argument.

## THE ITERATOR SHAPE, since stage 1 shipped one without snapshots

Stage 1 landed `Entries(Reader[T], Span) iter.Seq[Entry[T]]`. The snapshot
seam is a SECOND sequence, not a change to that one:

    iter.Seq2[Entry[T], func() (S, error)]

Callers that do not want folded state keep the one-value form and pay
nothing. Three rules on the accessor, each of which is a MISS rather than
a LIE, in the layered-cache tradition:

  1. IT IS VALID FOR ITS OWN STEP. Called after the iteration has moved
     on, it returns an ERROR — never a snapshot from another position.
  2. A BACKWARD REQUEST IS LEGAL AND BOUNDED. It re-folds from the header;
     it must never silently rewind the memo, and the count instrument must
     see it as what it is.
  3. IT RETURNS AN ERROR, NOT A ZERO SNAPSHOT, when the fold cannot be
     performed. An empty board and an unavailable board are different
     facts, and only one of them is a form with nothing in it.

---

# THE FORM CHANNEL'S WRITER INVARIANT, NAMED

Found and measured by aria 041454f1; ruled and recorded by d921742d (role
@980dc16c), 2026-08-18. It is stated HERE, in the plan, and not only in
the test that guards it, because the person who will violate it is the
author of the NEXT append site and they will read this before they read
a test in another package.

## THE INVARIANT

    EVERY WRITE TO THE FORM CHANNEL PASSES ITS PATCH THROUGH
    encoding/json BEFORE IT REACHES Append.

Three sites today, enumerated rather than assumed:

    internal/store/form.go:547   Form.Apply -- the central path
    internal/store/xwal_store.go writeBirth
    the stump path, identically

## WHY THE SEAM DEPENDS ON IT

Stage 2 assembles a snapshot from two implementations: the header half is
folded by figwal through `formReduce`, i.e. THROUGH JSON, and the tail
half by figaro through `form.Fold`, i.e. DECODED. `encoding/json` rewrites
bytes on the JSON route — `marshalerEncoder` (encode.go:487) compacts
every Marshaler's output with `escapeHTML` true, and `json.RawMessage` IS
a Marshaler, so every value passes through it. That is not our code and it
is not avoidable while the header is produced by `json.Marshal`;
hand-emitting the object is rejected by form.go BY NAME, because
delegating is what makes byte-identity with the pre-tree format true by
construction.

THE TWO HALVES AGREE ANYWAY, and the reason is the third door: THE
REWRITE IS IDEMPOTENT AND THE WRITER HAS ALREADY APPLIED IT. Every value
on disk is already a fixed point of the JSON route, so the header half and
the decoded half hold THE SAME BYTES rather than merely equal ones.

MEASURED, at the wire and not at the snapshot: 136 (segBase, lt) pairs
across a 15-record fixture, RENDERED WIRE BYTES identical at every one —
both as a whole-board render and as the PREV of a further patch, so that
`.OldString` and the board-carried delta limits participate. A divergence
in the second would have moved TRUNCATION, which nothing was watching.

## WHAT HAPPENS IF IT IS BROKEN

A payload built by hand — never through `json.Marshal` — makes the two
folds put DIFFERENT BYTES on the wire for the same LT. Since those bytes
are what the per-LT translation cache stores and what prompt caching keys
on, the model's prompt would depend on WHEN A SEGMENT ROTATED.

`TestSeam_TheWriterCanonicalisesAndThatIsWhyItAgrees` asserts that
divergence deliberately, so the day someone makes hand-built payloads safe
the test goes RED and tells them an invariant they did not know about has
moved. Assert the fact, not the wish.

## THE OPEN QUESTION: CAN THIS BE A SHAPE INSTEAD OF A RULE

This campaign's two best moves both converted a rule into something
unstatable: piece A made a stray provider `Append` FAIL TO COMPILE, and
the amendment gave the LT to exactly one party so the ordering could not
be expressed wrongly. The analogue here is a form-channel append that
takes a TYPED `message.Patch` and marshals it itself, so hand-built bytes
cannot arrive. NOT BUILT, and not to be built before the shape is
reported: whether those three are the only byte-level doors to `chanForm`,
whether that door is reachable from outside package `store`, and what a
typed funnel would cost in churn. If it is cheap it DELETES this section
by making the invariant unstatable; if it is not, this section stands
alone and says so.

## AND THE ASSERTION THIS STAGE OWES, SHARPENED

Part II said the one-segment bound is asserted as a COUNT rather than a
time. THAT IS NO LONGER SUFFICIENT. An equivalence oracle is structurally
blind to a header that reads one record TOO MANY — form patches are
idempotent, so re-applying a record the header already holds is a no-op;
measured, that error is caught in 0 of the 120 pairs where lt > segBase.

    THE COUNT MUST BE EXACT, NOT A BOUND:
    folds == lt − segBase + 1 on a cold memo,
    folds == lt − lastMemoLT on a warm one.

"At most one segment" is SATISFIED by a header one record ahead, which is
precisely the case no oracle can see. Full account:
~/notes/figaro/instrument-not-reaching-the-code.md, under "the oracle whose
subject is invariant under the error".

## CORRECTION, IN ITS OWN PARAGRAPH: THE EXACT COUNT IS NOT SUFFICIENT
## EITHER, AND THE MISTAKE WAS MINE

d921742d, 2026-08-18, on aria 9ed3f561's falsification. The section above
sharpens the one-segment assertion from a bound to an EXACT COUNT and says
that is the instrument the oracle cannot supply. IT IS NOT.

THE COUNT IS COMPUTED FROM THE HEADER'S OWN DECLARED BASE. A header that
holds one record too many while still declaring base b makes GetSnapshot
fold records[b..lt] — exactly lt − segBase + 1 applications, so the
equality PASSES — and the surplus record re-applies idempotently, so the
value matches too. Canaried on a reference model of stage 2's shape: the
equality does not fire in the ahead direction.

THE REUSABLE PART, because this is a level above the usual instance: AN
INSTRUMENT WHOSE REFERENCE IS DERIVED FROM THE THING UNDER TEST CANNOT SEE
AN ERROR IN THE REFERENCE. I ruled a bound up to an equality and did not
ask what the equality was measured against.

## SO THE BOUNDARY OWES THREE INSTRUMENTS, AND NONE IS REDUNDANT

    HEADER IDENTITY    the header compared against a fold from zero at its
                       DECLARED base. One comparison per segment, no
                       counting, no build tag. THE ONLY ONE THAT SEES
                       AHEAD-BY-ONE. Measured on the model: 4 of 4
                       segments ahead, 3 of 4 behind (segment zero's
                       header is empty under either clamp).
    FOLD COUNT, EXACT  folds == lt − segBase + 1 cold, == lt − lastMemoLT
                       warm. Sees a memo that re-folds and a bound merely
                       satisfied.
    VALUE ORACLE       the fold agreeing with today's walk at every
                       prefix. Sees skips and wrong values.

The 13-of-136 catches reported earlier at lt == segBase WERE header
identity, reached by accident at the one LT where the two coincide.
Asserted directly, it stops being an accident.

## TWO CONSTRAINTS THE REFERENCE MODEL PRODUCED BEFORE ANY CODE

  THE MEMO CARRIES AN EXPLICIT VALID BIT, NEVER A SENTINEL LT. A
  zero-valued memo claiming to stand at LT 0 makes every request inside
  the FIRST segment silently skip its first record. figaro's LTs start at
  1, so the defect would have been invisible in production until something
  numbered from zero — a shape that survives only by an unrelated
  convention is a shape waiting.

  PUBLISH WHAT WAS WRITTEN. internal/store/form.go:547 marshals the patch
  for disk and six lines later publishes the in-memory snapshot from the
  SAME patch VERBATIM, so the live board and the board read back from disk
  can hold different bytes for one value — and for OBJECT- and
  ARRAY-valued keys that divergence reaches the wire, where genericBody
  hands objects over raw. The fix is one round trip per form patch (per
  set, not per delta): publish the snapshot built from the bytes that were
  WRITTEN. NULL TODAY, STRUCTURAL AFTERWARDS — verbatim bytes can enter at
  internal/cli/form.go:30, but rpc/caller.go:277 marshals params in
  transit, so no known path delivers them to Form.Apply. The writer
  invariant above is about DISK and would never have seen this: two halves
  of one seam.

## PART V, ADDENDUM: TWO NEARLY IDENTICAL SENTENCES, ONE OF THEM DISCARDED

Found by aria 7e151902 while building the durable-log instrument for the
Ctrl-C case; recorded by d921742d, 2026-08-18.

On an interrupt today the resultTic is NEVER APPENDED: turn.go:751 takes
the repair branch and returns before the append at 763. So the string
`interrupted: tool execution was cancelled`, built in collectToolResults,
is discarded — and the string that actually reaches disk is
`interrupted: tool execution did not complete`, written by repairTurnTail.

Two sentences one word apart, one durable and one not, and nothing says
which is which. PART V SHOULD COLLAPSE THEM RATHER THAN INHERIT THEM: it
is rewriting exactly this path, and after it the interrupt path writes
what it has rather than synthesising provider-shaped content, so there is
one place for the sentence to live.

AND THE DURABLE ONE IS NOW LOAD-BEARING FOR A TEST. `InterruptedToolNotice`
is exported precisely so the pty case and production cannot drift, and it
is the only mark reachable ONLY through the figaro.interrupt RPC —
Agent.Interrupt sets isInterrupted, repairTurnTail runs only under it. A
client that cancels its own context and exits cannot produce it. Whoever
changes that string, or the branch that writes it, is changing the only
thing that distinguishes "the turn was stopped by the interrupt" from "the
turn stopped somehow".

SCOPE, STATED BECAUSE IT IS EASY TO OVERQUOTE: that mark requires A TOOL
IN FLIGHT. It proves Ctrl-C reaches the daemon for a turn with an open
tool call. A PROSE-ONLY interrupt — the model mid-paragraph, no tool
running — is a different case and is not covered by it.

---

# THE LEDGER, MEASURED: INSURANCE, NOT-A-SAVING, AND THE DELETION

d921742d (role @980dc16c), 2026-08-18. The LEDGER section above says
"expect net-negative" and "do not quote a line count before it is
measured". This is the measurement, and it moves the stage's headline.

## WHAT WAS MEASURED, AND BY WHOM

Aria 9ed3f561 walked the owner's real store, READ-ONLY, counts and sizes
only. d921742d reproduced the channel counts independently by a different
walk before any of it was reported upward.

    form channels on disk                 1,218   (1,217 on the second
                                                   walk: one aria was born
                                                   between them — ours)
    segment files                         1,218
    channels with MORE THAN ONE segment       0   (0.0%)
    fattest channel                     117,013 B = 5.6% of ONE 2 MiB
                                                    segment
    records per channel      median 3 · p90 8 · p99 42 · max 371
    mean record size                      2,326 B → ~900 records per
                                                    2 MiB segment

## PART ONE: THE FOLD BOUND IS INSURANCE

NOTHING HAS EVER ROLLED. Every form channel in the store is a single
segment, so a header at base 1 IS the initial state and folding from the
header IS folding from zero. THE ONE-SEGMENT BOUND DOES NO WORK TODAY. A
median snapshot request folds THREE patches; the p99 folds 42; the worst
channel in 1,218 folds 371.

It becomes real the day a channel rolls: bounded at ~900 records where an
unmemoised fold grows without limit. That is a future property, stated in
the future tense.

CAVEAT CARRIED FORWARD, and it is 9ed3f561's: 1,218 channels at median 3
records means the store is dominated by SHORT-LIVED arias. A libretto held
for weeks, or a role form patched every turn, moves the p99 and starts the
bound working. This says the bound has not worked YET, not that it will
not.

## PART TWO: THE UNMARSHAL SAVING IS NOT A SAVING AGAINST TODAY'S CODE

A saving of "one whole-board decode per request instead of one per record"
was measured and then WITHDRAWN, because it prices a route this plan
already rejected. Today figaro does NOT decode the board per record:
`ProjectIncrementally` folds DECODED PATCHES from the resident window, and
the cold path (`form.go:290`, `patchesFromLog`) unmarshals ONE
`message.Patch` per record — not the board. The whole-board
Unmarshal/Apply/Marshal belongs to `formReduce`, which runs on ROTATION
and inside figwal's `StateAt` — and StateAt is exactly the route ruled
against earlier in this document.

    A SAVING MEASURED AGAINST AN ALTERNATIVE WE REJECTED IS NOT A SAVING.

Nobody quotes it when this lands.

## PART THREE: THE HEADLINE IS THE DELETION

What stage 2 buys on today's data is what Part II said it buys in its
first paragraph: `IncrementalProjection` and its five carried version
fields, the four hand-written `acceptAssistantProjection` copies, the
provider's need to hold an LT, a turn, a projection and a bookmark — and
Gluck's ruling that NOTHING MAY PIN EVICTED BYTES, which the projection is
what violates.

WHAT THE DELETION RETURNS, as a LOWER BOUND and never to be quoted
otherwise: the encoded native messages a projection carries are, ON DISK,
median 0 B · p90 249,455 B · p99 2,463,337 B · max 9,841,806 B, totalling
200.2 MiB across 1,463 channels. The projection holds DECODED Go values,
commonly several times larger. THE REAL FIGURE IS A HEAP MEASUREMENT, NOT
A FILE WALK, and it has not been taken.

## WHY THIS IS RECORDED RATHER THAN QUIETLY ABSORBED

Gluck's standing instruction on this design is that a regression is
REPORTED and TUNED, not treated as a veto. There is no regression here —
only a benefit that is STRUCTURAL rather than TEMPORAL. What changes is
what may be CLAIMED when it lands, and a stage whose justification is
written down honestly before it ships cannot be re-justified afterwards by
whoever needs it to have been worth it.

## CORRECTION TO THE LEDGER: ONE LINE ITEM WAS ALREADY BANKED

d921742d, 2026-08-18, on fd15d2a0's grep. The LEDGER above lists "the four
hand-written acceptAssistantProjection copies" among what stage 2 deletes.
THEY ARE ALREADY GONE: `git log -S` puts their death at 493a6bcb, nine
sites — four calls and four definitions — removed together with the
five-line append ritual, in PIECE A.

So they must NOT be re-quoted when the deletion lands, on exactly the
principle the ledger was written under: a debt counted twice is a claim,
not a measurement. What actually remains, enumerated from source at
fb08a77c:

  - `IncrementalProjection[T]` and its carried fields (State, Form,
    Entries, LastLT, LastFormVersion, LastStudyVersions,
    FormVersionOfSnapshot)
  - `ProjectionConfig.Previous` and the warm-start watermark block
  - the `projection` field on all four providers, its mutex-guarded read
    and write-back in catchUp, and openaichat's `p.projection = nil`
    cache-clear path
  - THE RETENTION: `State` carries the ENCODED natives — the 200.2 MiB
    lower bound, and the thing that pins bytes the log's window believes
    it evicted

## AND THE CONSUMER-SIDE OFF-BY-ONE, RULED BEFORE THE BRIDGE IS CUT

Also fd15d2a0's, found by reading the fold order rather than by running
anything. `ProjectIncrementally` folds `snap = form.Fold(snap,
msg.Patches)` AFTER `Encode`, and sets `msg.Patches =
PatchesBetween(lastForm, entry.FormChannelVersion)`. SO ENCODE RECEIVES
THE BOARD AS OF THE PREVIOUS RECORD, with this record's transitions
carried separately as a delta block.

THEREFORE THE IR PATH PASSES THE **PREVIOUS** ENTRY'S FormChannelVersion
to the snapshot accessor, not its own. Passing its own would hand the
encoder a board with this record's transitions already folded in, and the
encoder would render them TWICE — once as state and once as a delta.

AND NO VALUE ORACLE CAN SEE THAT, for the reason this stage has now met
four times: form patches are IDEMPOTENT, so a board one record ahead,
folded with the delta again, is the SAME BOARD. The only visible
difference is in what the transition block SAYS, on the records where one
is rendered. It is the header-one-record-ahead blindness arriving on the
consumer side.

THE FIRST RECORD HAS NO PREVIOUS, and that is a legitimate case, not an
error: its base is the EMPTY board, which is what a cold walk starts from
(`snap = form.Snapshot{}`, `lastForm = 0`). An accessor asked for version
0 must ANSWER with the empty board rather than refuse — which is precisely
why the memo carries an EXPLICIT VALID BIT and not a sentinel index.

## BOTH OF THE ABOVE, RE-VERIFIED BY THE INCOMING BEARER — AND THE FIRST
## ONE ANSWERS ABOUT A BRANCH

f3aa1d0b (role @980dc16c), 2026-08-18, on taking the role from d921742d,
who handed both findings over as EVIDENCE and not as settled. Verified
rather than inherited, and one of them needs a qualifier that was not
wrong so much as unstated.

THE OFF-BY-ONE IS CONFIRMED, by reading the loop rather than the summary.
In `ProjectIncrementally`'s miss branch, `snap` is caught up to `lastForm`
— which at that instant still holds the PREVIOUS record's version — then
`msg.Patches` is set to `PatchesBetween(lastForm, entry.FormChannelVersion)`,
and only AFTER that does `lastForm` advance to this record's version. So
Encode does receive the board as of the previous record, with this
record's transitions carried as a delta, exactly as stated. The ruling
stands as written.

THE PAID DEBT IS CONFIRMED, WITH A SCOPE THAT MUST TRAVEL WITH IT.
`acceptAssistantProjection` died at 493a6bcb, nine sites, and 493a6bcb is
ON feat/delta-seam. IT IS NOT ON feat/layered-cache. Counted today:

    feat/delta-seam      2 mentions, both in a test and a comment, no code
    feat/layered-cache   9 LIVE SITES across the four providers

So a reader standing in the layered worktree — which is where this plan
LIVES — greps the name, finds nine live sites, and concludes the debt is
still owed. It is not: it is paid on the branch that merges back. The
ledger line is retired either way and must not be re-quoted when the
deletion lands.

THE REUSABLE PART, and it is this campaign's own thesis wearing another
hat: A GREP ANSWERS ABOUT THE WORKING TREE YOU ARE STANDING IN, NOT ABOUT
THE CAMPAIGN. Where work is split across branches that have not merged,
"the code still does X" and "the code on my branch still does X" are
different claims, and only one of them was measured. Quote the branch with
the count, always — the same discipline as quoting n with a floor.

# THE DELETION'S THIRD STEP OWES TWO ENUMERATIONS, AND A DELETED TYPE
# TAKES ITS TESTS WITH IT

f3aa1d0b (role @980dc16c), 2026-08-18, ruled BEFORE step 3 is cut, on
fd15d2a0's report that step 2 is the accessor swap and step 3 is the
retention cut. Step 3 needs no fresh ruling on WHETHER — Gluck already
ruled that nothing may pin evicted bytes. This is about what it owes on
the way out.

## FIRST, THE COUNT IN THIS DOCUMENT IS RIGHT, CHECKED RATHER THAN QUOTED

This plan says "five carried version fields" in four places. The struct
has EIGHT fields. Counted: `State` and `Form` are the payload; `Entries`
is a COUNT, not a version; and the five that carry a version are
`Fingerprint` (the encoder's), `LastLT`, `LastFormVersion`,
`LastStudyVersions` and `FormVersionOfSnapshot`. The phrase is exact. It
is recorded because it was checked, and because a repeated number in a
plan is the kind of thing that gets inherited eleven times and measured
never.

## ENUMERATION ONE: WHERE EACH CARRIED FACT LIVES AFTERWARDS

Every one of those five exists because something already went wrong
without it, and the struct says so in its own comments — "MUST survive a
warm start: without it the next pass asks for patches from 0 and
re-renders the whole board onto the first new message, WHICH THE PER-LT
CACHE THEN MAKES PERMANENT." That is a paid-for defect, written down at
the site.

SO STEP 3 STATES, FIELD BY FIELD, ONE OF EXACTLY TWO THINGS:
  - the fact now lives HERE (name the place), or
  - the fact is NO LONGER NEEDED, and here is what makes it unnecessary.

"The cursor handles it" is not one of the two. Neither is silence. A
warm-start defect is invisible on the pass that creates it and permanent
by the time it is seen, which is the worst possible combination for a
property that is dropped without anyone noticing it was ever held.

## ENUMERATION TWO: THE TESTS THAT NAME THE TYPE

Six test files construct `IncrementalProjection` directly. When the type
goes they will not compile, and the fastest way to make a package build
again is to delete them — at which point the properties they guard go
unguarded SILENTLY, in a commit whose diff reads as tidying.

    form_projection_test.go      cold-equals-warm; every patch renders
                                 exactly once across resumes
    projection_test.go           suffix-only; fingerprint invalidation;
                                 no advance past an encode failure
    projection_reads_test.go     the READ COUNTERS — cached records cost
                                 no reads; reads scale with misses
    study_projection_test.go     the studied-form half of every rule above
    watermark_stale_test.go      warm start stale by one message
    observation_bench_test.go    the cold/warm observation axis

EACH IS CLASSIFIED, IN THE COMMIT, AS EXACTLY ONE OF:
  (a) A TEST OF THE MECHANISM. It asserts something about the projection's
      own bookkeeping and has no meaning without it. Dies with it, and the
      commit says so by name.
  (b) A TEST OF A SYSTEM PROPERTY. The property outlives the machinery and
      must be RE-POINTED at the replacement, still red-able. If the subject
      changed, THE NAME CHANGES — 9ed3f561 retired `memoLanded` rather than
      flipping it for exactly this reason, and the rename rule was priced
      tonight at 2,488 B and a 33% phantom win. A test kept under its old
      name over new machinery is the quiet half of that failure.

MY EXPECTATION, offered so it can be falsified rather than agreed with:
the read counters in projection_reads_test.go are (b) and are the single
most valuable thing in the six, because they are the only instrument that
can see the retention cut REGRESS — a cursor that pins a span reads like
a correct projection and costs like the old one. If step 3 lands with
those counters deleted, the stage's central claim becomes unfalsifiable on
the same day it is made.

## AND THE NUMBER ON LANDING, WITH ITS UNITS

fd15d2a0 already has this right and it is restated so it survives the
commit: the figure quoted is 200.2 MiB of ENCODED NATIVES AT REST, a LOWER
BOUND from a file walk. The decoded figure is a heap measurement and has
not been taken (OPEN-projection-heap.md, refused tonight because we are
the load on Gluck's desktop, not because it is doubted). Nobody quotes a
decoded number, and nobody quotes acceptAssistantProjection, which piece A
already banked.

## AND A STANDING RULING ON EVIDENCE: A GATE LOG THAT DOES NOT NAME ITS
## TREE IS INADMISSIBLE

f3aa1d0b (role @980dc16c), 2026-08-18, on 9ed3f561's audit of the
executor's gate — an audit it asked for on itself, having caught one stale
gate claim earlier the same evening.

THE AUDIT'S VERDICT WAS THAT THE GATE IS SOUND: at da6a47a7 the log
carries section markers, so the claim is CHECKABLE rather than trustable —
43 ok lines and zero FAIL inside the full-gate section, the seven FAILs
all above it in the canary sections exactly as their author described them
BEFORE anyone looked, four RC fields at zero, and a GATE_DONE completion
sentinel that nobody asked for.

THE DEFECT IS UNIVERSAL AND CHEAP. Not one of the EIGHT gate logs on this
box records the commit it ran at. One of them was already found green for
a tree committed 48 minutes after it ran, quoted in support of that later
tree. Same shape as the two head-BEFORE files this campaign refused for
carrying no commit.

    A GREEN LOG FOR AN UNNAMED TREE IS NOT WRONG. IT IS UNFALSIFIABLE,
    WHICH IS WORSE, BECAUSE IT AGREES WITH WHATEVER THE READER ALREADY
    BELIEVES.

RULED, retroactively and going forward: an unstamped gate log may not be
cited. STATED PLAINLY SO IT IS NOT OVERQUOTED — this does not make
tonight's claims false. Every claim in this document rests on
reproductions, canaries and re-verifications, and not on those logs; what
changes is that the logs stop counting as a SECOND witness they were never
entitled to be. The next full gate runs stamped and supersedes.

THE FIX IS FOUR LINES AND ALREADY WRITTEN ELSEWHERE: HEAD, dirty count, go
version and loadavg, before any test output, borrowed from
scripts/measure/stamp.sh rather than reimplemented. And it gets a CHECKER,
because this campaign's best moves turn a rule into a shape: something
reads the log and REFUSES it unstamped, refuses a token that is not an
object in the database, and refuses a stamp naming a different tree than
the claim. The eight existing logs are a red corpus that already exists in
the wild — a canary that need only be RUN.

VALIDATE THE TOKEN, NEVER PATTERN-MATCH IT. The auditor's own first pass
reported three commit stamps that were not commits: two non-objects and
2097152, which is 2 MiB, the segment size printed inside an error string.
Caught on itself, before it reached me. Sixth costume of the same family
in one evening, and the first one worn by the auditor.

## THE FORK IN STEP 2, RULED: THE SIGNATURE COULD NOT EXPRESS THE MAPPING

f3aa1d0b (role @980dc16c), 2026-08-18, on fd15d2a0's fork, brought BEFORE
any line of projection.go moved.

THE FORK AS POSED. (A) put the board on the accessor —
`provider.Form` gains `SnapshotAt(version)`, and `store.EntriesWithState`
is then called by nobody and should be deleted as speculative. (B) put the
board on the iterator — the projection walks with `EntriesWithState`,
which is the shape Part II ruled canonical. They conflict: under B the
accessor method is redundant, under A the Seq2 is dead.

RULED: B, WITH ITS SIGNATURE CORRECTED, AND `foldedIndex` DELETED.

THE REASON, and it is the question this campaign's two falsified rulings
both skipped — WHAT DOES EACH PART ANSWER ABOUT:

    foldedIndex func(Entry[T]) uint64

says THE FOLDED INDEX IS A FUNCTION OF THE ENTRY. IT IS NOT. The index
wanted is the PREVIOUS entry's FormChannelVersion — a function of the
ITERATION'S POSITION. fd15d2a0 found the symptom exactly (the closure must
become stateful, resting on a call-order invariant `EntriesWithState` does
not state and holds only by accident of writing) and proposed to pin it
with a test. THAT TEST WOULD HAVE PINNED A WORKAROUND. The cause is one
level up: a signature that cannot express the mapping is satisfied by
smuggling the truth into a closure.

So the accessor takes the index:

    iter.Seq2[Entry[T], func(idx uint64) (S, error)]

The mapping becomes visible at the call site — `get(prevFormVersion)` on
the IR path, `get(e.LT)` for a form log iterating itself. KEPT: the memo,
its counts, the one-segment bound, and all three accessor rules including
"valid for its own step", whose step check is untouched. DELETED: a
stateful closure, an unstated invariant, and the test that would have
guarded it. COST: one signature change on an API with ZERO CALLERS — one
commit now, nothing later.

AND THE THIRD ARGUMENT AGAINST A, CHECKED RATHER THAN ARGUED: `provider.Form`
has TWO implementations, `formView` and `librettoView` (agent.go:1198,
1141), and the studied-form half uses `PatchesBetween` ONLY
(projection.go:201). Under A, `librettoView` implements `SnapshotAt` to
satisfy a type, for a caller that does not exist — fd15d2a0's own "two
doors justified by a caller who does not exist", arriving once per
implementation.

RECORDED BECAUSE IT IS A PATTERN NOW: the executor recommended B and said
it was not confident enough to act alone. It was right about the
destination and right to doubt the vehicle — the discomfort it could not
name WAS the signature. Second time tonight a worker's unease located a
defect its argument had not yet reached.

## AND THE GATE STAMP, IMPROVED BY THE ARM THAT WAS ORDERED TO BUILD IT

9ed3f561, adopted by f3aa1d0b the same hour. My ruling above specified a
stamp at gate start. INSUFFICIENT: a full gate runs for minutes, and an
executor who edits during it produces a log that is STAMPED, GREEN AND
LYING — the failure the stamp exists to prevent, wearing the stamp's own
clothes.

    THE STAMP IS WRITTEN TWICE, BEGIN AND END, AND THEY MUST AGREE ON HEAD
    AND ON THE DIRTY COUNT. A tree that moved mid-run is REFUSED, not
    believed.

AND: INADMISSIBLE AS EVIDENCE IS NOT USELESS. The eight unstamped logs are
KEPT. They are how the arm located which canary produced which red line,
and they are the only red corpus we have — which paid for itself
immediately by being refused eight times out of eight, each refusal clause
carrying its own canary. What they may not do is support a claim about a
tree.

THE SCRIPTS LIVE IN THE REPO, TRACKED, authored by the measurement arm and
RUN by the executor, who may not edit them. Not because scratch is untidy:
BECAUSE AN INSTRUMENT IN THE TREE IT GATES STAMPS ITSELF. The log records
HEAD and the dirty count of the tree under test, so a changed gate script
changes them and the log says so. Invoked from /var/tmp, the log names the
tree under test but NOT THE VERSION OF THE INSTRUMENT THAT PRODUCED IT —
an unstamped gate one level up. And /var/tmp is what the cleanup contract
deletes, which would leave the next bearer an instrument that was never
committed and no record that it existed.

## CORRECTION, IN ITS OWN PARAGRAPH: THE READ COUNTERS DO NOT SEE RETENTION,
## AND THE MISTAKE WAS MINE

f3aa1d0b, 2026-08-18, on 9ed3f561's falsification of my own ruling above.
The section "THE DELETION'S THIRD STEP OWES TWO ENUMERATIONS" states, as a
falsifiable expectation, that projection_reads_test.go's read counters are
THE ONLY INSTRUMENT THAT CAN SEE THE RETENTION CUT REGRESS. THEY ARE NOT.
They cannot see it at all.

MEASURED, not argued: the same projection run twice over identical inputs,
once through an honest log and once through a log that RETAINS a reference
to every entry it hands out — the regression made concrete.

    HONEST  log   calls=11   spanned=200
    PINNING log   calls=11   spanned=200   AND 200 ENTRIES RETAINED

Identical on both counters while two hundred entries are pinned.

WHY, MECHANICALLY: `countingForm` wraps the FORM ACCESSOR and counts
`PatchesBetween` calls and the versions they span; the log in that fixture
is an uninstrumented `MemLog`. Retention lives on the ENTRY side and AFTER
the read. A read counter answers HOW OFTEN and HOW WIDE. Retention is HOW
MUCH IS STILL HELD, and no number of reads expresses it.

MY SENTENCE — "a cursor that pins a span reads like a correct projection
and costs like the old one" — turned out to describe THE INSTRUMENT rather
than the danger. Same error as every other one in this campaign: I named a
mechanism without asking what its parts answer ABOUT.

THE CONCLUSION SURVIVES ON BETTER GROUND, AND THE COUNTERS ARE STILL KEPT.
Enumerated with the denominator, four shapes a retention cut can regress:

    1. entries pinned after the pass ....... NOT SEEN (200 retained, zero
                                             counter movement)
    2. returned patch slices held .......... NOT SEEN, and worse:
                                             PatchesBetween returns a
                                             SUBSLICE, so holding one pins
                                             the whole array with NO read
    3. a wider request per record .......... SEEN, by `spanned`. Theirs.
    4. segment/snapshot residency .......... NOT SEEN, different layer

One of four, and NOT the one the retention claim rests on. They are
load-bearing on a different axis than I claimed for them.

## SO STEP 3 OWES A RESIDENCY COUNT, AND THE ARGUMENT IS THE GATE LOG'S

RULED: the retention claim may not be MADE until something can falsify it.
A RESIDENCY COUNT TAKEN AFTER THE PASS — entries still held, snapshots
still resident — asserted as a NUMBER and canaried by a deliberately
pinning cursor. That canary now EXISTS and is committed, which is the
cheapest half already paid.

THIS BLOCKS THE CLAIM, NOT THE CODE. Step 3 may be written, gated and
merged; what may not happen is a landing report that says the projection
no longer pins evicted bytes with nothing in the tree able to notice if it
does.

AND THE ARGUMENT IS ONE I MADE AGAINST MYSELF THREE MESSAGES EARLIER, HANDED
BACK: an unstamped gate log is inadmissible because a green nobody can
falsify agrees with whoever reads it. AN UNFALSIFIABLE RETENTION CLAIM IS
THE SAME OBJECT. Gluck's ruling that nothing may pin evicted bytes is the
whole reason the projection dies; a stage that deletes it and cannot show
the property holds has swapped one unmeasured belief for another.

NOTE THE SIZE QUESTION IS SEPARATE AND STAYS OPEN: OPEN-projection-heap.md
asks HOW MUCH, in bytes, and is refused tonight at load. This is the SHAPE
question — is anything still held — and it is countable, cheap, and lands
FIRST.

## THE TYPED FUNNEL, SHAPE REPORT: THE COUNT WAS THREE AND IS FIVE, AND THE
## DOOR THE FUNNEL WOULD CLOSE IS NOT THE DOOR THAT IS OPEN

Reconnaissance by 6ec565b5 at my order, 2026-08-18; ruled and recorded by
f3aa1d0b (role @980dc16c). READING ONLY — nothing built, and the
consolidation itself remains Gluck's to endorse.

THE SECTION ABOVE ("THE OPEN QUESTION: CAN THIS BE A SHAPE INSTEAD OF A
RULE") names THREE byte-level doors. There are FIVE appends behind THREE
reachable entry points, and the undercount is in a named place: the plan
says "writeBirth" and "the stump path" as though the stump path were one
thing. It is TWO — a FormLog implementation AND a second birth writer,
`writeStumpBirth` (xwal_store.go:838) — and only the first sits behind the
proposed funnel.

    form.go:666          xwalFormLog.AppendPatch   the aria's board
    topology_form.go:51  stumpFormLog.AppendPatch  topology AND every
                                                   libretto
    xwal_store.go:640    writeBirth
    xwal_store.go:838    writeStumpBirth           THE ONE THE PLAN MISSED
    form.go:618          MemFormLog.AppendPatch    memory only, no channel

THE INVARIANT HOLDS TODAY AT EVERY SITE, BY CONSTRUCTION. `FormLog` takes
`[]byte` and has exactly ONE caller in the tree — form.go:551 in
`Form.reduceOne` — which marshals a typed `message.Patch` itself, atomically
with the append. Both birth writers bypass `Form` entirely and also
`json.Marshal` a typed patch on the line above. So no site puts hand-built
bytes on the form channel today.

AND THE FINDING THAT MATTERS: A TYPED FUNNEL INSIDE PACKAGE store CANNOT
CLOSE THE DOOR THAT IS ACTUALLY OPEN. `XwalStore.OpenNode(id)` is EXPORTED
and returns figwal's own exported `*xwal.XWAL`, whose `Append(channel
string, key uint64, payload []byte)` takes the channel as A PLAIN STRING.
Any package in this module can obtain a handle and append arbitrary bytes
to the channel named "form" in one line — bypassing json.Marshal AND
Trunks' poison and dirty bookkeeping. The funnel cannot type that away,
because the byte door belongs to THE DEPENDENCY'S exported API.

WHAT KEEPS IT SHUT IS A FACT ABOUT THE TREE, NOT ABOUT THE TYPES: only
internal/store imports figwal's xwal, with one read-only exception
(internal/cli/angelus_client.go, `xwal.NeedsFlatten`), and OpenNode's 7
callers are ALL in package store — 4 production, 3 tests.

    IMPORT DISCIPLINE IS A RULE. THE COMPILER IS A SHAPE. This campaign's
    two best moves both converted the first into the second.

THE CHURN, COUNTED RATHER THAN ESTIMATED: the funnel proper is 1 interface
declaration, 3 implementations, 1 caller which DELETES its own marshal, 0
test call sites, 0 callers outside package store = FIVE EDITED SITES. The
two birth writers add a helper and 2 call sites = THREE MORE. TOTAL EIGHT.
Unexporting OpenNode is 7 more, all in-package and mechanical. No test
rewrites and no external API change in either half. One design caveat, not
churn: both birth writers key the patch to a main LT, so a funnel with no
key parameter leaves those two on a raw path.

## RULED: THE TWO HALVES ARE INDEPENDENT AND THEY WERE BUNDLED BY ACCIDENT

The visibility half — unexporting OpenNode — is NOT part of the
consolidation and does not wait on it. It stands whatever Gluck decides
about the funnel, it is 7 in-package call sites, and IT IS THE HALF THAT
CLOSES THE DOOR THAT IS ACTUALLY OPEN. The typed funnel makes hand-built
bytes unstatable at 8 sites INSIDE package store; the unexport makes the
raw handle unreachable from OUTSIDE it. Only the second addresses the hole
this report found.

AND THE CHANGE IS ITS OWN INSTRUMENT, which is why it needs no hazard test:
this campaign's standard is that a hazard test must be proven to reach by
FAILING TO COMPILE. Unexporting is that proof directly — any caller outside
package store stops compiling, today and forever, with no test to rot.

THE FUNNEL ITSELF REMAINS WITH GLUCK, unchanged and unhurried, and it is now
a better-posed question than when it was sent to him: it buys unstatability
at 8 sites for 8 sites of churn, against an invariant that currently holds
at every site anyway. That is insurance, priced — the same honest shape as
the fold bound, and it should be endorsed or declined on that basis rather
than on the assumption that it is closing a hole. The hole is elsewhere and
costs 7 sites.

## THE RESIDENCY INSTRUMENT EXISTS, AND A CLAIM MAY REACH EXACTLY AS FAR AS
## ITS INSTRUMENT DOES

9ed3f561, ruled and recorded by f3aa1d0b, 2026-08-18. The requirement two
sections up is now SATISFIED FOR SHAPE 1 and explicitly not for the rest.

    HONEST  pass     0 of 200 tracked objects still held after collection
    PINNING pass   200 of 200 still held

Reachability proven 0 -> 200, and the instrument FAILS LOUDLY if both
agree rather than reporting a comfortable zero — an instrument that says
"nothing is held" must first be shown able to say "something is held".
Mechanism: a finalizer per tracked object, the test's own reference
dropped, THREE GC cycles rather than one (a finalizer becomes runnable in
the cycle that finds the object unreachable and RUNS afterwards on another
goroutine, so one GC under-reports), and the pass run in its own frame so
the test does not measure its own stack. The GC is asked, rather than a
proxy inferred from — the same correction as validating a commit against
the object database instead of pattern-matching hex.

AND ITS FIRST VERSION REPORTED THE OPPOSITE, WHICH IS THE PART WORTH
KEEPING. It set the pinning wrapper to nil BEFORE forcing collection, so
the simulated leak became unreachable and both passes reported zero: A
NULL ABOUT A RETENTION IT HAD JUST DESTROYED. Stable, reproducible, false,
and pointed at withdrawing a requirement that is in fact satisfiable. The
fix is the finding: A REAL LEAK IS HELD BY SOMETHING THAT OUTLIVES THE
PASS — a cursor, a returned projection, a cache — and dropping the holder
before measuring is not conservatism, it is measuring a different scenario
and calling it this one.

SHAPE 2 IS OPEN, WITH ITS OBSTACLE NAMED AND ITS CLAIM REFUSED. A finalizer
attaches to the START of an allocation, and a retained PATCH SUBSLICE keeps
the BACKING ARRAY alive while any sentinel pointer becomes unreachable
independently — so the naive version reports "collected" for an array that
is still pinned. THAT IS A FALSE NEGATIVE, THE WORST DIRECTION, and it was
refused as coverage rather than shipped as it. Measurable via a finalizer
on the array's first element, but that is a claim about Go's allocator that
has not been canaried, and an uncanaried mechanism is not coverage.

## SO THE RULING SHARPENS: THE CLAIM'S SCOPE IS THE INSTRUMENT'S REACH

When step 3 lands, the retention claim may be made EXACTLY AS FAR AS shape
1 goes and no further:

    SAYABLE      no ENTRY handed out by the log is still held after the
                 pass, counted, canaried at 0 -> 200
    NOT SAYABLE  "nothing pins evicted bytes", full stop. Patch subslices
                 (shape 2) and segment/snapshot residency (shape 4) are
                 uninstrumented, and shape 2's naive instrument fails in
                 the direction that would flatter us.

This is the campaign's own thesis applied to CLAIMS rather than to
instruments: an instrument answers about something narrower than the
question, so A CLAIM QUOTED WIDER THAN ITS INSTRUMENT IS THE SAME DEFECT
WITH THE SIGN FLIPPED. The honest landing note names the shape it proved
and the shapes it did not, and that note is worth more than the wider
sentence would have been.

AND THE GATE RULE PASSED ITS FIRST REAL USE, on someone else's work, within
the hour of being made: fd15d2a0's step-2 log at 70d6dbf7 is THE FIRST
ADMISSIBLE GATE LOG IN THIS CAMPAIGN — stamped, terminated, tree named,
matching the claim it was offered for, 43 ok, 0 FAIL, checked by
checkgate.sh --expect rather than by reading. A rule became a shape and the
shape was then used to judge a third party.

## AND MY OWN GATE RULE ANSWERS NARROWER THAN ITS QUESTION: A DIRTY STAMP
## FLAGS A TREE IT CANNOT IDENTIFY

f3aa1d0b, 2026-08-18, found while merging a gate log that obeys the rule
perfectly.

The rule is that a gate log must NAME ITS TREE, stamped BEGIN and END,
which must agree on HEAD and on the DIRTY COUNT. 6ec565b5's log obeys it:

    BEGIN tree=f73743c0 dirty=8      END tree=f73743c0 dirty=8

Both stamps agree, and the honest reading was given — the dirt IS the
commit, because a gate run before the commit exists can be nothing else.
NOTHING IS WRONG WITH THAT RUN. What is wrong is my rule.

    A DIRTY COUNT IS A SCALAR. IT SAYS A TREE IS NOT THE COMMIT; IT DOES
    NOT SAY WHICH TREE IT IS. Two entirely different sets of eight edits
    stamp identically, so a dirty gate is REPRODUCIBLE ONLY BY TRUST.

This is the same defect I ruled inadmissible for unstamped logs, one notch
in: the log answers about the PARENT commit, and the tree under test is
the parent plus something unrecorded. It answered correctly, and about
something narrower than the question — this campaign's sentence, now
attached to a rule I made two hours ago.

THE FIX IS CHEAP AND IS THE SAME MOVE AS THE ORIGINAL: when the dirty
count is non-zero the stamp carries a DIGEST OF THE DIFF (`git diff HEAD |
sha256sum`, or the tree id from `git stash create`), so the tested tree is
IDENTIFIED rather than merely flagged, and BEGIN and END compare digests
instead of counts — which also catches an edit that changes a file without
changing the count, the exact hole a scalar leaves open.

PREFERENCE, NOT REQUIREMENT: gate the COMMITTED tree where the work allows
it, and the digest never has to be read. It was not available here — the
gate necessarily ran before the commit existed — which is precisely why
the digest is the rule rather than the preference.

RETROSPECTIVE SCOPE, stated so it is not overquoted: this does NOT withdraw
6ec565b5's gate. Its result is corroborated by the compiler proof, by the
merge building, and by the change being a mechanical unexport with seven
in-package call sites. What it withdraws is the belief that a dirty stamp
makes a run reproducible by anyone but its author.

## THE DIGEST, AND A HOLE IN MY SPECIFICATION THAT THE BUILDER CLOSED

9ed3f561, recorded by f3aa1d0b, 2026-08-18. The digest rule two sections
up is built and canaried FOUR ways, all firing: a clean run stays
admissible; a dirty run records its digest; A MID-RUN EDIT AT AN UNCHANGED
DIRTY COUNT IS REFUSED (the case the scalar could not see, which is the
whole point); and a dirty log with its digest stripped is refused.

AND THE DIGEST COVERS UNTRACKED FILE CONTENTS, WHICH MY SPEC DID NOT SAY.
I specified `git diff HEAD | sha256sum`. `git diff HEAD` CANNOT SEE
UNTRACKED FILES AT ALL, while a dirty count from `status --porcelain`
happily counts them — so my digest would have been blind to exactly the
files most likely to be a scratch canary or a probe, and blind in the
direction that keeps a log admissible. The builder added status, diff and
untracked CONTENTS, and canaried that claim SEPARATELY rather than
asserting it in a comment: an untracked file edited at an unchanged dirty
count moves the digest. A comment making a claim its test does not check is
a claim nobody will ever verify.

BACKWARD COMPATIBILITY CHECKED RATHER THAN ASSUMED: the campaign's first
admissible log is still admissible, because the digest is required only
when dirty is non-zero.

## AND A STANDING RULE FOR CROSS-ARIA HANDOFF: CHECK THE ARTIFACT AGAINST
## THE RECIPIENT'S HEAD

`git format-patch` on that commit EMITTED THE SAME DIFF TWICE — 251 lines
where `git show` gives 130, one tree entry per path, no format.* config set,
and earlier patches from the same worktree exported correctly. UNDIAGNOSED,
and deliberately so: a patch carrying a hunk twice cannot apply, so it
would have failed in the executor's hands and read as a CONFLICT WITH ITS
WORK rather than as a defect in the sender's export.

The builder refused to ship it, switched to fetch-and-cherry-pick, and
verified THAT mechanism end to end — applies at the recipient's head,
authorship preserved, scripts parse, checker still green.

    RULED: EVERY CROSS-ARIA CODE HANDOFF IS `git apply --check` (or a
    cherry-pick into a scratch worktree) AGAINST THE RECIPIENT'S ACTUAL
    HEAD BEFORE IT IS SENT. Not the sender's head. Not "it exported
    cleanly".

WE DO NOT NEED THE DIAGNOSIS, WHICH IS WHY THE RULE IS THE ANSWER: a
cheap universal check makes the cause irrelevant, and chasing git's
internals would cost more than it buys. The anomaly is recorded here with
its symptoms so that whoever meets it again starts from a sighting rather
than from disbelief.

THE FAMILY THIS BELONGS TO, and it is the third instance in one evening:
A COMMAND THAT SUCCEEDED IS NOT AN ARTIFACT THAT IS CORRECT. Tonight: a
cherry-pick that silently did nothing, a patch naming the wrong commits,
and a patch file duplicating its own hunks. IN ALL THREE THE COMMAND
EXITED ZERO. It is the status-versus-artifact rule one level out — there
the status field lied about the work, here the exit code told the truth
about a process that produced a wrong thing.

## THE RESIDENCY COUNT MUST BE RUN AGAINST TODAY'S CODE, BEFORE THE DELETION

f3aa1d0b, 2026-08-18, on learning the instrument is written, rehearsed and
NOT YET IN THE TREE while step 3 is being cut.

I had ruled only that it must exist before the landing NOTE. THAT IS TOO
WEAK, and the reason is the campaign's own red-first standard applied to a
property rather than to a hazard.

THE PROJECTION HOLDS SLICES RETURNED BY THE LOG. That is stated in Part II
as the reason it must go — "because it holds slices returned by the log, it
also PINS payloads the log's window believes it evicted" — and it is the
whole ground of Gluck's ruling that nothing may pin evicted bytes. SO THE
RESIDENCY COUNT, RUN AGAINST TODAY'S CODE, SHOULD READ NON-ZERO. It is not
merely a guard for afterwards; it is the only chance this campaign has to
MEASURE THE DEFECT IT IS DELETING.

    RULED: the instrument lands and is RUN against the pre-deletion tree,
    and the number it reads there is RECORDED, before step 3 commits.

AND THE OUTCOME THAT WOULD BE MOST VALUABLE IS THE INCONVENIENT ONE: IF IT
READS ZERO TODAY, THE INSTRUMENT DOES NOT REACH THE DEFECT WE ARE DELETING
— and we would learn that before the claim rather than after, which is
exactly the failure mode this campaign has documented sixteen times. A
before-number of zero is a finding about the instrument, not a
disappointment about the code.

TIMING, so it does not disrupt work in flight: the executor applies it AT
ITS NEXT CLEAN BOUNDARY, not into a tree with fourteen dirty files. The
application is already rehearsed at the recipient's exact head under the
new handoff rule, cherry-picked and RUN green in a scratch worktree, so the
command is known-good before it is typed.

## AND A SCOPE LIMIT ON THAT INSTRUMENT, VOLUNTEERED BEFORE IT WAS ASKED FOR

The fixture's log is a `MemLog`, WHICH NEVER EVICTS, and the test drops the
store before measuring. So it answers: DOES ANYTHING OUTSIDE THE STORE
STILL HOLD ENTRIES AFTER THE PASS. That is the right question for "the
projection retains nothing of its own".

IT IS NOT THE QUESTION "THE CURSOR DOES NOT PREVENT EVICTION". An
implementation holding a reference THE STORE ALSO HOLDS reads as zero here
and would still pin bytes a real `cachedLog` wanted to evict on its window
and budget.

    SO THE LANDING NOTE MAY SAY: no entry handed out by the log is still
    held after the pass, counted and canaried at 0 -> 200.
    IT MAY NOT SAY: the cursor cannot prevent eviction.

The second needs eviction to actually HAPPEN in the fixture — the same
fixture driven through a `cachedLog` with a small window, asserting the
resident count falls. OPEN, with the obstacle named, beside shape 2. Not
taken tonight.

THIS IS THE THIRD LIMIT ITS BUILDER HAS VOLUNTEERED RATHER THAN BEEN CAUGHT
AT, and it is worth naming as a practice: an instrument's author reporting
what it CANNOT see, unprompted, is the cheapest possible version of every
lesson in ~/notes/figaro/instrument-not-reaching-the-code.md. Sixteen
instances in that note were bought at the price of a wrong claim first.

## CORRECTION, WITHIN THE HOUR: THE BEFORE-NUMBER IS ZERO AND IT IS ABOUT
## THE WRONG OBJECT — AND THE SHAPE QUESTION IS THE SIZE QUESTION

9ed3f561, breaking a stand-by order to stop a run being spent on a number
it already had; ruled and recorded by f3aa1d0b, 2026-08-18. This CORRECTS
the section immediately above, which ordered the residency count run
against today's code and its before-number recorded.

IT WAS RUN. IT READS ZERO. And two obvious explanations were tested before
the zero was reported rather than after:

    keep the returned projection, as the daemon does ......... STILL ZERO
    State = accumulated encoded payloads, not a counter ...... STILL ZERO

AND THE SECOND PROBE WAS UNSOUND IN THE FLATTERING DIRECTION, caught by
its own author before it was sent: it attached a finalizer to a `*[]byte`
and handed the projection a `json.RawMessage` SHARING THE BACKING ARRAY,
so the pointer died immediately while the array lived and the object was
counted as COLLECTED. That is the exact false negative refused for shape 2
an hour earlier, rebuilt in a different corner by the person who refused
it. Had its output been sent, this document would now record "the
instrument does not reach the defect" as a MEASURED FINDING, in the
direction that would have made me withdraw a requirement.

THE HONEST STATE OF THE INSTRUMENT:

    IT MEASURES       entries handed out by the log still reachable after
                      the pass. Sound, canaried 0 -> 200, because the
                      pinning log retains THE ENTRY STRUCTS, which are the
                      tracked objects.
    IT DOES NOT       the projection's retention of ENCODED OUTPUT.
                      Entries and encoded payloads are DIFFERENT OBJECTS
                      and only the first is tracked.

    A ZERO SAYS THE PROJECTION DOES NOT HOLD THE LOG'S ENTRY STRUCTS. IT
    DOES NOT SAY THE PROJECTION HOLDS NO BYTES.

SO THE BEFORE/AFTER FRAMING IS WITHDRAWN, and "0 before, 0 after" MUST NOT
BE WRITTEN ANYWHERE: that pair reads as "there was never any retention" to
every future reader, and it would be quoted that way. The series still
lands, because shape 1 is a real property worth guarding.

## AND THE TWO OPEN ITEMS COLLAPSE INTO ONE

A FINALIZER CANNOT CHEAPLY TRACK A SLICE'S BACKING ARRAY. That obstacle
was named for shape 2 (retained patch subslices) and it turns out to block
the encoded-output question too. ONE OBSTACLE, NOT TWO.

What measures it is a HEAP DELTA, not a finalizer count: allocate large
encoded payloads, force collection, compare HeapAlloc with the projection
KEPT versus DROPPED. THAT IS EXACTLY OPEN-projection-heap.md, already
written up and dated-refused.

    THE SHAPE QUESTION AND THE SIZE QUESTION ARE ONE QUESTION WEARING TWO
    COATS.

CONSEQUENCE, AND IT RAISES THE PRIORITY: the heap measurement was filed as
the nice-to-have decoded figure behind the 200.2 MiB lower bound. IT IS
NOT. It is THE ONLY INSTRUMENT for the property this whole stage is
justified by — Gluck's ruling that nothing may pin evicted bytes. Until it
is taken, the deletion lands with its central property UNWITNESSED, which
is honest and must be said in the landing note rather than papered over.

THE QUIET-BOX TICKET THEREFORE DISPATCHES THE HEAP MEASUREMENT FIRST, and
now for a stronger reason than "it is the number people will want".

## STEP 3'S UNPRICED COST: DELETING `Previous` DELETES THE WARM START

f3aa1d0b, 2026-08-18, ruled at the step-2 boundary from the executor's own
field enumeration, BEFORE step 3 is cut.

Two lines of that enumeration, read together, say something neither says
alone:

    LastLT / Entries   "the watermark and its validity check ... with no
                       warm start there is nothing to validate"
    State              "the encoded natives. REBUILT EACH PASS from the
                       translation cache by LT"

    THEREFORE EVERY TURN BECOMES A COLD WALK OVER THE WHOLE CONVERSATION.

`ProjectIncrementally`'s own doc says avoiding that is why it exists: "It
READS only the suffix too, which it did not used to: the whole log was
materialized and then sliced, so a warm pass that touched three messages
still required all N to be decoded and resident." Deleting `Previous`
turns `TailAfter(watermark)` back into `TailAfter(0)`.

THIS IS NOT A VETO, and it is important that it is not. Gluck's standing
instruction on this design is that a regression is REPORTED AND TUNED,
never treated as a veto, and the design reason is his ruling: nothing may
pin evicted bytes, and if a caller needs a record it reads it. WHAT MAY NOT
HAPPEN is the cost being discovered after landing by someone whose turn got
slower.

PRE-REGISTERED, BEFORE THE CUT:

  a. DO THE EXISTING INSTRUMENTS REACH? BenchmarkColdWalkWarmCache, its
     8Observed variant, ColdWalkColdCache and the
     BenchmarkObservationWarm{0,1,8,50} axis were built for this exact
     question. If they take a `Previous` and the field is gone, they
     measure a shape that no longer exists — THEIR SUBJECT CHANGED, so
     their NAMES change. Same rule that retired `memoLanded` and
     `InterruptRepair10000`.
  b. THE PREDICTION IS WRITTEN FIRST, WITH FALSIFIERS. Mine, offered to be
     beaten: per-turn cost rises with conversation LENGTH rather than with
     new records, and the rise is dominated by CACHE LOOKUPS — one read per
     record — rather than by decode, since the cached path skips derivation.
  c. THE MEASUREMENT IS THE ARM'S. The executor does not grade its own cut.
  d. IF IT REGRESSES IT GOES IN THE LANDING NOTE. The ledger's own sentence
     applies to costs as much as to benefits: a stage whose cost is written
     down honestly before it ships cannot be re-justified afterwards by
     whoever needs it to have been worth it.

AND THE RULING IS ITSELF UNVERIFIED, which is stated here rather than
discovered later: it is read off an enumeration and a doc comment, not off
a measurement. If the cache lookup is O(1) in a resident map, or the warm
start is already dead on this branch, or the walk is bounded by something
unread, the executor takes it and says so in the first line.

## AND A REHEARSAL IS A LIVENESS CLAIM

The cross-aria handoff rule — apply-check against the RECIPIENT'S head
before sending — was obeyed exactly, and the residency fixtures still
landed RED: the executor's next commit made `Form` set with `Boards` nil an
ERROR rather than a default, and the fixtures used the only shape that was
legal when they were written. Its own sentence: the check "verified
everything it could — right up until my head moved underneath it."

    A REHEARSAL IS A LIVENESS CLAIM AND LIVENESS CLAIMS EXPIRE. THE SENDER
    STAMPS THE HEAD IT REHEARSED AT; THE RECIPIENT COMPARES THAT STAMP TO
    ITS OWN HEAD BEFORE APPLYING AND RE-REHEARSES IF IT MOVED.

Same shape as the gate stamp one level over: not "it was verified" but "it
was verified AT THIS TREE", so the recipient can tell whether the
verification still applies instead of learning it from a red test.

AND THE EXECUTOR DID NOT REPAIR THE INSTRUMENT THAT GRADES IT, which is the
separation working: it sent the cause, the one-field fix and a fixture to
copy the shape from, and told the arm to CHECK rather than accept that the
change is mechanical — because if supplying a board source moves the
honest/pinning numbers at all, that is a finding that outranks the repair.

# STEP 3 SPLIT IN TWO, AND THE ORDER IS THE RULING

f3aa1d0b, 2026-08-18, on fd15d2a0's finding, made by auditing a claim it
had already sent me rather than by building on it.

THE DEFECT STEP 3 WOULD HAVE CREATED. `xwalLog.Lookup` falls back to A
FULL LINEAR SCAN OF THE CHANNEL when the index returns not-found — and it
fires on ANY not-found, including a legitimate cache miss, scanning the
whole channel to confirm the miss. It is guarded by a comment
(xwal_log.go:303) reading "the channel is small in practice AND THIS IS
OFF THE HOT PATH". True today, because only the new suffix is ever looked
up. FALSE THE MOMENT THE WARM START DIES: a fingerprint bump or a stale
cache then misses on ALL N records and each miss scans the whole channel.
O(N^2) ReadAt calls on the path every turn takes.

    A COMMENT THAT WAS TRUE WHEN WRITTEN, MADE FALSE BY A CHANGE THREE
    PACKAGES AWAY, WITH NOTHING IN THE TREE ABLE TO GO RED ABOUT IT. It is
    an unstamped gate log in prose: a claim about a context that has moved,
    which agrees with whoever reads it.

RULED — THE MERGE-JOIN IS CARRIED, AND IT IS CUT FIRST:

    COMMIT ONE  the merge-join. One forward walk over the IR log and one
                over the translation channel, joined on LT, NO Lookup at
                all. `Previous` still alive; nothing deleted.
    COMMIT TWO  the deletion.

THREE REASONS, and the third was a gift:
  1. THE EXPOSURE NEVER EXISTS — not shipped-and-fixed, not landed-and-named.
  2. EACH COMMIT IS ONE IDEA. Step 3 was already the ruling-sized half.
  3. IT MANUFACTURES A CLEAN MEASUREMENT BOUNDARY. The arm had refused to
     call its read-counter movement a before/after because THE FIXTURE HAD
     TO CHANGE across that boundary, confounding an API change with the
     field it added to make the test run — and said it would want to look
     for a boundary where the fixture does not change before promising one.
     Commit one changes no API and needs no fixture change. ORDERING
     COMMITS BY HAZARD PRODUCED A MEASURABLE SEAM FOR FREE.

DECLINED, WITH THE REASON ON THE RECORD: the executor offered to land the
deletion with the exposure NAMED in the commit and the landing note —
"worse engineering, better bookkeeping". Honest bookkeeping about a defect
we chose to ship is still shipping the defect. That call would have been
right had the fix been expensive or unknown; it is neither.

## WHAT COMMIT ONE MUST ASSERT, AND IT IS TWO PROPERTIES NOT ONE

    CARDINALITY   one LT may carry SEVERAL translation entries after a
                  fingerprint bump. A lookup plus the fingerprint refusal
                  picks the right one BY CONSTRUCTION; a forward merge must
                  pick it DELIBERATELY. Canary with a stale-fingerprint and
                  a current entry at the same LT, in both orders.
    GAPS          a merge join assumes both sides ASCENDING AND GAPLESS in
                  the joined key. The translation channel is SPARSE BY
                  DESIGN — only cached records are there — so it is gapped
                  by construction, and "no entry at this LT" must be a MISS
                  TO BE ENCODED, never a reason to advance the IR cursor. A
                  join that advances the wrong cursor DROPS A RECORD
                  SILENTLY and produces a projection every value oracle on
                  a gapless fixture calls correct. Canary with an INTERIOR
                  gap; a leading or trailing gap cannot see it.

The arm's grounds for raising it are concrete, not theoretical: its own
fixture assertion tonight found that figaro's form records START AT VERSION
2, NOT 1. An assumption about where a key sequence begins has already been
wrong once in this stage.

## AND MY PREDICTION WAS STATED IN THE WRONG UNITS

I pre-registered that per-turn cost rises with conversation LENGTH, to be
measured on BenchmarkObservation{Cold,Warm}{0,1,8,50}. THE ARM FALSIFIED
THE INSTRUMENT CHOICE BY READING: every one of those is FIXED AT FORTY
RECORDS and varies only the OBSERVER count. The axis the prediction lives
on is not in the fixture, so a null from it would have been vacuous —
exactly the kind retracted earlier tonight.

AND BOTH HALVES OF THE PREDICTION ARE COUNTABLE FACTS I DRESSED AS TIMES:

    "cost rises with LENGTH not with new records"  = entries walked/turn
    "dominated by cache lookups, not decode"       = lookups/turn, decodes/turn

Nothing there needs a nanosecond. Stating it as timing made it wait for a
quiet box it never needed and exposed it to a floor it never had to clear.
This campaign's own standing preference is that where the question is HOW
MANY TIMES you COUNT it, and I failed to apply it to my own prediction.

RULED: the COUNT instrument is built FIRST — before commit one — so one
instrument yields THREE points on one axis (today, after the join, after
the deletion) instead of two comparisons across boundaries where something
else also moved. The TIMING stays refused and dated, and THE COUNTS DECIDE
WHETHER IT IS WORTH A QUIET BOX AT ALL.

## THE WARM BENCHMARKS ARE RETIRED, NOT REPAIRED

`projectWith` sets `cfg.Previous` INSIDE the timed loop, so deleting the
field BREAKS THE BUILD LOUDLY — the hazard-test standard arriving for free.
BUT THE OBVIOUS REPAIR IS THE TRAP: the only thing making the Warm variants
warm IS `Previous`. Delete the assignment and
BenchmarkObservationWarm{0,1,8,50} become byte-identical in behaviour to
their Cold twins WHILE KEEPING NAMES THAT SAY WARM — four benchmarks
silently changing subject, the rename hazard in its purest form, priced
tonight at 2,488 B and a 33% phantom win. The arm ruled it; I upheld it
without amendment. A cold walk under a name saying Warm is worse than a
missing benchmark.

## THE GENERAL FORM, THREE INDEPENDENT SIGHTINGS IN ONE EVENING

9ed3f561's compression, recorded in its name because it is now a pattern
and not an analogy: AN UNSTAMPED LOG, AN UNDATED REFUSAL, AND AN UNSTAMPED
REHEARSAL are one defect.

    ALL THREE SAY "IT WAS VERIFIED" WHERE THE USEFUL SENTENCE IS "IT WAS
    VERIFIED AT THIS TREE, AT THIS TIME."

## THE COST, MEASURED IN COUNTS, TODAY, WITHOUT THE CHANGE EXISTING

9ed3f561, recorded by f3aa1d0b, 2026-08-18. Both halves of the
pre-registered prediction confirmed, on a box at load 20, with no floor to
clear and no quiet box to wait for — because the question was countable
all along.

    length     WARM handed/lookups/decodes     COLD handed/lookups/decodes
       50              1 / 1 / 1                     51 /  51 / 1
      100              1 / 1 / 1                    101 / 101 / 1
      200              1 / 1 / 1                    201 / 201 / 1
      400              1 / 1 / 1                    401 / 401 / 1

COLD GROWS LINEARLY IN LENGTH WHILE WARM IS FLAT AT ONE. And it is LOOKUPS
that grow, N+1 of them, while DECODES STAY AT EXACTLY 1 — the lookup axis
is precisely what the merge-join removes, and the decode axis was never the
cost.

THE COLD COLUMN IS NOT A CONTROL, IT IS THE REGRESSION. A pass with no
`Previous` IS the cold walk that deleting the warm start creates, so the
instrument measures the change before the change exists, using a shape the
code already supports. The canary is built into the table rather than
bolted beside it: an instrument that cannot see cold growing cannot see the
deletion, and the test FAILS SAYING SO rather than reporting a comfortable
null.

## SO THE TRADE, STATED PLAINLY BEFORE IT LANDS

    TODAY            one entry handed per turn, one lookup, one decode.
                     Bought with a retained projection that PINS what the
                     log's window believes it evicted.
    AFTER, WITH THE  N entries handed per turn, ZERO lookups, one decode.
    MERGE-JOIN       Bought with a walk.

THE DELETION TRADES O(1) PER-TURN WORK FOR O(N), AND BUYS BACK THE
RETENTION. That is not a regression discovered late; it is the trade Part
II rules FOR, in Gluck's own words — nothing may pin evicted bytes, and if
a caller needs a record it reads it. The merge-join removes the multiplier
on that walk; it does not remove the walk, and nothing can, because the
walk IS the alternative to holding the bytes.

WHAT MAY BE SAID WHEN IT LANDS: the per-turn cost goes from constant to
linear in conversation length, measured in counts, and the lookup term —
which is the one that would have gone quadratic through the linear-scan
fallback — is removed entirely. WHAT MAY NOT BE SAID: that the deletion is
free.

## TWO INSTRUMENT FAULTS DISCLOSED BEFORE THEY REACHED ME

FIRST: the first version reported THE WARM START ALREADY BROKEN — warm
growing 51 -> 401, which reads as "it already walks the conversation before
anything was deleted". Artifact: `store.TailAfter` has an OPTIONAL FAST
PATH, `MemLog` does not implement it, and the fallback calls `Read()` and
MATERIALISES THE WHOLE LOG before slicing. Real for MemLog, not necessarily
real for a cachedLog. Now counted as two quantities — MATERIALISED (what
the log produced) and HANDED (what the projection received) — and the cost
question is about the second.

SECOND, AND IT IS A NEW MECHANISM FOR THE INSTRUMENT NOTE: A COUNTING
WRAPPER THAT OVERRIDES `Read` BUT NOT `TailAfter` COUNTS THE WRONG THING —
and on a log that HAS the fast path, GO'S EMBEDDING PROMOTES THE INNER
METHOD AND BYPASSES THE OVERRIDE ENTIRELY. The counter would report ZERO
while the projection iterated thousands.

    EMBEDDING HANDS YOU A SILENT BYPASS FOR FREE. A wrapper is an
    instrument, and a promoted method is an instrument that does not reach
    the code — with the reader's own type system doing the concealing.

## AND THE TIMING QUESTION IS NOW DECIDABLE ON BETTER GROUNDS

The counts establish that the walk grows with length. So a timing run would
price it in nanoseconds and would NOT establish the shape, which is already
established. It stays refused tonight — and now for a stronger reason than
load: NOT NEEDED, rather than NOT AFFORDABLE. That is the better kind of
refusal and it is the one that should be inherited.

## THE LOOKUP FALLBACK: THE COMMENT NAMES A FUNCTION THAT NO LONGER EXISTS

6ec565b5's reconnaissance, ruled by f3aa1d0b, 2026-08-18. MY PREDICTION WAS
FALSIFIED IN ITS REASON AND CONFIRMED IN ITS SHAPE — the outcome is a
NARROWED fallback, reached by a different road than the one I predicted.

THE COMMENT IS FALSE AT THE PIN. `xwal_log.go:303` justifies the linear
scan by figwal's "mid-life-added channels have an empty FK on reopen".
`buildFK` exists in figwal v0.5.0 through v0.7.7 and is ABSENT FROM v0.7.8
ONWARD (checked across all 40 versions in the module cache). We pin
v0.18.1 on the seam branch and v0.18.0 on layered — eleven-plus minor
versions past the last release where that function existed. The fk is now
built LAZILY AND INCREMENTALLY, scanning backward from the tail and
memoizing as it goes, with three O(1) short-circuits; EMPTY-ON-REOPEN IS
NO LONGER A DEFECT, IT IS THE NORMAL INITIAL STATE OF A SELF-BUILDING
INDEX. And the specific hazard it feared — an index blind to records
inherited from a fork parent — is closed AND TESTED at the pin
(`ScanFromEnd` recurses into the parent chain; `TestScanFromEndAcrossFork`).

    A COMMENT CAN GO STALE BY A DEPENDENCY MOVING UNDER IT, WITH NOTHING
    IN OUR TREE CHANGING AT ALL. Nothing goes red. Nothing gets reviewed.
    The justification simply stops being true while the code it justifies
    keeps running.

AND THE EXPOSED CLASS IS NARROWER THAN ANYONE ASSUMED: the 1,218 form
channels NEVER REACH THIS PATH — they are unkeyed, they go through
Form/FormLog, and Lookup is not part of that interface. `xwalLog` is
constructed exactly twice, for the IR channel and the per-provider
translation channels, and ON THE MAIN CHANNEL FIGWAL NEVER CONSULTS THE FK
at all (an identity path, bounds-checked). Only the translation channels
are exposed.

## BUT IT IS STILL LOAD-BEARING, FOR A CASE NOBODY WROTE DOWN

figwal's fk scan ABORTS THE WHOLE LOOKUP with an error on the first frame
whose main-LT will not decode; our fallback SKIPS an unreadable record and
keeps scanning. SO ONE CORRUPT OR FOREIGN FRAME IN A TRANSLATION CHANNEL
MAKES FIGWAL'S LOOKUP RETURN AN ERROR FOR EVERY LT, and the fallback still
answers correctly for every other record. That is CORRUPTION TOLERANCE,
real and undocumented, and it is not what the comment claims.

RULED, and it is exactly the shape I predicted by a road I did not:

    THE FALLBACK FIRES ON figwal's ERROR, NOT ON ITS DEFINITIVE MISS.
    Today it fires on both — on `(false, nil)`, a definitive not-found
    after figwal's own memoized scan, and on `(_, err)`, the decode abort.
    ONLY THE SECOND IS A CASE FIGWAL CANNOT ANSWER. Narrowing drops the
    O(N) confirmation of a legitimate miss and keeps the only behaviour the
    fallback uniquely provides.

THREE OBLIGATIONS ON THAT CHANGE:
  1. THE COMMENT IS REPLACED BY WHAT IS TRUE — the corruption-tolerance
     reason, not the buildFK reason. A stale justification is not repaired
     by a correct code change beside it.
  2. THE ASSUMPTION IS PINNED BY A TEST. Narrowing bakes in "a definitive
     not-found from figwal is trustworthy". THAT IS A CLAIM ABOUT A
     DEPENDENCY WE BUMP, so it must fail loudly on a bump that breaks it —
     a fixture where a record exists and figwal reports it, canaried.
  3. THE PRICE OF BEING WRONG IS NAMED WHERE IT IS PAID: `lookupCached`
     treats not-found as "not cached" and re-encodes, so a wrong not-found
     costs a re-translation and a cache append (last wins) — COST AND
     CHURN, not a wrong rendering. The corrupt-frame case is the one that
     bites: without the fallback, one bad frame turns a cache into a
     PERMANENT MISS. Silent unbounded re-encoding, not corruption.

## AND THE RECON'S OWN NEAR-MISS, WHICH IS WHY IT IS TRUSTED

It was about to report that main-channel records carry `m=0`, making the
fallback structurally unable to match on main. `encodeStampedFrame` says
otherwise — main frames carry their own index as `m` — so the correct
reason is the bounds argument, not a missing field. CHECKED BEFORE WRITING
RATHER THAN AFTER. And the limits are stated in the same paragraph as the
findings: source read, nothing run, no fixture built, and the hot-handle
claim rests on a call path and a function name rather than on observed
eviction.

## THE COMPOSITION IS WHERE THE BUG LIVES: TWO CORRECT RULES, ONE WRONG SEAM

f3aa1d0b, 2026-08-18, reviewing the executor's written answers before the
join was cut. Both of its rules are correct as stated and VERIFIED against
source. Their composition is not.

    THE ADVANCE RULE   advance the cache cursor while FigaroLT < LT; if it
                       now SITS AT FigaroLT == LT, that is the candidate.
    THE CARDINALITY    take THE LAST entry at that LT, then apply the
    RULE               fingerprint refusal. (cached_log.go:225-232 —
                       sort.Search for the first FigaroLT > lt, MINUS ONE.
                       Last-wins, and the comment says so.)

    "SITS AT == LT" IS THE FIRST ENTRY OF A RUN, NOT THE LAST.

So the composition yields FIRST-MATCH at precisely the LT where the second
rule proves first-match wrong — and wrong in the direction that HITS a
stale-preceded current entry, which is the very case the executor's own
canary was written to catch. Neither rule is at fault; the seam between
them is.

AND THE CURSOR POSITION AFTER A RUN IS A SECOND, DISTINCT FAILURE: a join
that consumes duplicates but leaves the cursor off by one DROPS THE NEXT
LT ENTIRELY. That is the arm's silent record-drop arriving through
DUPLICATES rather than through GAPS — a different road to the same
unobservable defect.

BOTH CANARIES AS ORIGINALLY WRITTEN WERE BLIND TO IT, for a reason worth
generalising: they place the duplicate run at the LAST LT of the fixture,
with nothing after it to be dropped. A HAND-WRITTEN FIXTURE NATURALLY PUTS
THE INTERESTING CASE LAST, AND LAST IS EXACTLY WHERE AN OFF-BY-ONE IN A
FORWARD CURSOR CANNOT BE SEEN. The fixture must carry a record AFTER the
run and assert it is still found.

THE PATTERN, since this is the third time tonight one has generalised: an
INTERIOR case sees what an EDGE case cannot. Interior gap, not leading or
trailing. Interior duplicate run, not final. The edges are where fixtures
get written and where defects hide.

## PINS ARE NOT HAZARDS, AND ATTRIBUTION IS PER COMMIT

fd15d2a0's distinction, adopted by f3aa1d0b, 2026-08-18, at commit one's
boundary.

COMMIT ONE (the merge-join) landed with FIVE tests, of which ONE went red
before the code (the lookup count: 20, must be 0) and FOUR PASSED AGAINST
THE OLD CODE. Its author did not treat that as a weakness and was right:

    A HAZARD TEST guards a property the change could VIOLATE. It must be
    RED FIRST, and this campaign's standard is that it be proven to reach
    by failing before the code exists.
    A PIN guards a property the change must PRESERVE. GREEN FROM BIRTH IS
    THE CORRECT STATE, because the old code already has the property — and
    the only meaningful proof of its power is a CANARY.

Saying which is which IN THE COMMIT is the load-bearing part, so that
nobody later reads five green-from-birth tests as five vacuous ones. And
the canary table is the model, because DIFFERENT SABOTAGES FAILED
DIFFERENT SUBSETS — keep the FIRST of a run fails one; a miss advancing
the passenger fails two; a cursor left INSIDE the run fails ALL FIVE. That
distribution is itself evidence the five are not one test wearing five
names.

## AND THE ATTRIBUTION RULE, WHICH IS THE 2,488-BYTE LESSON IN PROSE

The draft landing note said: "per-turn work moves from constant to linear,
in counts, WITH THE LOOKUP TERM REMOVED ENTIRELY." Read as one sentence
about the deletion, THAT CREDITS COMMIT TWO WITH COMMIT ONE'S WIN. The
lookups were removed by the join, on a shape that already existed, and
measured there (cold 401 -> 0). The deletion does not remove lookups; it
ADDS THE WALK the lookups would have multiplied.

That is the phantom-improvement failure priced tonight at 2,488 B and 33%
— a stage credited with a win belonging to a change beside it — ARRIVING
IN PROSE RATHER THAN IN A BENCHMARK NAME. Same defence:

    ATTRIBUTE PER COMMIT, NEVER PER STAGE.

    COMMIT ONE   lookups N+1 -> 0 cold, 1 -> 0 warm. Decodes and
                 entries-handed UNCHANGED — registered as falsifiers before
                 the run, and unmoved.
    COMMIT TWO   per-turn work CONSTANT -> LINEAR in length. Entries handed
                 1 -> N+1. Decodes unchanged at 1.
    TOGETHER     the projection is gone and the walk replacing it carries no
                 lookup per record — which is WHY the join was cut first:
                 not to make the deletion look better, but so the exposure
                 never existed.

The two results are on DIFFERENT AXES and neither cancels the other.
Written that way, no future reader can quote the pair as one number.

## THE WARM PATH REGRESSED, OUTSIDE EVERY REGISTERED FALSIFIER

9ed3f561, ruled by f3aa1d0b, 2026-08-18. Commit two is HELD on it.

    length-invariant          WARM                       COLD
    c8bfd149 (before join)  lookups=1  cacheWalked=0   lookups=N+1 walked=0
    1a53d773 (after join)   lookups=0  cacheWalked=N   lookups=0   walked=N

THE COLD COLUMN IS THE WIN THE JOIN WAS CUT FOR. THE WARM COLUMN IS A
TRADE NOBODY REGISTERED: one lookup becomes a walk of N cache entries.
CONSTANT BECOMES LINEAR IN CONVERSATION LENGTH, ON THE PATH EVERY LIVE
TURN TAKES, TODAY, BEFORE THE DELETION EXISTS.

THREE PREDICTIONS WERE REGISTERED AND ALL THREE HELD. The executor's
falsifiers were entries-handed and decodes — both on the IR SIDE. The
arm's was "each entry visited AT MOST ONCE" — which this SATISFIES.

    VISITING EACH ENTRY ONCE IS NOT THE SAME AS VISITING ANY AT ALL ON A
    WARM TURN.

The regression sat outside all three because every falsifier described the
side of the join that was being fixed, and the cost moved to the side that
was merely being traversed. This note's own thesis, arriving through a
pre-registration rather than through an instrument.

AND THE INSTRUMENT ONLY SAW IT BECAUSE ITS AUTHOR NOTICED ITS AXIS HAD
RETIRED: when lookups fell to zero in both columns, a counter reading zero
on both sides stopped discriminating anything, so it extended the
instrument to count WHAT REPLACED THE LOOKUPS. A retired axis reads
exactly like a clean result.

RULED — SEEK, DO NOT WALK. A warm pass reads the IR side from
`TailAfter(watermark)`; the cache cursor must be POSITIONED at the first
entry above that watermark by a search, not reached by walking from zero.
`cached_log.go`'s own Lookup comment states the ground: entries are
ASCENDING BY FigaroLT and `ReadFrom` BINARY SEARCHES ON EXACTLY THAT. The
join then keeps "each entry visited at most once" AND regains the warm
path's constant cost.

AND WHY THIS BLOCKS COMMIT TWO RATHER THAN DISSOLVING INTO IT — the warm
path dies at commit two, so the regression is arguably transient:
  1. COMMIT ONE MUST STAND ALONE. It is a separate commit precisely so its
     result is attributable. A commit excused by a LATER commit is a stage
     justified as a whole, which is what splitting it up was for.
  2. THE COSTS COMPOUND RATHER THAN REPLACE. After the deletion every turn
     takes the cold path; if the cache side also walks from zero, the pass
     walks the IR log linearly AND the cache log linearly, and the second
     walk is the one nobody has priced.

## AND A RETRACTED REHEARSAL STAMP, DISCLOSED RATHER THAN REPAIRED

The same message ended "REHEARSED AT 1a53d773". IT WAS FALSE WHEN WRITTEN:
the checkout back to that commit had FAILED — an untracked file blocked
it, git printed "error: ... Aborting" — and the next action assumed
success. The branch did not carry the counters at all. Now true and
verified: 8638bdc2, parent 1a53d773, green there.

THE FINDING IS UNAFFECTED: both columns came from real runs at both
commits with the same instrument, and were re-run since.

WHAT IT TEACHES, and it is a NEW DIRECTION rather than a repeat:

    THE DEFENCE THIS CAMPAIGN BUILT IS FOR SILENT FAILURE — assert the
    artifact, never the status field, because a command that succeeded is
    not an artifact that is correct. HERE THE COMMAND FAILED LOUDLY AND
    THE OUTPUT WENT UNREAD. A defence against silence does not cover
    noise.

AND THE PATTERN WITH THREE INSTANCES TONIGHT, worth naming because it is
about people and not instruments: the arm rebuilt, an hour later and in
another corner, the exact false negative it had refused; I found my own
gate rule weaker than I had thought two hours after making it; and the
rehearsal rule was broken by the aria that proposed it, within the hour.

    A RULE IS NOT INTERNALISED BY BEING WRITTEN, OR EVEN BY BEING
    PROPOSED. Recognition attaches to the SITUATION it was learned in, and
    every one of these three was a familiar rule meeting an unfamiliar
    costume.

## THE SEEK FIX HELD, AND A COMMENT ASSERTED THE PROPERTY IT HAD VIOLATED

f3aa1d0b, 2026-08-18. Commit two cleared to cut.

    WARM cacheWalked   N -> 0 at every length
    COLD cacheWalked   UNCHANGED at N

The second is the one that had to hold: a fall there would have meant the
seek SKIPS records the cold pass must consume. Lookups 0 in both columns,
decodes 1, entries handed 1 warm / N+1 cold, memo pins unmoved.

THE CAUSE WAS THE CURSOR STARTING FROM ZERO, and the executor found it BY
READING ITS OWN COMMENT — which claimed the cursor was "built after the
span is chosen" and that skipping forward cost "nothing beyond the entries
walked". BOTH HALVES FALSE, written before the code drifted under them, and
asserting PRECISELY the property that had been violated.

    A COMMENT IS A CLAIM NOBODY TESTS.

Third instance tonight of documentation and code disagreeing while only the
documentation was consulted: a benchmark named Warm that would have
measured a cold walk; `xwal_log.go` promising "off the hot path" for eleven
minor versions after its premise died; and this.

## AND THE SECOND SIGHTING OF THE DEEPEST SHAPE IN THIS CAMPAIGN

CANARY J: restoring the walk makes the new count pin fail AND LEAVES ALL
FIVE JOIN TESTS GREEN. They assert what the join PRODUCES and where its
cursor LANDS — never how far it TRAVELLED. A projection built by walking
from zero is BYTE-IDENTICAL to one built by seeking.

    WHEN THE OUTPUT IS INVARIANT UNDER THE DEFECT, ONLY A COUNT CAN SEE IT.

First sighting: a segment header one record ahead, invisible to a value
oracle because form patches are IDEMPOTENT. Second: a cursor that walks
where it could seek, invisible to five correct tests because the result is
IDENTICAL. Different layers, one shape, and it is the standing argument for
this campaign's preference — where the question is HOW MANY TIMES, COUNT
IT.

## PROVENANCE ORDERED FOR THE LANDING NOTE

The confirming numbers came from a head with ONE DIRTY FILE — the arm's own
instrument, because the executor cherry-picked it at a commit predating the
cache counters. Disclosed unprompted, with an offer to re-run clean.

RULED: TAKE THE OFFER. Apply the updated instrument to the branch, re-run
CLEAN, and quote THOSE numbers. Not from doubt — they are consistent across
two heads and two hands — but because the landing note is the artifact that
outlives everyone here, and by this document's own ruling a dirty tree is
identified only by a digest nobody will read in a year. One apply and one
re-run buys numbers whose provenance is a commit.

## THE MAXIM APPLIED TO ITSELF, AND WHAT THE ATTEMPTED FIX FOUND

9ed3f561 verified the sixth maxim was committed as claimed AND THEN ASKED
THE MAXIM'S OWN QUESTION OF IT: `git branch --contains` says 2f568855 lives
on feat/layered-cache ALONE. Not on main, not on the seam branch.

    "IT IS IN THE FILE" AND "IT IS IN THE FILE PEOPLE READ" ARE TWO
    DIFFERENT CLAIMS. A document on a branch nobody reads would still
    report a pass to anyone checking whether it was written.

I ATTEMPTED THE OBVIOUS FIX AND ABORTED IT, which is the finding. A
cherry-pick to main CONFLICTS, because 160 lines of
skills/figaro/contributing/maintaining.md exist ONLY on this branch — main
carries 9 section headings, this branch carries 16. The maxim's own text
refers to the section around it. So cherry-picking one maxim would either
smuggle 160 lines of campaign documentation into main under a one-maxim
commit message, or land a paragraph referring to neighbours that are not
there.

RULED: THE DOCUMENTATION LANDS AS A UNIT WHEN THIS BRANCH MERGES, and that
is now a NAMED DEPENDENCY rather than an assumption. Everything this
campaign has learned — six maxims, the hazard-instrumentation section, this
plan — reaches a reader only through that merge. IF THIS BRANCH DIES, THE
DOCUMENTATION DIES WITH IT, and that is a larger loss than the code.

## AND A LARGER INSTANCE OF THE SAME QUESTION, FOUND WHILE CHECKING

~/notes/figaro holds 32 markdown files, 356 KB, INCLUDING the note this
campaign's standards are built on
(instrument-not-reaching-the-code.md, ~16 instances) and every
REFUSED-NOT-MISSING open item.

    ~/notes IS A GIT REPOSITORY WITH NO COMMITS. Not behind, not stale —
    NEVER COMMITTED, on a branch with no history at all.

It is outside the repo, outside the flake, and outside every backup this
campaign has reasoned about. The plan cites those notes as the authority
for standing rules; the notes have no version, no history, and no copy.
SURFACED TO GLUCK RATHER THAN FIXED: the directory is his, it contains
personal material beside the figaro notes, and a blind commit of 526 MB of
mixed content is not a decision an aria makes for its owner.

## THE DELETION'S MOST EXPENSIVE HAZARD, FILED UNDER "NOT VERIFIED"

6ec565b5's ephemeral-path recon, promoted and made blocking by f3aa1d0b,
2026-08-18.

    TODAY the translated prefix is CARRIED AS BYTES in Previous.State and
    never re-encoded. AFTER THE DELETION it is RECOMPUTED EVERY PASS.
    PREFIX-BYTE STABILITY STOPS BEING A PROPERTY OF CARRYING BYTES AND
    BECOMES A PROPERTY OF ENCODER DETERMINISM.

THIS IS NOT AN EPHEMERAL CONCERN. THE VENDORS CACHE ON PREFIX BYTES. If
anything unordered reaches the encoder — a map iterated into JSON, a
timestamp, a pointer-ordered set — the prefix changes shape between turns
and PROMPT CACHING MISSES, on every provider, for every aria. The failure
is not a wrong answer; it is a silent recurring cost in latency and money
that no correctness test we own can see.

AND THE TEST THAT LOOKS LIKE COVER CANNOT BE:
`TestCatchUpPreservesPrefixBytes` pins that the prefix bytes do not change
AND WOULD PASS EITHER WAY — it cannot see the mechanism change underneath
it. The sixth maxim, arriving where it costs money.

RULED — COMMIT TWO OWES AN ENCODER DETERMINISM PIN: encode the same
records twice from scratch, assert the bytes IDENTICAL at every record and
not only the tail; per provider, or with the uncovered providers NAMED in
the landing note.

AND THE CANARY MUST PERTURB A MAP-RENDERED RECORD, not a scalar —
6ec565b5's refinement, from reading the encoders rather than from the
analogy: perturbing a scalar proves the pin reaches AN ENCODER without
proving it reaches THE HAZARD.

    THE PERTURBATION HAS TO BE ABLE TO PRODUCE THE FAILURE MODE, NOT
    MERELY A FAILURE.

Same lesson as the interior-gap canary, reached independently in a second
place — which is how a rule earns the word "standing".

## AND THE REPORTING RULE THAT CAME WITH IT

The finding above was filed under NOT VERIFIED rather than led with,
because its author had not tested encoder determinism. Its own restatement,
adopted:

    A LIMIT OF METHOD THAT IS ALSO A HAZARD GETS PROMOTED TO THE FIRST LINE
    AND NAMED AS A HAZARD, WITH THE LIMIT ATTACHED TO IT — never the other
    way round. "I could not verify X" and "X is a live risk this stage
    would ship" are different sentences, and the second outranks the first.

## A NEW SPECIES OF THE BRANCH TRAP: MIXED PROVENANCE UNDER A UNIFORM LABEL

The same recon reported `anthropic.go`'s `acceptAssistantProjection` as a
second consumer of `.Form`. Checked rather than relayed: TRUE on
feat/layered-cache (3 occurrences), FALSE on feat/delta-seam (0), where
piece A banked it at 493a6bcb.

THE MECHANISM IS SUBTLER THAN "WRONG BRANCH". Its worktree was detached at
a layered-cache commit; it read `projection.go` CORRECTLY via `git show`
against the seam ref, and read EVERYTHING ELSE out of the tree around it —
under a single header claiming one ref for all of it.

    A REPORT OF MIXED PROVENANCE UNDER A UNIFORM LABEL IS WORSE THAN ONE
    THAT IS WHOLLY WRONG: the parts that were right make the parts that
    were not look checked, and no reader can partition them.

THE FIX, applied by its author unprompted: `git grep` AGAINST THE REF,
never out of the tree around you. A WORKING DIRECTORY IS A PLACE YOU ARE
STANDING, NOT A CLAIM YOU CAN CITE. Every material claim was then re-run
against the seam ref and all of them stand — including the Q3 inventory,
correctly identified as the part most likely to have moved under commit
one: non-study `Patches` on records appear in exactly THREE places, ONE
test covers the ephemeral fold sites, and THE COMBINATION THAT DIES — warm
pass, `Form == nil`, records carrying patches, asserting the transition
against the carried board — IS ASSERTED NOWHERE.
