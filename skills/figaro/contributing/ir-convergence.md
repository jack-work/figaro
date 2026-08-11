# figaro IR and UI IR convergence

> **STATUS: PART SHIPPED, ONE PART OPEN.** Checked against source. The naming
> pass landed (`message.ContentProse` exists, `message.go`). Turn addressing
> landed and supersedes this file's turn-id discussion: `message.TurnID` is the
> coordinate, LT is the storage substrate, both documented at `message.go`. See
> [turns.md](../reference/turns.md), which is canonical for that.
>
> **Still open:** tool calling over a separate channel, this file's "key
> blocker". There is no tool channel in `internal/store` today, so the Future
> section below is a proposal, not a description.

Two representations of a conversation exist today:

- **fig IR**: `message.Message` (`internal/message`): the canonical, lossless,
  provider-agnostic record. The main xwal channel. Append-only turns; rich
  content blocks + provenance (roles, usage, stop reasons, interrupts, patches).
- **UI IR**: `aria.Turn` + `livedoc.Node` on the `aria.Page` read wire
  (`internal/livedoc`, `internal/livelog/aria`): a render projection. Lossy and
  splice-friendly, with a versioned mutable suffix for live repaint. Derived
  from fig IR through `internal/uiir`/`internal/compose`, a pure one-way map.

The north star is to **converge them** so the fig IR carries (or trivially
projects to) the UI shape, and the live-turn machinery is shared. We are not
there yet; the blocker is tool handling (below). Today the UI projection is
materialized in `aria.Server` and recomputed from canonical IR when a dormant
aria hydrates. A linked `ui` xwal channel remains an optional derived cache, not
current storage.

> **The convergence now has a plan and a shared coordinate.** See
> [../reference/turns.md](../reference/turns.md). Both IRs carry a **turn id**; **LT
> joins, turn id addresses**. The open debate below ("should a live message be a
> whole turn?") is **resolved: yes**: the UI IR unit is the turn, prompt and
> reply together.

## The drift, measured

`content` blocks and `nodes` are the same *concept* but have never been the
same *thing*. The mapping is not 1:1 in either direction:

| fig IR content | → UI IR nodes |
|---|---|
| `tool_invoke` (assistant msg) **+** `tool_result` (user msg) | **1** `tool` node: 2 blocks, 2 *messages*, 1 node |
| empty `prose` / `thinking` | **1**: reserves its ordinal; renderer hides it until content arrives |
| `image` | **0**: no case in the `compose.Nodes` switch |
| `interrupt` | **0**: no case |
| `prose` inside a `tool_result` message | **1** `steering` node: no content counterpart |

Vocabularies overlap by half: content is `prose, image, thinking, tool_invoke,
tool_result, interrupt` (6); nodes are `prose, thinking, tool, steering` (4);
shared: `prose, thinking`.

**What turn ids fix.** The node `id` field was doing double duty, a synthesized
`"<lt>.<blockIdx>"` for prose/thinking, an opaque provider receipt for tools,
and nothing at all for the user's prompt. The UI address is now the positional
pair `(turn, node ordinal)`, while `lts`/`src` carry provenance and the user's
question is `Turn.Inquiry`. `Node.ID` still serializes the old string metadata
for now, but clients must not use it as identity. The cardinality mismatch
above stays: folding a tool's invoke and result into one node is correct, and
is now stated by `lts`/`src` instead of being invisible.

## Answers to the open IR questions

- **Where do tools render, assistant or user message?** The **assistant**
  output inside the turn. `compose.Nodes` iterates assistant messages; a
  `tool_invoke` block becomes one `tool` node. The opening prompt is
  `Turn.Inquiry`, not a node or separate unit. A user-role `tool_result`
  message also creates no unit; it folds into the invoked tool node.
- **How does the UI converge a tool to one item?** fig IR has two events in two
  messages: `tool_invoke` (assistant) and `tool_result` (a later user message),
  linked by `tool_call_id`. `compose.toolNode` merges them into a single `tool`
  node `{id, name, args, status, output}`: status `running` until the result
  arrives, then `ok`/`error`; output is the streamed/final result text
  (tail-bounded to 200 lines: the full text stays in the canonical IR). So the
  UI's single tool node is the *folded lifecycle* of the fig IR's invoke+result.

## Primitive-name alignment

`content` blocks (fig IR) and `nodes` (UI IR) are the same concept; ideally both
become **"blocks."** Deferred (cosmetic), but acknowledged. Shared primitive
names are the target: `prose`, `thinking`, `tool`, `image`.

- **Done** (this branch): `ContentText` → `ContentProse` (wire `"text"` →
  `"prose"`), matching `livedoc.NodeProse`. `thinking` already matches. The
  disliked "tic" term is gone (→ "message"/"turn").
- **Deferred:** rename `content`/`node` → `block`; the `Content.Text` field →
  `Markdown`; the `TextContent()` constructor → `ProseContent()`.

## Configuration provenance

`model` and `provider` are form values. The fig IR carries content and
provenance; configuration is read from `system.model` and `system.provider`.
Dormant list metadata comes from the live `_meta` summary, not a second
derived snapshot.

## Future (north star + blockers)

- **Tool calling over a separate channel: the key blocker.** Today a tool is
  "handled via instructions" inside the fig IR (invoke block in one message,
  result in another). The intent: the fig IR **encodes a tool** (one block), and
  the tool is *run over a separate channel*, delivering IR updates to the
  mutable turn suffix: exactly how the UI streams `NodeDelta`s into an open
  node. This unlocks:
  - **Formalizing the "live" message in the fig IR** the way the UI does: an
    open message with a version, mutated by deltas, then closed. The live state
    can be serialized; on a server crash with a live tail message, it can be
    **discarded or closed** by policy.
  - **Restore correctness from UI tool-state:** because the UI knows, to the
    degree observed, whether a tool was invoked/completed, a separate tool
    channel + that state lets us know **which tools still need handling** on
    restore. (Full fig↔UI convergence then becomes safe.)
- **"message" supplants "tic"; turn vs message.: RESOLVED.** The UI IR unit is
  the **turn**: user inquiry and assistant reply in one entry. Tools and
  steering do not make this hard once nodes carry `lts`, a tool node simply
  spans two LTs, and steering is the same primitive as the prompt at a later
  position. See [../reference/turns.md](../reference/turns.md).

## Challenges to call out

- Tool **lifecycle mismatch**: fig IR = invoke + result (two events, two
  messages); UI = one node with a status flip. Converging needs the
  tool-over-channel model above. *Mitigated:* the node now records both source
  LTs in `lts`, so the fold is documented rather than lossy.
- **Steering** mid-turn breaks a clean request/response turn boundary.
  *Resolved:* steering is a node inside the turn, ordered after the tool nodes
  and sharing the `tool_result` LT.
- Reshaping the unit (turn-as-message) wants **compaction** in place first.
- The wire `type` value changed (`text`→`prose`); existing stores need a fresh
  start (or a migration): fine on this branch.

## Sequence

1. ✅ Answer the IR questions (above).
2. ✅ `text`→`prose`; message/turn vocabulary replaces `tic`.
3. ✅ Turn-shaped `aria.Page` pull/push wire, positional `(turn,node)`
   addressing, field deltas, and an in-memory live suffix.
4. ✅ Dormant hydration recomposes turns from canonical IR.
5. ⏭ Optional **UI as a linked xwal tree**: a derived `ui` channel whose entries
   are sealed turns, `mainLT` = the turn's first LT. Never persist `Turn.Live`.
6. (later) tool-over-channel; live-message formalization in fig IR; full
   convergence.
