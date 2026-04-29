# Aria Log Unification — Patches as Conversation History

> **Status:** plan / proposal. No implementation work intended yet. Sits alongside the completed [`cache-control/SYSTEM-REMINDERS.md`](../cache-control/SYSTEM-REMINDERS.md) as the next architectural revision.

## Why this document exists

After Stage 4 of the chalkboard work landed, inspection of the on-disk files for a real aria revealed two problems:

1. **Chalkboard patches have no representation in the aria's conversation log.** The aria.json contains only user/assistant messages with their own baggage. The system-reminder content that the model actually saw at request-time exists nowhere in the IR — it was added at projection-time to the wire-format `nativeRequest` and discarded after the HTTP call.
2. **Logical-time collisions between the two log files.** The chalkboard patch and the first user message both got `logical_time: 1` because they live in separate stores with independent lt spaces.

Concretely, for aria `9d961345`:

```jsonc
// arias/9d961345.json (conversation history)
{
  "next_lt": 5,
  "messages": [
    {"role": "user",      "content": [{"text": "say only the word ciao"}],   "logical_time": 1, ...},
    {"role": "assistant", "content": [{"text": "Ciao."}],                    "logical_time": 2, ...},
    {"role": "user",      "content": [{"text": "say only the word saluti"}], "logical_time": 3, ...},
    {"role": "assistant", "content": [{"text": "Saluti."}],                  "logical_time": 4, ...}
  ]
}

// chalkboards/9d961345/log.json (chalkboard patches)
{"lt":1,"patch":{"set":{"cwd":"/home/gluck/dev/figaro-qua/main","datetime":"..."}}}
```

The user-role IR messages contain only the prompt text. The system-reminder content (`Working directory: /home/gluck/...`, `Current time: ...`) that was actually wrapped in `<system-reminder>` tags and sent to Anthropic does **not** appear in the IR at all. The conversation log doesn't accurately reflect what the model saw.

## Why it works this way (current code)

Trace through `internal/figaro/agent.go`:

1. **`eventUserPrompt` arrives** (line 510) with text + an optional `*rpc.ChalkboardInput`.
2. **`applyChalkboardInput`** (line 534, defined at line 981) computes a patch from the input, persists it to `chalkboard.Store` at `lt = memStore.LeafTime() + 1`, advances `a.cbSnap` in memory, renders the patch via `chalkboard.Render` into `a.cbTurnReminders` ([]RenderedEntry).
3. **The user message is then appended** to `memStore` (line 553) — without any reminder content. The MemStore auto-assigns `nextLT` and increments. For a fresh aria, `nextLT = 1` here, **the same value** that line 1023 computed for the chalkboard patch.
4. **`startLLMStream`** is called with `a.cbTurnReminders`.
5. **`Provider.Send`** is called. Inside `internal/provider/anthropic/anthropic.go`:
   - `projectBlockWithModel` projects `block.Messages` (without reminders) into `nativeRequest`.
   - **`applyRenderer(req, reminders)`** mutates `req` *in place*: `renderTag` appends `<system-reminder>` content blocks to `req.Messages[len-1]`, the leaf user message in the wire payload.
   - The HTTP request is sent.
6. **The response is captured** as a new assistant `message.Message`. Its `Baggage["anthropic"]` is populated with the *response*'s native form. The *request*-side mutation to `req.Messages` is never propagated back to `block.Messages`.

So the answer to "why are there no system-reminders in the aria baggage": **system-reminders live only in the wire-time `nativeRequest` and are discarded after the HTTP call.** They never enter the IR. Each subsequent turn re-renders from `cbTurnReminders` (or skips entirely if the chalkboard didn't change). The conversation log is missing the content that the model actually saw on the previous turn.

## Why this is wrong

Three reasons:

1. **The IR isn't faithful to the conversation.** Anyone reading aria.json would think the user said only `"say only the word ciao"` — but the model also received `<system-reminder name="cwd">Working directory: /home/gluck/...</system-reminder>` and `<system-reminder name="datetime">Current time: ...</system-reminder>` as additional content blocks on that same user message. The aria log is silently incomplete.

2. **Logical time collisions.** Two distinct events at `lt=1` is corrupt-by-construction. If we ever try to merge the two streams chronologically — e.g. for replay, debugging, or any future feature that needs ordered history — we hit ambiguity.

3. **No baggage cache for patch projections.** Every send re-renders chalkboard patches from raw values (~10µs total, fine in absolute terms). But more importantly, switching from baggage-based projection (which messages already use) to fresh-rendering for the patch portion of the wire format means we can't take advantage of the same prefix-stability discipline that makes the message baggage round-trip cache-friendly.

## Proposed redesign

The user's framing is the correct one: **patches are conversation history.** They mark state transitions that occur at specific points in the timeline; the model sees them folded into the wire format as system-reminder content at the same logical position. They should live in the same log, share the logical-time space, and carry per-provider baggage exactly as messages do.

### Type unification

`Block.Messages []Message` becomes `Block.Entries []LogEntry`. `LogEntry` is a sum/union type:

```go
// internal/message/message.go (proposed)

// EntryKind discriminates log entries.
type EntryKind string

const (
    EntryKindMessage EntryKind = "message"
    EntryKindPatch   EntryKind = "patch"
)

// LogEntry is one ordered entry in an aria's history. It is either a
// conversational Message or a chalkboard Patch. Both share LogicalTime,
// Timestamp, and per-provider Baggage.
type LogEntry struct {
    Kind        EntryKind                  `json:"kind"`
    LogicalTime uint64                     `json:"lt"`
    Timestamp   int64                      `json:"ts"`
    Baggage     map[string]json.RawMessage `json:"baggage,omitempty"`

    // Exactly one of these is non-nil.
    Message *Message `json:"message,omitempty"`
    Patch   *Patch   `json:"patch,omitempty"`
}

// Patch is the chalkboard delta. Moved here from internal/chalkboard/
// so it lives in the IR alongside Message. The chalkboard package
// continues to own snapshot management and rendering.
type Patch struct {
    Set    map[string]json.RawMessage `json:"set,omitempty"`
    Remove []string                   `json:"remove,omitempty"`
}
```

`Block` becomes:

```go
type Block struct {
    Header  *Message    // compacted summary header (unchanged)
    Entries []LogEntry  // ordered log of messages + patches
}
```

A helper `Block.Messages() []Message` filters to message-kind entries for callers that don't care about patches (e.g. the existing `Context()` consumers).

**Why two types in a union rather than one type with a role?** A `Patch` doesn't have a role, doesn't have content blocks, doesn't have a stop reason. Trying to shoehorn it into `Message` either bloats `Message` or misuses it. Two cleanly distinct shapes with a discriminator is more honest.

### Storage layout

```
~/.local/state/figaro/arias/
├── {id}/
│   ├── aria.jsonl          # NDJSON log: one LogEntry per line
│   └── chalkboard.json     # cached current snapshot (derived; rebuildable from log)
```

- **aria.jsonl** is the source of truth. Append-only NDJSON; one `LogEntry` per line. Each line is self-contained JSON.
- **chalkboard.json** is a derived cache: the current snapshot, written at endTurn boundaries. Existence is a performance optimization for cold load; correctness does not depend on it.

The current paths retire:
- `arias/{id}.json` (single big JSON object) — replaced by `arias/{id}/aria.jsonl`.
- `chalkboards/{id}/log.json` and `chalkboards/{id}/snapshot.json` — replaced by `arias/{id}/chalkboard.json` (snapshot only). The patch log is no longer separate; patches are entries in the unified aria log.

Migration on cold load: read old-format `arias/{id}.json` (if it exists), interleave with old `chalkboards/{id}/log.json` patches by logical time, write out as new-format `arias/{id}/aria.jsonl`, and delete the old files. One-time per aria, idempotent.

### Provider projection

The render-and-stash pattern that messages already use for baggage extends naturally to patches:

1. **First projection of a patch:** the renderer (e.g. `renderTag`) computes the wire-format content blocks the patch produces and stashes them in `entry.Baggage["anthropic"]`. The wire-format request gets these blocks attached to the appropriate position. The IR `LogEntry` now carries the projection result alongside the patch itself.
2. **Subsequent projection of the same patch:** baggage is present, so the renderer skips computation and reads directly from baggage — same path messages already use.

For the tag renderer, baggage might look like:

```json
{
  "anthropic": {
    "attach_to": "next_user",
    "blocks": [
      {"type": "text", "text": "<system-reminder name=\"cwd\">Working directory: /home/gluck/...</system-reminder>"},
      {"type": "text", "text": "<system-reminder name=\"datetime\">Current time: ...</system-reminder>"}
    ]
  }
}
```

For tool-injection:

```json
{
  "anthropic": {
    "attach_to": "after_next_user",
    "messages": [
      {"role": "assistant", "content": [{"type": "tool_use", ...}]},
      {"role": "user",      "content": [{"type": "tool_result", ...}]}
    ]
  }
}
```

The shape can be tightened later; the principle is that baggage records *what was actually sent* so re-projection is byte-stable across turns.

**Renderer-change behavior.** If a user changes `reminder_renderer: "tag" → "tool"` mid-aria, the existing baggage on past patches is preserved (those turns happened that way). New patches get rendered with the new renderer. The wire format becomes mixed-renderer for that aria's history; the conversation remains internally consistent.

### Projection algorithm

Rough sketch for the Anthropic provider:

```go
func (a *Anthropic) project(entries []LogEntry, ...) nativeRequest {
    // Group: each user message accumulates "pending" content blocks
    // from any preceding patch entries that target it.
    var pending []nativeBlock
    var result []nativeMessage
    for _, e := range entries {
        switch e.Kind {
        case EntryKindPatch:
            // Render or read from baggage; queue blocks for the next user message.
            blocks := a.renderPatchBlocks(&e) // populates baggage if missing
            pending = append(pending, blocks...)
        case EntryKindMessage:
            native := a.projectMessage(*e.Message)
            if native.Role == "user" && len(pending) > 0 {
                native.Content = append(native.Content, pending...)
                pending = nil
            }
            result = append(result, native)
        }
    }
    // ...trailing patches with no following user message: attach to the
    // leaf if it's a user message; otherwise append a synthetic user
    // turn (current behavior). This is rare in practice.
    return nativeRequest{Messages: result, ...}
}
```

The renderer becomes a pure function: `(*LogEntry) → []nativeBlock`, populating `entry.Baggage["anthropic"]` on miss. Cache breakpoints (`markCacheBreakpoints`) continue to attach to the second-to-last message — the leaf-1 logical position, which now might be a user message that *includes* attached system-reminder blocks. That's still byte-stable across turns when the patch baggage is stable.

### Logical-time allocation

Both messages and patches must come from the **same** monotonic counter. Today the chalkboard log uses `LeafTime() + 1` and the message store uses `nextLT++` independently — this is the source of the collision.

Proposed: route both through `MemStore.AllocLT()` which returns and increments `nextLT`. Patches get `lt = AllocLT()` before the user message that follows, so the patch slots in cleanly between turns:

```
lt=1: patch (cwd, datetime set)
lt=2: user message ("ciao")
lt=3: assistant message ("Ciao.")
lt=4: user message ("saluti")  -- no patch this turn since chalkboard unchanged
lt=5: assistant message ("Saluti.")
```

### Debug-mode reconciliation

Per the user's preference: at every endTurn flush, write the current snapshot. **In debug mode**, also walk the aria.jsonl log, replay all patches in order to reconstruct the snapshot independently, and assert it matches the cached one. Log a warning on divergence; don't crash.

Configuration: `FIGARO_DEBUG=1` env var, or `[debug] reconcile_chalkboard = true` in config.toml. Default off (snapshot is trusted; reconciliation costs a full log walk per turn).

```go
// In endTurn, after writing snapshot:
if debug.ReconcileChalkboard {
    rebuilt, err := chalkboard.ReplayFromLog(a.entries())
    if err != nil {
        log.Printf("debug: chalkboard replay error: %v", err)
    } else if !snapshotEqual(rebuilt, a.cbSnap) {
        log.Printf("debug: chalkboard snapshot diverges from log replay; investigate")
    }
}
```

### Watermarking (deferred)

Long-running arias accumulate patches. To avoid replaying the entire log on cold load, we eventually want **periodic watermarks**: at each (configurable) interval, write a checkpoint that captures the snapshot as of logical time N. Cold load reads the most recent checkpoint and replays only entries with `lt > N`.

This is straightforward but not urgent. The current snapshot file is already a watermark-of-one — it captures the snapshot as of the last endTurn. Watermarks generalize this to "as of any prior boundary" with the snapshot file potentially lagging by a configurable number of turns. Not in scope for the unification work; flag for future.

## Implementation stages

Each stage commits independently. The plan is conservative: shape the types first, migrate storage, then rewire projection. Tests verify behavior at each step.

### Stage A — IR type changes

**Goal:** `LogEntry`, `Block.Entries`, helper accessors. No storage or projection changes yet.

**Deliverables:**
- `internal/message/message.go`: `LogEntry`, `EntryKind`, `Patch` (moved from `internal/chalkboard/`).
- `internal/message/message.go`: `Block.Entries []LogEntry`; `Block.Messages() []Message` filter; `Block.Patches() []Patch` filter; deprecate direct `Block.Messages` field access.
- `internal/chalkboard/chalkboard.go`: re-export `Patch` from `message` for compatibility, mark old definition as deprecated.
- All call sites that read `Block.Messages` updated to call `Block.Messages()` (or migrated to iterate `Block.Entries` if they need patches).

**Tests:** existing tests still pass; `Block.Messages()` returns the same slice as before for blocks that contain only message entries.

### Stage B — MemStore + FileStore unified log

**Goal:** the in-memory and on-disk stores hold `LogEntry` slices, not just `Message` slices. Logical-time allocation goes through one counter.

**Deliverables:**
- `internal/store/mem.go`: `Append(entry LogEntry)` (was `Append(msg Message)`); `AllocLT() uint64`; `Entries()` accessor.
- `internal/store/file.go`: NDJSON serialization (`aria.jsonl`); each line is one `LogEntry`. Atomic-rename pattern adapted: write all entries to `aria.jsonl.tmp`, rename. (Append-only with fsync is an optimization for later; for now we keep the rewrite-on-flush model but with NDJSON shape.)
- `internal/store/store.go`: interface updated.
- Migration on cold load: detect old `arias/{id}.json` format, convert to `arias/{id}/aria.jsonl`, delete old file. One-shot, idempotent.

**Tests:** restore from old format produces identical `Block.Entries` (modulo patch interleaving — see Stage C). New writes go to `arias/{id}/aria.jsonl`.

### Stage C — Patches enter the log

**Goal:** `applyChalkboardInput` writes a `Patch` LogEntry to MemStore at the same `lt` boundary as the next message, instead of a separate chalkboard log.

**Deliverables:**
- `internal/figaro/agent.go`: `applyChalkboardInput` allocates `lt` via `MemStore.AllocLT()`, builds a `LogEntry{Kind: EntryKindPatch, ...}`, appends to MemStore. The chalkboard `Store.Append` call is removed.
- `internal/chalkboard/store.go`: `Store` interface narrows. `Append` is gone (patches live in the message store now). `Snapshot` and `SaveSnapshot` remain — the snapshot file is now `arias/{id}/chalkboard.json`. `chalkboard.Store` becomes a thin wrapper around the snapshot file.
- `internal/figaro/agent.go`: cold-load path reads the snapshot file; if missing, replays patches from the log.
- Migration: on first load of an old-format aria, the chalkboard log entries are converted to `LogEntry{Kind: EntryKindPatch}` and interleaved into the message log at their original `lt` slots. Deletes `chalkboards/{id}/` afterward.

**Tests:** after a turn with chalkboard input, `aria.jsonl` contains a Patch entry alongside the user/assistant messages. Logical times are unique. Cold-load reconstruction matches the snapshot file.

### Stage D — Projection rewrite

**Goal:** `applyRenderer` becomes a pure projection-time function that reads/writes `entry.Baggage["anthropic"]`. The agent's `cbTurnReminders` field disappears.

**Deliverables:**
- `internal/provider/anthropic/anthropic.go`: `projectBlockWithModel` walks `Block.Entries` (was `Block.Messages`). For Patch entries, calls `renderPatchBlocks(entry)` which uses baggage if present, computes-and-stashes if not.
- `internal/provider/anthropic/render.go`: `renderTag` and `renderTool` become pure functions on `*LogEntry`; populate baggage as a side effect.
- `internal/figaro/agent.go`: remove `cbTurnReminders`; remove the `[]chalkboard.RenderedEntry` parameter from `Provider.Send`. The provider has all the information it needs from `block.Entries`.
- `internal/provider/provider.go`: `Send` signature drops `[]chalkboard.RenderedEntry`.

**Tests:** wire-payload byte-equality across two consecutive turns with the same chalkboard state (no IR changes triggered). New patch on turn 2 produces baggage on the patch entry; turn 3 with no chalkboard change re-uses that baggage. The user-facing tag-rendered text appears in the persisted IR via the patch's baggage.

### Stage E — Debug-mode reconciliation

**Goal:** opt-in log-replay verification.

**Deliverables:**
- `internal/figaro/agent.go`: `endTurn` snapshot save is unchanged. When `FIGARO_DEBUG_RECONCILE=1`, after the snapshot is written, replay all patch entries from `block.Entries` and compare to the saved snapshot. Log a warning on divergence.
- `internal/chalkboard/replay.go`: `ReplaySnapshot(entries []LogEntry) Snapshot` — pure function, walks patches in lt order.

**Tests:** induced corruption (test-only path) of the snapshot file produces a divergence warning on the next endTurn.

### Stage F — Tests + benchmarks + docs

- Unit tests for `LogEntry` ordering, baggage round-trip on patches, projection algorithm with mixed message/patch streams.
- Benchmarks for the unified projection: should be no slower than current (baggage cache means repeated projections are O(n) reads instead of O(n) renders).
- Update `docs/system-reminders/audit.md` to reflect the unified shape (the audit's Stage 4 design is partially superseded).
- Update `agents.md` and `ARCHITECTURE.md` for the new on-disk layout.
- CHANGELOG entry covering the storage migration.

## Open questions

1. **Strict-NDJSON vs lazy-NDJSON for aria.jsonl.** Strict NDJSON allows append-without-rewrite (fsync each line, restore is O(file size)). Lazy NDJSON keeps the current rewrite-tmp-rename atomicity at the cost of full rewrites on every endTurn. The latter is simpler and consistent with the chalkboard.json snapshot file; the former is the right shape for sqlite migration later. Recommend lazy for v1, strict for the watermarking work.

2. **Baggage shape for the tool-injection renderer.** Tool injection produces a *pair* of synthetic messages, not content blocks attached to an existing message. The baggage shape needs to express "after this patch, insert these two synthetic native messages into the wire stream." Slightly different from the tag renderer's "attach these content blocks to the next user message." Worth sketching the exact baggage union before Stage D.

3. **What about the `Header` field?** Compaction (future) writes a summarized message into `Block.Header`. Should that be a `LogEntry` too? Likely not — Header isn't a timeline event, it's a state summary. Leaving it as `*Message` is consistent.

4. **Renderer change mid-aria.** Already discussed above (existing baggage preserved, new patches rendered with new renderer). Tests should cover this explicitly so the behavior is locked in.

5. **What happens to `chalkboard.Store` as an interface?** After Stage C it does almost nothing — just the snapshot file. Worth folding it into `store.Backend` or removing it entirely, replaced by direct `os.ReadFile`/`os.WriteFile` calls in the agent. Decide during Stage C.

6. **Patch lt placement: before-or-after the user message?** Today the patch lt is computed *before* `Append(userMsg)`, so the patch slot would naturally come before the user message in the unified log:
   ```
   lt=1: patch
   lt=2: user message
   ```
   This matches the conceptual "state changes, then user speaks under that new state" ordering. Confirm this is the intended ordering in Stage C; the alternative (user message first, patch attached as metadata) would change semantics.

7. **Multi-provider baggage on patches.** A patch's baggage map (`map[string]json.RawMessage`) supports multiple providers, just like message baggage. When switching providers mid-aria, the new provider sees no baggage and re-renders. This is consistent with the existing message-baggage behavior and probably correct. Worth a test.

## Relationship to the cache-control work

The cache-control plan ([`../cache-control/SYSTEM-REMINDERS.md`](../cache-control/SYSTEM-REMINDERS.md)) is largely complete; the prefix-stability invariant is enforced and the `cache_control` wire is correctly placed. This unification proposal is a follow-on refinement, not a redirection: it makes the persisted IR faithful to what the model actually saw, fixes the lt-collision bug, and unifies storage in a way that aligns with the eventual sqlite migration.

The byte-stability of the cache prefix is unaffected by this work — patch baggage that re-projects to identical bytes is exactly what the existing cache-control wiring expects. If anything, the unified projection makes prefix stability *easier* to reason about (all wire content is derivable from IR baggage; nothing comes from ephemeral renderer state).
