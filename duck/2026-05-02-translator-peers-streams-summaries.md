# 2026-05-02 — Translator architecture: peers, streams, summaries

**Status:** brainstorm. Lossless capture of the user's reasoning, lightly
structured for readability. Editorial structure is mine; the framing,
emphases, and conclusions are the user's. Feedback section at the bottom is
clearly labeled and separable.

## TL;DR

Figaro is fundamentally a translator system between *peers*, each with its
own IR. Three IRs are in play today (Anthropic, figaro canonical, CLI/RPC);
N more are anticipated (other LLM APIs, telegram, webui, future tools).
The system should be redesigned around this symmetry, with translators as
first-class components, the figaro IR as the canonical merge point, and a
two-tier stream model that distinguishes *volatile streaming events* from
*durable summarized messages*. Don't replace figaro's implementation
immediately — build the translator first, evaluate it in isolation, then
integrate.

## Motivating observation

> "I am converting some incoming format into some outgoing format. For any
> given event, whether that be a content event or a full message, it
> corresponds to one or more figaro IR events, or it is a figaro IR event
> and corresponds to one or more provider IR events."

Several IRs are already in play:

- the **Anthropic IR** (LLM-side wire format),
- the **figaro IR** (the canonical timeline),
- the **input IR** between the client CLI and the server (RPC wire
  protocol).

Future expansion is bidirectional: more LLM APIs *and* more clients (CLI,
chat, telegram, webui). Treating CLIs and LLMs as the same kind of thing —
*peers with their own IR* — opens a more declarative redesign.

## Architecture

### Inbox as aggregator

The inbox aggregates all events in a stream **according to that event's
type**. It handles input events according to the RPC wire protocol, where
the wire protocol wraps some event payload. The inbox streams each
arriving event to a durable per-type output stream (Anthropic, figaro, or
user input).

When any kind of message arrives, the inbox **signals the translator**.

### Translators — three layers

**Tail translator** (the inner, common-path translator):

- Assumes the set of streams is causally complete up to a point.
- Runs once per event added.
- At first design, has *no awareness of which message caused it to run*.
  It will be given a pointer to a typed peer-IR stream and a pointer to a
  figaro stream — by *stream* meaning a causally-masked list.
- Assumes all messages up to the causal mask are populated and constraints
  apply. Assumes there are no holes (hole repair is a separate concern,
  rare, and does not need to be cheap).
- Expects a trailing sequence of events in either list to have no
  equivalent in the other. Translation is required for that tail.
- Receives the starting index of the trailing untranslated range.
  Iteratively sends both streams to the relevant per-peer translator,
  applying and advancing the causal mask at each step.
- The peer-side translator is responsible for handling per-event values.

There are two relevant providers (today: Anthropic + the CLI peer). There
are several forms of translators for different stages of the process.

**Hole-patching translator** (the outermost):

- Assumes there *may* be holes.
- Run only when the system explicitly asks for it.
- Walks the list from the start, locates the first hole, applies causal
  masks such that the side beyond the hole cannot be seen.
- *Patching breaks the append-only constraint*, so the causal mask
  operates by emitting all events to a *new* append-only list,
  effectively replacing the holed list. This avoids insertion mid-list.
  Hole patching is rare and can take longer.
- Once the hole is patched, delegates to the tail translator.

**Direction of translation.** The tail translator is told which of the two
lists has untranslated tail entries and the starting index. If the figaro
side is ahead of the peer, the translator applies a causal mask to the
first index that has no matching peer event, passes both sides to the
peer's translator (figaro IR → peer IR), emits the generated events to
the peer's stream, advances the causal mask of the peer stream, and
repeats until the mask reaches the end. The mirror direction (peer ahead
of figaro) is handled by a different method on the same translator —
peer IR → figaro IR, emitting into the figaro stream.

The tail translator brings both lists/streams *up to date*. With both
lists in sync, we are ready for the figaro agent loop.

### N peers vs 2 peers

For a single run of `hole-patching translator → tail translator`, there
are exactly two peers being reconciled (one figaro stream, one peer
stream). For the figaro agent loop, there are always **three** streams in
context: figaro IR, peer 1, peer 2 (or however many).

Hole patching delegates to a 2-peer function that is called once per
peer–figaro pair (twice in the 3-peer case). Phase 1 always brings every
stream into sync with figaro before phase 2 (the agent loop) runs.

### Figaro actor loop

The actor gets all streams but **doesn't observe their types** beyond
their identity (peer 1 has provider 1 attached; peer 2 has provider 2
attached; figaro IR is the canonical center). It is invoked on every new
figaro event, sees only the latest each time, and decides:

- Do I need to stream events to peer 1? **XOR**
- Do I need to stream events to peer 2? **XOR**
- Do I need to stream/handle events to the environment (tool calls,
  configuration changes, etc.)?

Examples:

- Most recent figaro event is a user message → stream peer-1 IR events to
  the LLM. The LLM responds; its events post back through the tail
  translator (no holes to patch in the common path).
- Most recent figaro event has a "yield for user response" marker, *or*
  is an in-progress agent message → stream peer-2 IR events over the SSE
  stream to the CLI peer.
- Most recent figaro event contains a tool call → stream figaro events
  to a figaro tool actor, which executes the tool and posts results back
  to the figaro stream. The translator then advances the peer streams
  accordingly.

### State — cursors at the peer adapter, not the actor

The actor is stateless per invocation. But somewhere we need to know, for
each peer that accepts events iteratively, *what was the index of the
last event we sent to this peer*. This is a cursor.

- The LLM is easy: it receives all messages up front, then streams
  responses. No outbound cursor needed.
- The client peer (CLI today; telegram / webui in the future) receives
  events live and needs a cursor — otherwise we either re-send everything
  or risk gaps.
- The figaro agent (tools, settings, controller) is the same: it shouldn't
  re-execute every tool call on every actor invocation. It needs to know
  what's already been dispatched.

The provider/peer adapters can hold this cursor. They each carry a pointer
to the full stream and an index of the last message pumped over the wire
(either to the figaro tool handler or to the client message stream). The
actor remains as stateless as practical; the bookkeeping lives at the
boundary that needs it.

> "The figaro tool handler I think could abide by the same spec that the
> user client SSE CLI stream does. I don't think either exists currently."

The Anthropic stream behaves the same way under the same spec, except it
doesn't actually support streaming *into* it — so it ignores most calls
to stream new data and only acts when a full message is in the stream.

## The deeper meditation: streams vs messages

This design is sound *if and only if* streaming events form the
fundamental appended primitive to these immutable append-only lists. That
might be how to take it in early prototypes, but **it will not fly**
long-term.

Verbosity hedging is required. Contiguous sequences of lower-order stream
events must be aggregated and summarized by a single stream event. This
is expected to happen semi-frequently — frequently enough that
**caching operations should only commit after we are sure a summary has
occurred**. Currently this occurs on the "turn" boundary. The system
defines no boundary in the full-duplex case.

Summaries bear on:

- what is persisted,
- what is streamed over what surface.

Therefore, summary is a **function of all participating peers**.

### Two parts of every stream

- **Durable** — events that have received a summary event.
- **Active / volatile** — current streamed events that have not yet been
  summarized.

### Summary operation

The summary operation can only be taken by the figaro actor when it sees
a tail of events that should be summarized. The trigger is some signal in
the stream itself — for example, an event message that says "aggregate
these now," or a CLI client streaming typed content with an `enter` event
arriving and the server aggregating what it has accumulated.

Figaro defers to an implementation that summarizes figaro content. That
method is given:

- the stream of events to be summarized (represented as a distinct
  sub-stream),

and produces:

- a single summarized event (a *message*) conforming to the global
  appended-primitive shape, **without** the delta events but summarizing
  them. It has `logical_time` and `timestamp`.

The logical time of the summarized message corresponds to **the last
logical time that had been read** — meaning logical time may *skip* when
moving from a stream of fine-grained events to a serialized message
boundary.

### After the figaro IR summary message is created

1. The stream flushes pending unsummarized events.
1. The summary message is appended to the durable summarized stream.
1. The summarized stream goes to storage (similar to today's persistence).
1. `flush` is called on the *other* peer streams. Each may generate one or
   more summary events of its own — they may emit multiple, if the figaro
   IR doesn't cleanly map onto theirs.
1. Since the providers have effectively *write access* over the stream
   (by yielding updated events relative to what was accumulated), they
   reassign / mend the foreign keys at this point — knowing the span of
   logical times that were compressed by the previous summary — so the
   peer-side events all point at the *finishing* figaro logical time
   only. That binds the peer events to exactly one figaro event,
   preserving the **N:1 XOR 1:N** constraint at the message level.

There are several system expectations here — e.g. that the stream events
don't point to logical times prior to pre-streamed events — though
perhaps those don't matter, so long as the N:1 XOR 1:N constraint isn't
violated. Validation by experimentation with the system we build.

## Implementation approach

> "I'd like to try to reason about this plan and see if we can't implement
> a translator with a nice way of viewing it... I wouldn't want to replace
> the implementation of figaro with this immediately; rather I'd like to
> build up the translator and see where it fits, and build tooling to
> evaluate it and its correctness and performance."

Build the translator as a standalone component with mock streams. Get the
tail translator right first; the hole-patching layer is a wrapper. Build
evaluation harnesses (correctness, throughput, stream-back-pressure
behaviors). Don't fold it into the live agent until the abstractions
prove themselves.

______________________________________________________________________

## Feedback (Claude — separable from the brainstorm)

The architecture is coherent. The unification (every party is a peer with
its own IR; the figaro IR is the canonical merge point) is the right
framing for the multi-client, multi-LLM future the user wants. The
two-tier stream model is the load-bearing insight — without it,
write-amplification grows unboundedly.

A few items worth nailing down before code lands:

1. **Direction-of-translation phrasing.** The text above describes "if
   figaro is ahead of the peer, peer-IR → figaro-IR" in places and the
   reverse elsewhere. Both are needed — one method per direction on the
   peer's translator. The naming convention ("encode" vs "decode" or
   `figaroToPeer` vs `peerToFigaro`) should be settled before
   prototyping.

1. **Summary trigger rule.** All examples in the brainstorm are *external*
   triggers (an event in the stream tells the actor to summarize). Worth
   committing to "external-only, for now" — internal heuristics
   (time-based, size-based, token-count-based) are tempting but introduce
   non-determinism that will bite during testing. Add later if needed.

1. **Cursor durability.** The brainstorm puts cursors on peer adapters.
   Worth being explicit that they are *persistent across process
   restarts* (probably stored under the aria's directory) — otherwise a
   client reconnect would replay the entire conversation, which today's
   CLI tolerates but a webui won't.

1. **FK collapse on summary is intentional.** When a summary message at
   figaro lt=N replaces stream events at lts [N-K..N], peer events that
   were FK'd to those individual lts get re-mapped to FK = [N]. This
   collapses information about which peer event corresponded to which
   pre-summary figaro event — that information is *not* preserved
   post-summary. Worth stating this explicitly so a future maintainer
   doesn't try to "fix" it.

1. **Tools as a peer.** The brainstorm sometimes treats tools as
   "environment" and sometimes as a peer-like system. Treating the figaro
   tool handler as a peer that subscribes to figaro events and posts
   results back is symmetric with the rest of the design and worth
   formalizing. The "figaro tool handler... could abide by the same spec
   that the user client SSE CLI stream does" line in the brainstorm
   already hints at this — make it explicit.

1. **Naming collision risk.** "Translator" overloads the existing
   `Translation` / `ProviderTranslation` types in
   `internal/message/translation.go` — those are the *cache-of-wire-bytes*
   shape, not the *encode/decode logic*. The new translator component
   should pick a name that doesn't conflate (e.g. `IRBridge`,
   `StreamTranslator`, `PeerTranslator`, `IRConverter`) or the existing
   `Translation` types should be renamed (e.g. `WireProjection`).

1. **Cross-stream causal ordering.** Within one stream, monotonic LT is
   enough. *Across* streams, the FK relation encodes the mapping. The
   figaro stream is the canonical merge point and serves as the global
   ordering — concurrent events from independent peers (e.g. user types
   while LLM is streaming) all end up logged in the figaro stream in
   some serialization order. Worth documenting this as the
   "happens-before" rule for the system.

1. **Stage relationship.** The current Stage D.2 + Stage E work
   *prefigures* this redesign — particularly the per-aria translation log
   (Stage D.2d/f), which is essentially "the durable peer stream for
   provider X." The new architecture would generalize that to N peers and
   add the volatile event tier on top. Worth tracing which existing
   abstractions survive the redesign, which get renamed, and which get
   replaced — before deciding where the translator prototype plugs in.

## Next steps (proposed)

1. Sketch the translator's Go API in isolation. `TailTranslator` first;
   `HolePatcher` second. Mock streams (in-memory, deterministic).
1. Define the `Peer` interface — the abstraction every peer adapter
   implements (its IR type, its encode/decode methods, its cursor).
1. Build a small evaluation harness: feed canned event sequences into the
   translator, assert correctness of the resulting streams, measure
   throughput.
1. Settle the volatile/summarized boundary in the prototype before
   wiring it into the live agent. The trickiest correctness questions
   live there.
1. Once the translator is validated, decide where it slots into the
   existing figaro: replace `Anthropic.Send`'s projection? Replace the
   agent's `startLLMStream`? Both? This is a conversation, not a
   foregone conclusion.

# Answers to questions asked by CC

1. Settle the direction-of-translation naming before prototyping (encode/decode vs figaroToPeer/peerToFigaro).

- encode / decode is correct

2. Commit to external-only summary triggers for now; defer internal heuristics.

- summary triggers I think are going to be in practice the following:
  - cli: I think the cli might just send one message at a time anyway, so summary can probably trivially happen. Figaro most likely would create a trivial summary. This is an interesting case because I think the cli might currently send a full message to figaro which is not aggregated. So we can exercise the case where a single event is summarized to a single event, and the case where the volatile stream is just projected onto the durable part of the stream with minimal if any transformation
  - llm: anthropic gives some kind of a summarize event that instructs the recipient to aggregate all that it had hitherto aggregated into a message. That should get an equivalent figaro ir message, and should instruct figaro to do the same. That should be the trigger. That trigger implies that it was derived from some peer, and as such it is expected should order the other peer to summarize, hence why it was ever generated to begin with. OR the trigger implies that figaro wants some peer to summarize, which would be weird anyway. In practice this doesn't happen, but it does allow us if necessary in the future to write a tool that the agent can use to demand summary of other messages; e.g. if a tool result is far too long or something. but for now we'll leave it up to how the coding shakes out whether we need that.

1. Per-peer cursors should be persistent across process restarts — otherwise webui reconnects replay everything.

- I honestly don't think it's needed. I think we can say that all post-summarized messages have already been visited by the handlers. On summary, all peers should probably get that event too, as a hook or something. Everything else is volatile. If the process dies, the unsummarized content is lost. The summary is load bearing. Mid turn results needn't be preserved (for now.....in the future we probably will want to make this durable too, but for now we are good with what we have....item for later)

1. The FK-collapse on summary is intentional information loss; document it so a future maintainer doesn't "fix" it.

- Yeah that'll work. FK collapse is intentional.

1. Treat tools as just-another-peer; the brainstorm hints at this, the design should formalize it.

- Yeah but I think since all our tools are builtins and share a security boundary, and also communicate natively with figaro, they should be somewhat specialized. Like an "environemnt" peer. If the figaro IR list is longer than the other two peers, it means the environment updated in native figaro language and the other peers need to be notified. In fact, the "figaro IR is longer" can on tail translation should imply that the environment recorded updates of some kind and should be typed as such. But the system should have the capacity to configure environments, perhaps via the chalkboard, and other implementations are reasonable (e.g. one of ssh, for example, or using some process pool over IPC rather than forking or something).

1. Naming collision risk — Translation already means "cache of wire bytes" in internal/message/translation.go. The new translator-as-encode/decode component needs a different name (or the existing one gets renamed).

- translations are sort of caches of wire bytes, still. I think the definition is basically the same. It's just an extension on what we already had. We are still rebuilding caches, and formalizing the reconstruction of those caches. But we are allowing the system to more naturally fully reprocess the cache or update it by constraining a set of streams by increasingly stronger constraints (patching holes, handling tails, bringing all translated content up to date, then fanning it out to more producers for a feedback loop)
- encode / decode should still be translator. TranslatorV2 if we need to replace it, but it should be more of a progression than a replacement.

1. The figaro stream is the canonical "happens-before" merge point — worth stating explicitly.

- Worth stating explicitly, I think.

1. The existing Stage D.2 + E translation log is essentially "durable peer stream for provider X." The redesign generalizes it to N peers + adds the volatile tier on top. Worth tracing which abstractions survive, which get renamed, which get replaced — before deciding where the prototype plugs in.

- Let's maybe rewrite the prototype streams from the ground up but based on our current types. As we update types, we'll end up having to update the current dependencies of those types. We can compare our new data types with the updated versions of the old as a checkpoint and decide on the fly which can be merged into a common impl, which should be replaced wholesale, and which should be removed.
