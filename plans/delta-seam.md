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
