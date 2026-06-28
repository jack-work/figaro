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

## Planned near-term

- **`model`/`provider` are chalkboard values.** Today they sit on
  `message.Message` (set per assistant turn in `figaro/agent.go`, read in
  `figaro/derived.go` for list/meta). They should live in the chalkboard
  (`system.model` / `system.provider` already exist as loadout stamps) and be
  **derived on read via `ChalkboardState(LT)`** — removing `Message.Model` /
  `Message.Provider`. Net: the fig IR turn carries content + provenance only;
  configuration is the chalkboard's job. (Touches the agent write path + the
  derived/meta read path; do deliberately.)

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
- **"message" supplants "tic"; turn vs message.** The UI `message` primitive
  should replace the tic concept. Open debate: should a live *message* be a
  whole **turn** (user inquiry + assistant reply as one unit)? Tools and
  steering make that hard (a turn isn't a clean request/response). For now keep
  the `message` unit; revisit — and **compact first** before reshaping units.

## Challenges to call out

- Tool **lifecycle mismatch**: fig IR = invoke + result (two events, two
  messages); UI = one node with a status flip. Converging needs the
  tool-over-channel model above.
- **Steering** mid-turn breaks a clean request/response turn boundary.
- Reshaping the unit (turn-as-message) wants **compaction** in place first.
- The wire `type` value changed (`text`→`prose`); existing stores need a fresh
  start (or a migration) — fine on this branch.

## Sequence

1. ✅ Answer the IR questions (above).
2. ✅ Adjustments now: `text`→`prose`, rid `tic`.
3. ✅ Document intentions + challenges (this file).
4. ⏭ **UI as a linked xwal tree** (derived cache; the persisted form is the
   `NodeDelta` wire; rehydratable via `compose`).
5. ⏭ Rebase on `main`; test in full.
6. (later) `model`/`provider` → chalkboard; tool-over-channel; live-message
   formalization; full convergence.
