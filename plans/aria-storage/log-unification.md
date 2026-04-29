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

### Stage C — Patches enter the log; `chalkboard.Store` retires

**Goal:** `applyChalkboardInput` writes a Patch LogEntry to MemStore at `lt` immediately preceding the user message it triggered. The chalkboard.Store interface is replaced by a per-aria value handle.

**Deliverables:**
- `internal/chalkboard/aria.go` (NEW): `*Aria` per-aria handle owning the in-memory snapshot + the `arias/{id}/chalkboard.json` cache file. `Open(rootDir, ariaID) (*Aria, error)`, `(*Aria).Snapshot()`, `(*Aria).Apply(Patch)`, `(*Aria).Save()`, `(*Aria).Close()`. Atomic write-tmp + rename for Save. See Resolved Question R5.
- `internal/chalkboard/store.go` (DELETED): `Store` interface, `FileStore`, the separate patch-log machinery — all gone. Patch persistence is now the message store's responsibility (LogEntry of kind Patch).
- `internal/figaro/agent.go`:
  - Replace `cb chalkboard.Store` + `cbSnap chalkboard.Snapshot` fields with a single `aria *chalkboard.Aria`.
  - `applyChalkboardInput`: allocate `lt` via `MemStore.AllocLT()`, build `LogEntry{Kind: EntryKindPatch, LogicalTime: lt, ...}`, call `MemStore.Append(entry)`. Then call `a.aria.Apply(patch)` to advance in-memory state. The user message is appended next via `MemStore.Append(msg)` at the next allocated lt — patch and user message are appended atomically as part of one event handler so no log observer sees one without the other (R6).
  - `endTurn`: call `a.aria.Save()` alongside `MemStore.Flush()`.
  - `Kill`: call `a.aria.Close()`.
- `internal/angelus/protocol.go`: construction sites replace `Chalkboard chalkboard.Store` with `*chalkboard.Aria` (opened per aria). Restoration path opens an Aria for each restored aria.
- `cmd/figaro/main.go`: `buildChalkboard` returns just templates; `Aria` opening moves to per-agent construction in the angelus.
- Migration: on cold load of an old-format aria, read the legacy `chalkboards/{id}/log.json`, convert each patch entry to a `LogEntry{Kind: EntryKindPatch}` interleaved into `arias/{id}/aria.jsonl` at its original `lt` slot, then move `chalkboards/{id}/snapshot.json` → `arias/{id}/chalkboard.json` and delete the old `chalkboards/{id}/` directory.

**Tests:**
- After a turn with chalkboard input, `aria.jsonl` contains a Patch entry at `lt=N` followed by a user message at `lt=N+1`. Logical times are unique (no Stage-3 collision regression).
- Cold-load with existing `aria.jsonl` reconstructs the same `Block.Entries` and the same chalkboard snapshot.
- Migration test: synthesize a legacy-format aria + chalkboard pair on disk; cold-load; assert the unified format on disk afterward and identical in-memory state.

### Stage D — Projection rewrite (variadic baggage; renderer-as-pure-visitor)

**Goal:** Renderers become pure projection-time functions of type `f(*LogEntry) → []json.RawMessage` that populate `entry.Baggage[providerName]` on cache miss. The projection layer handles same-role coalescing for providers that need alternation. The agent's `cbTurnReminders` field and the `[]chalkboard.RenderedEntry` parameter on `Provider.Send` both go away.

**Baggage shape (per R1).**

```go
// internal/message/baggage.go (NEW)
type Baggage map[string][]json.RawMessage  // provider name → ordered list of wire messages
```

A flat list, no positional metadata. One LogEntry's wire output for one provider is `Baggage[provider]`. Length 1 for typical messages; length 0 for entries that produce no output for the active provider; length 2+ for tool-injection-rendered patches.

**Deliverables:**
- `internal/message/baggage.go` (NEW): the `Baggage` type. Defined as `map[string][]json.RawMessage` for explicit variadic-per-provider semantics.
- `internal/message/message.go`: replace `Message.Baggage map[string]json.RawMessage` with `Message.Baggage Baggage` (and same for `LogEntry.Baggage`). Migration: existing serialized message baggage (`{"anthropic": <nativeMessage>}`) reads as a malformed `[]json.RawMessage`; the unmarshal helper detects single-object form and wraps to `[<nativeMessage>]`. Forward writes always use the list form.
- `internal/provider/provider.go`: `Send(ctx, *Block, []Tool, maxTokens)` — `[]chalkboard.RenderedEntry` parameter removed.
- `internal/provider/anthropic/anthropic.go` — `projectBlockWithModel` becomes a uniform visitor:

  ```go
  // Pseudo-code; actual signature passes block + active provider.
  for _, entry := range block.Entries {
      wireMessages := entry.Baggage["anthropic"]
      if wireMessages == nil {
          // Cache miss: render or project from IR.
          switch entry.Kind {
          case EntryKindMessage:
              wireMessages = []json.RawMessage{projectMessage(entry.Message)}
          case EntryKindPatch:
              wireMessages = a.renderer(entry)  // tag or tool
          }
          entry.Baggage["anthropic"] = wireMessages  // populate cache
      }
      // Append all wire messages from this entry.
      for _, m := range wireMessages {
          accum = append(accum, decodeNativeMessage(m))
      }
  }
  // Coalesce consecutive same-role messages into one with combined content.
  req.Messages = coalesceSameRole(accum)
  ```
- `internal/provider/anthropic/render.go`:
  - `renderTag(*LogEntry) []json.RawMessage` — pure function of the patch alone. Returns one wire message: a user-role native message containing one or more `<system-reminder>` text content blocks. The projection's coalesce pass merges this into the next user message if one follows.
  - `renderTool(*LogEntry) []json.RawMessage` — pure function. Returns two wire messages: an assistant message with `tool_use` content blocks (one per reminder key) and a user message with matching `tool_result` content blocks. The user-side message coalesces with the next user prompt naturally.
  - Both renderers know nothing about ordering or surrounding entries.
- `internal/provider/anthropic/coalesce.go` (NEW): `coalesceSameRole(msgs []nativeMessage) []nativeMessage` — walks the slice, merges adjacent entries with the same role into one with concatenated content blocks. Pure function; trivially testable.
- `internal/figaro/agent.go`: remove `cbTurnReminders`; remove `applyChalkboardInput`'s render call. The agent no longer renders — that's the provider's job now, on cache miss.

**Tests:**
- Wire-payload byte-equality across two consecutive turns with the same chalkboard state: turn 1 populates `patch.Baggage["anthropic"]`; turn 2 hits the cache; bytes match.
- Tool-injection variadic case: a patch with two reminder keys produces a 2-message `Baggage["anthropic"]` slice (assistant tool_use + user tool_result). Both messages survive baggage round-trip.
- Coalesce: a tag-rendered patch (single user wire message with reminder content) immediately followed by the actual user-prompt entry produces ONE user wire message in the final request, with both content sets merged.
- Coalesce edge: tag-rendered patch followed by an assistant-role entry — no coalesce; both messages emitted as-is.
- Renderer-change mid-aria (Q4 / R4 above): existing baggage is sticky. Switching `reminder_renderer` on the running agent does not invalidate past patch baggage; the next render-cache-miss (a fresh patch) uses the new renderer.
- Multi-provider baggage (R7): provider A populates `Baggage["a"]`; switch to B; B sees no `Baggage["b"]`, re-renders, populates `Baggage["b"]`. Both keys coexist on the same entry.
- **The user-facing system-reminder text now appears in the persisted IR via `LogEntry.Baggage["anthropic"]`** — directly answering the original "why don't system-reminders show up in the aria" question.

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

## Resolved questions (concrete decisions)

The following questions have been answered. Recording the decisions so future implementers don't relitigate them.

### R1. Variadic baggage — wire messages only, no positional metadata

A LogEntry's per-provider baggage is **a list of complete wire-format messages**. No "attach to surrounding" semantics; no positional hints. The renderer is a pure function of `*LogEntry → []json.RawMessage` that doesn't know or care what entries surround it. Whatever ordering or alternation cleanup a provider's API requires happens at the projection step, *after* the renderer has produced its baggage.

```go
// internal/message/baggage.go (proposed)

// Baggage records the wire-format outputs of a LogEntry, keyed by
// provider name. The value is variadic — one LogEntry may produce
// multiple wire-format messages for one provider:
//
//   - A regular Message LogEntry: typically 1 wire message.
//   - A chalkboard Patch under tool-injection rendering: 2 wire
//     messages (assistant tool_use + user tool_result).
//   - A chalkboard Patch under tag rendering: 1 wire message
//     (a user-role message with <system-reminder> content). The
//     projection layer handles alternation by coalescing
//     consecutive same-role messages into one with multiple
//     content blocks; that is *not* the renderer's job.
//
// Treated as a cache: read on hit, populated by the renderer on miss.
// Multi-provider on the same LogEntry is supported and expected — see Q7.
type Baggage map[string][]json.RawMessage
```

**Why this shape.** A previous draft of this plan had `ProviderBaggage{Messages, AttachToNext}` to carry both "wire messages" and "content blocks to attach to the next user message." The AttachToNext field was rejected: ordering metadata should not live in the IR. Each entry's baggage is fully self-contained; the wire-stream cleanup is the provider's job.

**Alternation handling at the projection layer.** Anthropic's API requires strict user/assistant alternation in `messages`. Some renderer outputs collide naturally — e.g. a tag-rendered patch produces a user-role message containing system-reminder text, and the chalkboard-patch LogEntry sits immediately before a user-prompt LogEntry, so the wire stream has two consecutive user messages. The projection step resolves this by **coalescing consecutive same-role messages** into a single message with the union of their content blocks. This is the property of the projection algorithm, not of any renderer.

```
Wire stream out of renderers:
  user(reminder block) → user(prompt block) → assistant(reply)
After coalesce-same-role pass:
  user(reminder + prompt) → assistant(reply)
```

Tool-injection renderer collisions get handled the same way — the synthetic `user(tool_result)` followed by the actual `user(prompt)` coalesces into one user message containing both. Anthropic accepts this readily; multiple content blocks per message is the wire format's natural shape.

This also means the projection algorithm itself is a uniform visitor: walk `Block.Entries`, for each entry produce baggage (read or render), accumulate, finally apply the coalesce pass. No per-entry-kind branching at the high level.

### R5. `chalkboard.Store` retires; replaced by `*chalkboard.State` (per-aria handle)

After Stage C (patches enter the unified log), the chalkboard's only remaining persistence concern is the snapshot cache file. The agent already holds the in-memory snapshot (`a.cbSnap`) separately from the persistence interface (`a.cb`). The right consolidation is to merge those two concerns into a single per-aria value type:

```go
// internal/chalkboard/state.go (proposed)

// State is a per-aria chalkboard state handle. Owns the in-memory
// current snapshot and its on-disk cache file. Lifetime is bound to
// one Agent. Single-owner — safe for use by the agent's drain-loop
// goroutine without locking (the actor model serializes access).
type State struct {
    snapshot Snapshot
    path     string  // arias/{id}/chalkboard.json
    dirty    bool
}

// Open loads the snapshot for an aria from disk. Returns a State
// pre-populated with the persisted state, or an empty State if
// no snapshot file exists yet (cold-start).
func Open(path string) (*State, error)

// Snapshot returns a clone of the current in-memory state. Callers
// may not mutate; the returned value is safe to retain.
func (s *State) Snapshot() Snapshot

// Apply mutates the in-memory snapshot by applying p. Marks dirty.
// Returns the post-apply snapshot for caller convenience.
func (s *State) Apply(p Patch) Snapshot

// Save flushes the snapshot to disk if dirty. Atomic via
// rewrite-tmp-rename. Idempotent.
func (s *State) Save() error

// Close calls Save, then releases. After Close, methods panic.
func (s *State) Close() error
```

**Naming.** Originally proposed as `chalkboard.Aria`; renamed to `chalkboard.State` because "Aria" is already the conceptual name for the persistent conversation as a whole. There is no Go type called "Aria" — the Agent is the aria's runtime representation, and the on-disk directory `arias/{id}/` is its persistence. Calling the chalkboard handle "Aria" overloads the term.

**Vocabulary.** Settled below for clarity in the rest of this plan and in code:

- **Aria** — the *concept*: a persistent conversation identified by an ID. Lives on disk under `arias/{id}/`. Not a Go type.
- **Agent** — the *in-memory runtime* of one active aria. One Agent ↔ one aria. Owns `memStore` (conversation log) and `chalkboard.State` (chalkboard handle).
- **`chalkboard.State`** — the per-aria chalkboard handle. In-memory snapshot + path to `arias/{id}/chalkboard.json` + dirty flag.

```
                On disk                                In memory
                ─────────────────────                  ──────────────────────────
arias/{id}/                                            Agent
├── aria.jsonl   ◀── source of truth ──▶              ├── memStore.Entries  (mirror of aria.jsonl)
└── chalkboard.json ◀── derived cache ─▶              └── chalkboard.State
                  (rebuildable from log)                   ├── snapshot      (mirror of chalkboard.json)
                                                           ├── path
                                                           └── dirty
```

**Why a value type rather than an interface.** Exactly one implementation. Adding an interface for testability is unnecessary — the value can be constructed in tests against `t.TempDir()` like every other file-backed type in the codebase. Interfaces are for swap points (sqlite swap-in, alternate backends); we don't have one yet for chalkboard state.

**Lifecycle.** `Open` in `NewAgent`, `Close` in `Kill`. The State is field-embedded as `Agent.chalkboard *chalkboard.State`. The agent's drain-loop goroutine is the only thing that touches it (via the actor model), so no mutex is needed inside State; `dirty` is a plain `bool`, `snapshot` is a plain map.

`applyChalkboardInput` calls `a.chalkboard.Apply(patch)`; `endTurn` calls `a.chalkboard.Save()`; `Kill` calls `a.chalkboard.Close()`. The agent's previous `cbSnap` and `cb` fields disappear.

### R5. `chalkboard.Store` retires; replaced by `*chalkboard.Aria`

After Stage C (patches enter the unified log), the chalkboard's only remaining persistence concern is the snapshot cache file. The agent already holds the in-memory snapshot (`a.cbSnap`). The right consolidation is to fold that field plus the file-persistence operations into a single per-aria handle:

```go
// internal/chalkboard/aria.go (proposed)

// Aria is a per-aria chalkboard state handle. Owns the in-memory
// current snapshot and its on-disk cache file. Single-owner —
// safe for use by the agent's drain-loop goroutine without locking
// (the actor model serializes access).
type Aria struct {
    id       string
    snapshot Snapshot
    path     string  // path to arias/{id}/chalkboard.json
    dirty    bool
}

// Open loads the snapshot for an aria from disk. Returns an Aria
// pre-populated with the persisted state, or an empty Aria if
// no snapshot file exists yet (cold-start).
func Open(rootDir, ariaID string) (*Aria, error)

// Snapshot returns the current in-memory state. The returned value
// is a clone; callers may not mutate it.
func (a *Aria) Snapshot() Snapshot

// Apply mutates the in-memory snapshot by applying p. Marks dirty.
// Returns the post-apply snapshot for the caller's convenience.
func (a *Aria) Apply(p Patch) Snapshot

// Save flushes the in-memory snapshot to disk if dirty. Idempotent.
// Atomic: write-tmp + rename.
func (a *Aria) Save() error

// Close flushes any pending writes and releases the handle.
func (a *Aria) Close() error
```

The agent's `cbSnap` and `cb` fields collapse into one `*chalkboard.Aria`. `applyChalkboardInput` calls `aria.Apply(patch)`; `endTurn` calls `aria.Save()`; `Kill` calls `aria.Close()`. The `chalkboard.Store` interface goes away.

Lifecycle: opened in `NewAgent`, closed in `Kill`. One `*Aria` per agent, lifetime-bound. No multi-aria handle juggling — figaro agents are 1:1 with arias.

Why a value type rather than an interface: there is exactly one implementation. Adding an interface for testability is unnecessary — the value can be constructed in tests against a `t.TempDir()` like every other file-backed type in the codebase. Interfaces are for swap points (sqlite swap-in, alternate backends); we don't have one yet.

### R6. Patch lt placement — before the message it precedes

A patch lands in the log **before** the user message that triggered its application. Conceptually: state changes, then the user speaks under that new state. The patch reads as a comment annotating the turn it precedes.

```
lt=1: patch (cwd, datetime set)
lt=2: user message ("ciao")
lt=3: assistant message ("Ciao.")
lt=4: user message ("saluti")           ← no patch this turn (chalkboard unchanged)
lt=5: assistant message ("Saluti.")
```

If a prompt RPC carries a patch (or a context bag from which a non-empty diff is computed), the resulting patch entry inserts at `lt = MemStore.AllocLT()` immediately followed by the user message at the next allocated lt. The two are appended atomically as part of the same `eventUserPrompt` handling — no observer of the log can see one without the other.

This is purely a logical-time / ordering decision. It has no semantic implications for the renderer (which is a pure function of the patch alone) or for the projection layer (which walks entries in lt order regardless). The "patch precedes its triggering user message" rule exists for human readability of the log and for invariants downstream tools may rely on (e.g., debugging tools that pair patches with the turn that caused them).

## Resolved-with-elaboration

### R4. Baggage IS a cache (current behavior; extending the same model to patches)

The user asked: "is baggage treated as a cache as I expect or is the code handling it differently? At what point is the baggage currently fetched? Can our data model support baggage from multiple providers?"

**It is already a cache, today, for messages.** The plan extends the same model to patches.

Look at `internal/provider/anthropic/anthropic.go:projectMessages`:

```go
for _, msg := range msgs {
    if raw, ok := msg.Baggage[providerName]; ok {
        var cached nativeMessage
        if err := json.Unmarshal(raw, &cached); err == nil {
            // ...use cached directly (cache hit)...
            continue
        }
    }
    // ...derive from IR (cache miss)...
}
```

- **Cache key:** `(LogEntry, providerName)`.
- **Cache hit:** read `Baggage[providerName]`, unmarshal, use as the wire content for this entry.
- **Cache miss:** project from the IR, populate `Baggage[providerName]` on the way out (response messages get baggage populated in `consumeSSE` at `anthropic.go:632`).
- **Multi-provider:** `Baggage` is `map[string]json.RawMessage` keyed by provider name. Different providers populate different keys; switching providers reads no entry for the new provider, so it re-derives. Past entries from the prior provider are preserved as separate keys.
- **Visitor semantics:** the projection IS a visitor that walks the message slice and dispatches each item to either the cache-hit path or the IR-derive path.

After Stage D, this same visitor walks `Block.Entries` (messages + patches) and dispatches uniformly. For chalkboard patches, the "IR-derive path" is the renderer; for messages, it's the existing native-shape projection. Both paths populate baggage on miss; both paths read baggage on hit.

**Renderer-change mid-aria** (was Q4): existing patch baggage for the *active* provider is reused if present (it's a cache hit for that provider); switching `reminder_renderer` doesn't invalidate baggage automatically. If you want a clean re-render after a renderer change, the user must clear the relevant baggage entries — there's no automatic invalidation. Worth documenting this explicitly so users aren't surprised; could be a `figaro chalkboard rerender` command later. For v1 the policy is: baggage is the source of truth for what was sent; the active renderer only matters on cache miss.

This corrects the previous draft of this plan, which proposed "existing baggage preserved, new patches render with new renderer" — that's still partially true (new patches with no baggage do hit the renderer), but for *existing* patches the past baggage is reused even after a renderer change. To force a re-render, drop the baggage entry. Behavior is "baggage is sticky"; it does not auto-invalidate on configuration change.

### R7. Multi-provider baggage — same model

A patch's `Baggage` map (per-provider) supports multiple providers naturally — same as message baggage today. The visitor reads only the active provider's entry; the other entries are inert. If a patch was rendered by provider A, then later projected via provider B, B sees no entry for itself and re-renders, populating `Baggage["b"]` alongside the still-present `Baggage["a"]`. Both entries coexist on the same patch entry.

Test in Stage F: provider A renders patch, baggage["a"] populated. Switch to provider B for next send; baggage["b"] absent; provider B re-renders and populates baggage["b"]. Both keys coexist on the same patch entry. Re-send via provider A reads from baggage["a"] (cache hit, no re-render).

## Remaining open questions

Items still genuinely undecided.

### Q1. Strict-NDJSON vs lazy-NDJSON for `aria.jsonl`

**Decided: lazy NDJSON via rewrite-tmp-rename for v1.**

Lazy NDJSON means: the file format is NDJSON (one entry per line), but writes happen via the *atomic file replacement pattern*:

1. **Compute the full new content** in memory: serialize all current `Block.Entries` to NDJSON, one line per entry.
2. **Write to a sibling tmp file**: `os.WriteFile("arias/{id}/aria.jsonl.tmp", buf.Bytes(), 0o600)`.
3. **Sync (recommended)**: open the tmp file with `os.OpenFile`, call `f.Sync()` (issuing `fsync(2)` on POSIX) so the kernel flushes dirty pages to disk hardware. Without this, a power loss between the rename and the kernel flushing the page cache can leave the new file partially-written even though `rename()` has succeeded.
4. **Rename atomically**: `os.Rename("arias/{id}/aria.jsonl.tmp", "arias/{id}/aria.jsonl")`. POSIX guarantees `rename(2)` is atomic at the inode level — readers see *either* the old file or the new file, never an in-progress mix.

A reader of `aria.jsonl` always sees a consistent, fully-formed snapshot. A crash during steps 1–3 leaves the old file untouched (the rename hasn't happened); a crash after step 4 leaves the new file in place. This is the same pattern used by the existing `internal/store/file.go:Save` and by `chalkboard.State.Save`.

Cost: every `endTurn` rewrites the entire file. For a 100-message aria with ~2KB average entry size, that's ~200KB rewritten per turn — not a real concern at human-scale conversations, but would matter for very long sessions or high-frequency tool turns. Ahead of the sqlite migration (which eliminates the rewrite cost via incremental row inserts), the watermarking work — a separate plan — can introduce *strict* NDJSON (append-only writes with fsync per line) for the log file specifically while keeping `chalkboard.json` rewrite-tmp-rename. That's the right migration path: strict for monotonic-append data, lazy for state-replacement data.

### Q3. `Block.Header` field — purpose recap and decision

**Original intent.** When conversation context grows too large, the harness summarizes the early portion into one synthesized message and discards the originals. The summary is stored in `Block.Header`; the conversation continues in `Block.Messages` with only the recent post-summary entries. The provider's projection prepends Header's content as a system-role message in the wire payload. This is "compaction," documented in `plans/PLAN.md` and the original `internal/message/message.go:107-120` comment.

**Current actual use.** Compaction is not implemented. `Block.Header` has been **repurposed** as the system-prompt carrier — `agent.go:843`:

```go
if block.Header == nil && a.systemPrompt != "" {
    block.Header = &message.Message{
        Role:    message.RoleSystem,
        Content: []message.Content{message.TextContent(a.systemPrompt)},
    }
}
```

The provider's projection treats `block.Header.Content[0].Text` as the system prompt regardless of why it's there.

**Decision for this redesign.** Keep `Block.Header` as the system-prompt carrier. It's a state-stable identity field, not a timeline event, and modeling it as a `LogEntry` would force every consumer of `Block.Entries` to handle a special "header-position" case. When compaction lands, it can either:

- (a) Reuse this slot — write a summary into Header that includes the system prompt followed by a conversation summary; the projection keeps treating it as the system blob.
- (b) Introduce a separate `Block.Summary *Message` field; the projection handles two state-stable preamble slots.

That's a future-work decision. For now: Header stays, Block gains `Entries`, the system-prompt-via-Header pattern is unchanged.

### Q4. Renderer change mid-aria

Resolved above (see R4). Baggage is sticky; renderer change does not auto-invalidate; force-re-render is an explicit operation.

### Q7. Multi-provider baggage on patches

Resolved above (see R7).

## Relationship to the cache-control work

The cache-control plan ([`../cache-control/SYSTEM-REMINDERS.md`](../cache-control/SYSTEM-REMINDERS.md)) is largely complete; the prefix-stability invariant is enforced and the `cache_control` wire is correctly placed. This unification proposal is a follow-on refinement, not a redirection: it makes the persisted IR faithful to what the model actually saw, fixes the lt-collision bug, and unifies storage in a way that aligns with the eventual sqlite migration.

The byte-stability of the cache prefix is unaffected by this work — patch baggage that re-projects to identical bytes is exactly what the existing cache-control wiring expects. If anything, the unified projection makes prefix stability *easier* to reason about (all wire content is derivable from IR baggage; nothing comes from ephemeral renderer state).
