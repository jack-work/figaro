# figaro IR ↔ UI IR convergence

Two representations of a conversation exist today:

- **fig IR** — `message.Message` (`internal/message`): the canonical, lossless,
  provider-agnostic record. The main xwal channel. Append-only turns; rich
  content blocks + provenance (roles, usage, stop reasons, interrupts, patches).
- **UI IR** — `livedoc.Node` + the `aria` read wire (`internal/livedoc`,
  `internal/livelog/aria`): a render projection. Lossy, splice-friendly, with a
  per-node version for live repaint. Derived from the fig IR by `compose.Nodes`
  (`internal/compose`), a pure one-way map.

The north star is to **converge them** so the fig IR carries (or trivially
projects to) the UI shape, and the live-message machinery is shared. We are not
there yet; the blocker is tool handling (below). Until then the UI lives as its
own **linked xwal tree** (a derived cache), which is the next build.

> **The convergence now has a plan and a shared coordinate.** See
> [turn-addressing.md](turn-addressing.md). Both IRs carry a **turn id**; **LT
> joins, turn id addresses**. The open debate below ("should a live message be a
> whole turn?") is **resolved: yes** — the UI IR unit is the turn, prompt and
> reply together.

## The drift, measured

`content` blocks and `nodes` are the same *concept* but have never been the
same *thing*. The mapping is not 1:1 in either direction:

| fig IR content | → UI IR nodes |
|---|---|
| `tool_invoke` (assistant msg) **+** `tool_result` (user msg) | **1** `tool` node — 2 blocks, 2 *messages*, 1 node |
| empty `prose` / `thinking` | **0** — skipped by `TrimSpace(c.Text) == ""` |
| `image` | **0** — no case in the `compose.Nodes` switch |
| `interrupt` | **0** — no case |
| `prose` inside a `tool_result` message | **1** `steering` node — no content counterpart |

Vocabularies overlap by half: content is `prose, image, thinking, tool_invoke,
tool_result, interrupt` (6); nodes are `prose, thinking, tool, steering` (4);
shared: `prose, thinking`.

**What turn ids fix.** The node id was doing double duty — a synthesized
`"<lt>.<blockIdx>"` address for prose/thinking, an opaque provider receipt for
tools, and nothing at all for the user's prompt. Under turn addressing the node
id is a plain per-turn ordinal, `lts` carries provenance for every node type
including tools, and the user's question is `Turn.Inquiry` — text on the turn —
rather than an identity-less node. The cardinality mismatch above stays — folding a tool's invoke and
result into one node is correct — but it is now *stated* by `lts` instead of
being invisible.

## Answers to the open IR questions

- **Where do tools render — assistant or user message?** The **assistant**
  turn. `compose.Nodes` iterates assistant messages only; a `tool_invoke`
  block becomes one `tool` node in that turn. The user's *prompt* is its own
  unit (one prose node); a user-role **tool_result** tic is NOT its own unit —
  it folds into the assistant turn.
- **How does the UI converge a tool to one item?** fig IR has two events in two
  messages — `tool_invoke` (assistant) and `tool_result` (a later user tic),
  linked by `tool_call_id`. `compose.toolNode` merges them into a single `tool`
  node `{id, name, args, status, output}`: status `running` until the result
  arrives, then `ok`/`error`; output is the streamed/final result text
  (tail-bounded to 200 lines — the full text stays in the canonical IR). So the
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

`model` and `provider` are chalkboard values. The fig IR carries content and
provenance; configuration is read from `system.model` and `system.provider`.
Dormant list metadata comes from the live `_meta` summary, not a second
derived snapshot.

## Future (north star + blockers)

- **Tool calling over a separate channel — the key blocker.** Today a tool is
  "handled via instructions" inside the fig IR (invoke block in one message,
  result tic in another). The intent: the fig IR **encodes a tool** (one block),
  and the tool is *run over a separate channel*, delivering IR updates to the
  **live (open) message** — exactly how the UI streams `NodeDelta`s into an open
  node. This unlocks:
  - **Formalizing the "live" message in the fig IR** the way the UI does: an
    open message with a version, mutated by deltas, then closed. The live state
    can be serialized; on a server crash with a live tail message, it can be
    **discarded or closed** by policy.
  - **Restore correctness from UI tool-state:** because the UI knows, to the
    degree observed, whether a tool was invoked/completed, a separate tool
    channel + that state lets us know **which tools still need handling** on
    restore. (Full fig↔UI convergence then becomes safe.)
- **"message" supplants "tic"; turn vs message. — RESOLVED.** The UI IR unit is
  the **turn**: user inquiry and assistant reply in one entry. Tools and
  steering do not make this hard once nodes carry `lts` — a tool node simply
  spans two LTs, and steering is the same primitive as the prompt at a later
  position. See [turn-addressing.md](turn-addressing.md).

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
  start (or a migration) — fine on this branch.

## Sequence

1. ✅ Answer the IR questions (above).
2. ✅ Adjustments now: `text`→`prose`, rid `tic`.
3. ✅ Document intentions + challenges (this file).
4. ⏭ **UI as a linked xwal tree** (derived cache; rehydratable via `compose`).
   Now scoped by [turn-addressing.md](turn-addressing.md): a `ui` channel whose
   entries are **sealed turns**, `mainLT` = the turn's first LT, with the live
   turn held **in memory** until it seals. `_live` and `turn-wal` both persisted
   the live tail and were both retired for it — do not re-litigate.
5. ⏭ Rebase on `main`; test in full.
6. (later) `model`/`provider` → chalkboard; tool-over-channel; live-message
   formalization; full convergence.
