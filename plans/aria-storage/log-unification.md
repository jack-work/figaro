# Aria Log Unification — v2 (consolidated design)

> **Status:** plan / proposal. Plan-only commits to date. No implementation work has begun against this revision.
>
> **Revision history:**
> - **v1 (initial):** patches as standalone LogEntries in a discriminated union with messages; baggage with `AttachToNext` field for ordering hints; coalesce-same-role at projection; `chalkboard.Aria` per-aria handle; system prompt remained on `Block.Header`.
> - **v2 (this revision):** patches ride as **sidecars on the IR message they accompany**; baggage is a flat list of wire messages with no positional metadata; the projection layer emits no global cleanup pass — providers are pure handlers under causal masking. `Block.Header` retires entirely; the system prompt is captured at aria creation as a reserved chalkboard key (`system.prompt`) and lives in the chalkboard alongside other system configuration. `coalesceSameRole` is removed. Compaction is omitted by design. A new control-plane RPC (`figaro.rehydrate`) re-renders system configuration from disk and emits a chalkboard patch without polluting the conversation. The aria becomes a single-typed list-of-LogEntry. `chalkboard.Aria` is renamed `chalkboard.State` to avoid name collision with the conceptual aria.

## Why this exists

Inspection of aria `9d961345` after Stage 4 of the chalkboard work landed surfaced two observable problems:

1. **Chalkboard patches have no representation in the aria's conversation log.** The system-reminder content the model actually saw at request-time was added to the wire-format `nativeRequest` and discarded after the HTTP call. The IR `Block.Messages` contains only raw user text; baggage on user messages does not include the rendered reminders.
2. **Logical-time collisions between the two log files.** The chalkboard patch and the first user message both got `lt=1` because they live in separate stores with independent counters.

Plus several latent issues the audit revealed:

- `Block.Header` is dual-purpose: it was nominally for compaction summaries (never shipped) but is in fact carrying the rendered system prompt. The dual purpose is an architectural smell.
- The system prompt is re-templated on every prompt by Scribe — wasted work and a source of byte-instability if any template variable changes.
- Provider rendering of system reminders is locked into Anthropic's "tag inside user message" pattern; the IR doesn't expose the right shape for providers with different conventions.

This revision unifies storage, state, and projection into a model that fixes the observable bugs and the structural ones in one consistent shape.

## Core design

### One IR type, one log

```go
// internal/message/message.go
type Block struct {
    Entries []LogEntry  // ordered conversation log; only field on Block.
}

type LogEntry struct {
    LogicalTime uint64
    Timestamp   int64

    // Exactly one of Message or Patch is non-nil for most entries.
    // Bootstrap and rehydrate entries are Patch-only (no Message).
    // A turn whose chalkboard input produced changes carries both:
    // a Message (the user's prompt) and a Patch (the chalkboard
    // mutation that accompanied it).
    Message *Message
    Patch   *Patch

    // Per-provider wire-form cache. See § Baggage.
    Baggage Baggage
}
```

`Block` has no `Header`, no `Messages`, no `Tools`, no out-of-band slots. The aria is exactly an ordered list of `LogEntry`. Single type; uniform iteration; no special-cases at the top level.

### Patches as sidecars on the IR message they accompany

A chalkboard patch is **not** a standalone timeline event. It is metadata attached to the message that triggered it. The handler that projects the message also sees the patch and emits whatever wire content the patch implies (system-reminder tags, etc.) as part of the *same* wire message — because that is how Anthropic accepts them: as content blocks inside the user message, not as separate top-level entries.

This collapses the alternation problem entirely. There are no patch-only LogEntries to interleave with messages mid-stream; therefore no consecutive same-role wire messages from patches; therefore no need for a `coalesceSameRole` cleanup. The projection is a straight 1:1 visit over `Block.Entries`.

The exceptions are the **bootstrap** and **rehydrate** patches, which are Patch-only LogEntries. They emit no wire messages at projection time — their contribution is to update the chalkboard's `system.*` snapshot, which the projection reads to configure top-level wire fields (system prompt, etc.). They sit in the log for audit, but the LLM never sees them as conversation.

### Providers as pure handlers under causal masking

A provider's renderer is a pure function:

```go
// Conceptual signature; see § Baggage for the actual return type.
type Handler func(self *LogEntry, prior []LogEntry, snapshot chalkboard.Snapshot) []json.RawMessage
```

The handler can read `self`, the current chalkboard `snapshot`, and any prior LogEntries (for context like baggage cache-hits on previous turns). It cannot read future entries — **causal masking** — and it does not know its position in the wire stream globally. It produces the wire-format messages this entry materializes, period.

Causal masking is what makes per-message handlers safe under the prefix-stability invariant: an entry's wire output is determined entirely by the entry itself plus its causal past. No future change can retroactively alter past wire bytes. This is the property that prompt caching ultimately rewards.

### Baggage — flat list of wire messages, no positional metadata

```go
// internal/message/baggage.go
type Baggage map[string][]json.RawMessage  // provider name → ordered wire messages
```

A LogEntry's baggage records, per provider, the wire-format messages it materialized on its first projection. Subsequent projections read from baggage if present; cache-miss invokes the handler.

- Typical message LogEntry: `len(Baggage["anthropic"]) == 1`.
- Tool-injection-rendered patch sidecar (if a future provider variant uses it): `len(Baggage["anthropic"]) == 2`.
- Bootstrap / rehydrate Patch-only entry: `len(Baggage["anthropic"]) == 0` — the patch contributes via the chalkboard snapshot, not via wire messages.

No `AttachToNext`, no positional hints, no ordering metadata in the type system. The fan-out (1:N) is permitted but not encoded; each wire message is "the largest atomic primitive of a given feature" the handler chose to emit.

**Cache semantics.** Baggage is purely a cache: present → read; absent → derive and store. Multi-provider on the same LogEntry is naturally supported; switching providers misses the cache for the new provider name and re-derives. Past providers' baggage entries are preserved as inert keys.

**Renderer-config fingerprinting.** Each baggage entry carries a fingerprint of the renderer config that produced it (e.g., a hash of `{reminder_renderer: "tag", model: "...", relevant flags}`). On read, compare the fingerprint to the current config; if mismatched, log a warning. **Do not auto-invalidate** — a bytes-stable past projection is the source of truth for what was sent. Force-re-render is an explicit operation. (Future: a `figaro chalkboard rerender` command for manual cache busting.)

```go
type Baggage struct {
    // Per-provider wire messages. Outer key: provider name.
    Entries map[string]ProviderBaggage `json:"entries,omitempty"`
}

type ProviderBaggage struct {
    Messages    []json.RawMessage `json:"messages"`
    Fingerprint string            `json:"fp,omitempty"` // renderer-config hash at write time
}
```

The two-layer shape lets us add per-provider metadata (fingerprint, timestamp, etc.) without breaking the message list. JSON-level back-compat with v0 baggage (single-blob form) is a one-shot migration on read.

### Chalkboard with reserved `system.*` namespace

```go
// internal/chalkboard/chalkboard.go
type Snapshot map[string]json.RawMessage  // unchanged shape.
```

Two zones in one flat namespace:

- **`system.*` keys** — durable per-aria configuration. Set at aria creation by the bootstrap patch; mutated only by `figaro.rehydrate` control messages. Includes `system.prompt`, `system.model`, `system.provider`, `system.reminder_renderer`, `system.max_tokens`, `system.skills`, `system.skills_digest`, `system.credo_digest`. Conventionally read by the provider's projection to configure top-level wire fields.
- **other keys** — per-turn volatile state. Set by client patches arriving on `figaro.prompt`. Includes `cwd`, `model_active` (if differs from system.model), per-turn flags. The default chalkboard renderer turns these into system-reminder content that rides on the same wire message as the user prompt.

(`datetime` is removed entirely from the credo template's eligibility — see § Credo Context.)

The reserved `system.` prefix is a documented convention, not a type-system distinction. Diff/Apply/Render operate uniformly on both zones; the **projection** decides how to wire each (top-level config field vs. system-reminder content block).

### `chalkboard.State` (per-aria handle) — renamed from `chalkboard.Aria`

```go
// internal/chalkboard/state.go
type State struct {
    snapshot Snapshot
    path     string  // arias/{id}/chalkboard.json
    dirty    bool
}

func Open(path string) (*State, error)
func (s *State) Snapshot() Snapshot          // returns a clone; caller must not mutate
func (s *State) Apply(p Patch) Snapshot      // mutates in-memory; marks dirty
func (s *State) Save() error                 // atomic rewrite-tmp-rename if dirty
func (s *State) Close() error                // saves + releases
```

Single-owner; no mutex (the actor model serializes access). One `*State` per agent, lifetime-bound. The agent's previous `cb` and `cbSnap` fields collapse into one `chalkboard *chalkboard.State`.

Naming: "Aria" remains the *concept* (the persistent conversation as a whole, identified by `arias/{id}/`). The Agent is its in-memory runtime. `chalkboard.State` is the per-aria chalkboard handle the Agent embeds. There is no Go type called "Aria."

### Storage layout

```
~/.local/state/figaro/arias/
├── {id}/
│   ├── aria.jsonl          # NDJSON log: one LogEntry per line. Source of truth.
│   └── chalkboard.json     # cached current snapshot (derived; rebuildable from log).
```

`aria.jsonl` writes via **lazy NDJSON / rewrite-tmp-rename**: at flush time, serialize the full current `Block.Entries` to NDJSON in memory, write to `aria.jsonl.tmp`, fsync, atomically rename to `aria.jsonl`. Readers always see a consistent, fully-formed snapshot. Crash before the rename leaves the old file untouched; crash after leaves the new one in place.

`chalkboard.json` writes the same way — derived cache, atomic replacement, idempotent.

Strict NDJSON (append-only, fsync-per-line) is deferred to the watermarking work. Neither is needed for this revision.

The legacy `arias/{id}.json` and `chalkboards/{id}/` paths retire. Migration on cold load: read old format, interleave per `lt`, write new format, delete old files. One-shot per aria, idempotent.

### Aria bootstrap

`NewAgent` runs once at construction:

1. Construct the Agent shell — no entries yet.
2. **Run Scribe once.** Renders the credo body from `~/.config/figaro/credo.md`. The credo template's Context struct exposes a deliberately sparse set of identity fields (Provider, FigaroID, Version) — see § Credo Context. Datetime, Cwd, Root, Model are NOT exposed; values that change between aria creations or between turns must come from the chalkboard, not from the credo template.
3. **Load skills metadata.** `LoadSkills(skillsDir)` returns `[]Skill{Name, Description, Path}` — frontmatter only, no body content. See § Skills.
4. **Compute `system.credo_digest` and `system.skills_digest`.** SHA-256 of the credo file contents; SHA-256 of the sorted concatenation of skill file paths + their mod-times. Used to detect drift on rehydrate.
5. **Construct the bootstrap patch:**
   ```go
   bootstrap := Patch{Set: map[string]json.RawMessage{
       "system.prompt":            jsonString(scribeOutput),
       "system.skills":            jsonMarshal(skills),     // structured list
       "system.skills_digest":     jsonString(skillsDigest),
       "system.credo_digest":      jsonString(credoDigest),
       "system.model":             jsonString(cfg.Model),
       "system.provider":          jsonString(cfg.Provider.Name()),
       "system.reminder_renderer": jsonString(cfg.ReminderRenderer),
       "system.max_tokens":        jsonInt(cfg.MaxTokens),
   }}
   ```
6. Allocate `lt = MemStore.AllocLT() = 1`. Build `LogEntry{LogicalTime: 1, Timestamp: now, Patch: &bootstrap}`. Append to memStore. Apply to `chalkboard.State`.
7. Return the constructed Agent. The bootstrap entry is the first LogEntry in the aria, persisted at the next flush.

After bootstrap, Scribe is **not** invoked again unless rehydrate runs. The system prompt is read from `chalkboard.system.prompt`; subsequent prompts don't re-template anything.

### Rehydrate control message

A new RPC method that re-renders system configuration from disk and emits a patch if anything changed.

```go
// internal/rpc/methods.go
const MethodRehydrate = "figaro.rehydrate"

type RehydrateRequest struct {
    DryRun bool `json:"dry_run,omitempty"`  // compute the diff but don't apply
}

type RehydrateResponse struct {
    PatchApplied bool   `json:"patch_applied"`
    LogicalTime  uint64 `json:"logical_time,omitempty"`
    Diff         *Patch `json:"diff,omitempty"`     // populated when DryRun or when applied
    Reason       string `json:"reason,omitempty"`   // e.g. "no changes", or error detail
}
```

Agent handler:

1. Re-read user config: `~/.config/figaro/credo.md`, `~/.config/figaro/skills/*.md`, the current provider's `config.toml`.
2. Recompute `system.*` candidate values (credo_digest, prompt, skills, skills_digest, model, reminder_renderer, max_tokens). Same path as `NewAgent` step 4–5.
3. **Validate:** each new value must be parseable / acceptable for its type. `system.reminder_renderer` ∈ `{"tag", "tool"}`; `system.model` known to current provider; `system.provider` change — **rejected** for v1 (provider switching requires a new aria).
4. Diff against current `system.*` keys in `chalkboard.State.Snapshot()`.
5. If validation fails → return error; aria continues running on prior config; no patch applied.
6. If diff is empty → `PatchApplied: false, Reason: "no changes"`.
7. Else, construct the rehydrate Patch from the diff. `LogEntry{LogicalTime: AllocLT(), Patch: &diff}`. Append, apply, save snapshot, flush log.
8. Return success with the applied patch in `Diff` for client visibility.

`DryRun: true` runs steps 1–6 and returns the proposed diff without applying it — useful for previewing a config change.

CLI: `figaro rehydrate` operates on the figaro bound to the calling shell's PID. Optional flag `--dry-run`.

**The rehydrate entry has no Message.** When the next turn projects, the rehydrate entry contributes to the chalkboard snapshot but emits zero wire messages. The LLM sees only the consequences (e.g. a renderer change applies to subsequent turns) — never a message about the rehydrate event itself.

## Credo Context — sparse, point-in-time-frozen

The credo template's Context struct is reduced to fields that are immutable per aria:

```go
// internal/credo/credo.go (proposed)
type Context struct {
    Provider string  // provider name at aria creation
    FigaroID string  // immutable aria identifier
    Version  string  // build version (VCS revision)
}
```

Removed entirely: `DateTime`, `Cwd`, `Root`, `Model`, `Tools`. These are entropic — they change between aria creations and/or between turns — and they belong in the chalkboard, sent by the client (cwd, model) or computed by the server (datetime is no longer in the credo at all; the client supplies it as `chalkboard.datetime` per turn).

If an existing user `credo.md` references `{{.DateTime}}` or any other removed field, **the template execution fails with a clear error** ("credo.md should not use template variables for entropic values; use the chalkboard for cwd / datetime / model"). This is intentional: silently rendering empty strings produces a stale-but-plausible system prompt that could confuse the model for the rest of the aria's life.

When implementing this revision, also strip `~/.config/figaro/credo.md`'s footer (the `Current time:` / `Working directory:` / `Project root:` / `Model:` / `Figaro:` block) before any live testing. The default embedded credo at `internal/credo/default_credo.md` already has it removed; the user's personal copy still carries it.

## Skills — structured chalkboard data, per-provider rendered

See § Implementation Stages and the skills discussion section below for the full design. Summary:

- **Loading**: `LoadSkills(skillsDir)` continues to return `[]Skill{Name, Description, Path}`. Skills bodies are not loaded eagerly; the model reads them via the `read` tool when it wants details.
- **Storage**: skills go in `chalkboard.system.skills` as a structured JSON array of skill metadata. Set by the bootstrap patch and updated by rehydrate patches.
- **Projection**: each provider chooses how to surface skills. Anthropic for v1 produces the same markdown appendix it does today, but the rendering happens at projection time inside `internal/provider/anthropic/`, not inside Scribe. A future provider could surface skills as actual tool definitions or as a sidecar RAG corpus.
- **Drift detection**: `system.skills_digest` records a digest of skill files at bootstrap; cold load compares to current disk and surfaces a warning if they differ ("aria N has skills_digest A; disk is now B; run `figaro rehydrate` to update").

## Implementation stages

Each stage commits independently. Conservative order: shape the types, migrate storage, update projection, add control plane, then tests/docs.

### Stage A — IR type changes

**Goal:** `LogEntry`, `Block.Entries`, `Patch` (moved from `internal/chalkboard/`). Helper accessors. No storage or projection changes yet.

**Deliverables:**
- `internal/message/message.go`: `LogEntry` (Message + Patch sidecar shape), `Block.Entries []LogEntry`, helpers `Block.Messages() []Message` and `Block.Patches() []Patch` for callers that filter.
- `internal/message/baggage.go` (NEW): `Baggage` and `ProviderBaggage` types per § Baggage.
- `internal/message/patch.go` (NEW or moved): `Patch` IR type — re-exported from `chalkboard.Patch` for compatibility, eventually canonical.
- `Block.Header` and `Block.Messages` (the slice) marked `Deprecated:` — accessor methods route through `Entries`. Removed entirely in a follow-up commit.
- All call sites updated to use `Block.Entries` / accessors.

**Tests:** existing tests still pass; new tests for the shape (e.g. a LogEntry with both Message and Patch round-trips through JSON correctly).

### Stage B — MemStore + FileStore unified log; storage migration

**Goal:** in-memory and on-disk stores hold `LogEntry` slices. Logical time goes through one counter via `MemStore.AllocLT()`. NDJSON on disk under the new `arias/{id}/` directory layout.

**Deliverables:**
- `internal/store/mem.go`: `Append(entry LogEntry)`, `AllocLT() uint64`, `Entries() []LogEntry`.
- `internal/store/file.go`: NDJSON shape, lazy rewrite-tmp-rename. `arias/{id}/aria.jsonl`.
- `internal/store/store.go`: interface updated.
- Cold-load migration: detect old `arias/{id}.json`, read, write new layout, delete old.

**Tests:** restore from old format produces identical entries (modulo Stage C's patch interleaving); new writes use the new layout.

### Stage C — Chalkboard refactor; bootstrap and rehydrate

**Goal:** `*chalkboard.State` per-aria handle replaces `chalkboard.Store`. Aria bootstrap runs Scribe once and emits the bootstrap patch as the first LogEntry. Rehydrate RPC works end-to-end. Patches enter the unified log.

**Deliverables:**
- `internal/chalkboard/state.go` (NEW): `*State` with Open/Snapshot/Apply/Save/Close.
- `internal/chalkboard/store.go` (DELETED): `Store` interface and `FileStore` retire. Patch persistence is the message store's job (LogEntry of kind Patch via sidecar or standalone).
- `internal/figaro/agent.go`:
  - Replace `cb chalkboard.Store` + `cbSnap chalkboard.Snapshot` with one `chalkboard *chalkboard.State`.
  - `NewAgent`: implement bootstrap (Scribe → skills → patch → first LogEntry).
  - `applyChalkboardInput`: build a Patch; allocate `lt = MemStore.AllocLT()`; append a LogEntry where `Patch` is the sidecar AND `Message` is the user message at the same lt — *one LogEntry, both fields*. Apply patch to `chalkboard.State`.
  - `endTurn`: `a.chalkboard.Save()` alongside `MemStore.Flush()`.
  - `Kill`: `a.chalkboard.Close()`.
  - Remove the `cbTurnReminders` field and the `[]chalkboard.RenderedEntry` parameter on `Provider.Send`.
- `internal/credo/credo.go`: trim Context struct to `{Provider, FigaroID, Version}`. Template error on removed fields.
- `internal/figaro/protocol.go`: add `figaro.rehydrate` handler.
- `internal/figaro/client.go`: `Rehydrate(ctx, dryRun bool) (*RehydrateResponse, error)`.
- `cmd/figaro/main.go`: `figaro rehydrate [--dry-run]` subcommand.
- `internal/rpc/methods.go`: `MethodRehydrate`, `RehydrateRequest`, `RehydrateResponse`.
- Migration on cold load: read legacy `chalkboards/{id}/log.json`, convert each to a Patch sidecar on the message at the matching `lt` (or a Patch-only entry if no matching message), write to unified `arias/{id}/aria.jsonl`. Move `chalkboards/{id}/snapshot.json` → `arias/{id}/chalkboard.json`. Delete old `chalkboards/{id}/`.

**Tests:**
- After a turn with chalkboard input, `aria.jsonl` contains a LogEntry with both Message and Patch fields populated. Logical times unique. Cold-load reconstructs identical state.
- Bootstrap test: a freshly-created aria has exactly one LogEntry with no Message and a Patch setting `system.*`.
- Rehydrate end-to-end: edit credo.md; run rehydrate; verify a new Patch-only LogEntry; subsequent turn's projection reflects the new system prompt.
- Rehydrate validation: invalid `reminder_renderer` rejected; provider change rejected; aria untouched on rejection.
- Migration: synthesize legacy-format aria + chalkboard pair on disk; cold-load; assert unified format afterward and identical in-memory state.

### Stage D — Projection rewrite (handlers under causal masking)

**Goal:** Renderers become pure handlers over LogEntry. The Anthropic provider reads `system.*` chalkboard keys for top-level wire fields and renders entries via baggage cache.

**Deliverables:**
- `internal/provider/provider.go`: `Send(ctx, *Block, snapshot chalkboard.Snapshot, []Tool, maxTokens) (...)` — the provider receives the chalkboard snapshot as input. The `[]chalkboard.RenderedEntry` parameter is gone. The system-prompt parameter is gone — read from `snapshot["system.prompt"]`.
- `internal/provider/anthropic/anthropic.go`:
  - `projectBlockWithModel` walks `Block.Entries`. For each entry:
    - Bootstrap or rehydrate Patch-only entry → no wire output (configures projection via the snapshot, which the caller already passed in).
    - Message LogEntry (with optional Patch sidecar) → if `Baggage["anthropic"]` present and fingerprint matches, emit cached. Otherwise call the configured renderer (tag or tool) on the entry; populate baggage; emit.
  - Top-level wire fields (`req.System`, `req.Tools`) come from the chalkboard snapshot. `system.prompt` → `req.System` (with OAuth Claude Code prefix logic preserved). `system.skills` → markdown-format-and-append to `req.System` for v1.
- `internal/provider/anthropic/render.go`:
  - `renderTag(*LogEntry)` — pure function. Returns 1 wire message (the user-role message containing the user text + system-reminder content blocks for any sidecar Patch keys outside `system.*`).
  - `renderTool(*LogEntry)` — pure function. Returns 1 wire message for ordinary content; for entries with a Patch sidecar, returns more shapes if needed (still alternation-respecting, since the patch sidecar accompanies a user message — no two-message synthetic pair needed).
  - Both renderers take only the LogEntry and the current chalkboard snapshot. No surrounding context, no positional metadata.
- `internal/figaro/agent.go`: pass `a.chalkboard.Snapshot()` to `Provider.Send`. Remove `a.cbTurnReminders`.

**Tests:**
- Wire-payload byte-equality across two consecutive turns with the same chalkboard state: turn 1 populates `Baggage["anthropic"]` on the user message; turn 2 hits the cache; bytes match.
- Bootstrap projection: the bootstrap LogEntry produces no wire messages but its `system.*` keys flow into `req.System`.
- Rehydrate projection: rehydrate entry produces no wire messages; its system.prompt change appears in subsequent `req.System`.
- Renderer-change mid-aria: switch `system.reminder_renderer` via rehydrate; turn N+1 fingerprint mismatch on past baggage logs warning; new entries use new renderer.
- Multi-provider baggage: same LogEntry projects via provider A populates `Baggage["a"]`; switch to provider B; B sees no `Baggage["b"]`, re-renders, populates. Both keys coexist.

### Stage E — Skills as structured chalkboard data; per-provider translation

**Goal:** Skills move into `chalkboard.system.skills` as structured metadata. Anthropic's projection reads it and produces the same markdown appendix as today, but at projection time, not inside Scribe. (Future providers can render differently.)

**The shape stored in chalkboard:**

```jsonc
// chalkboard.system.skills
[
  {"name": "brave",  "description": "Search the web ...", "path": "/home/gluck/.config/figaro/skills/brave.md"},
  {"name": "docker", "description": "Docker container ...", "path": "/home/gluck/.config/figaro/skills/docker.md"}
  // ...
]
```

Only frontmatter metadata + path. Skill body content is read on demand by the model via the `read` tool — same as today.

**Drift detection digest:**

```go
func skillsDigest(skills []Skill) string {
    // Sort by path for stability
    sort.Slice(skills, func(i, j int) bool { return skills[i].Path < skills[j].Path })
    h := sha256.New()
    for _, s := range skills {
        info, _ := os.Stat(s.Path)
        fmt.Fprintf(h, "%s\t%d\t%d\n", s.Path, info.ModTime().UnixNano(), info.Size())
    }
    return hex.EncodeToString(h.Sum(nil))
}
```

Stored as `chalkboard.system.skills_digest` at bootstrap and on each rehydrate. On cold load, agent recomputes the digest from current disk; mismatch logs a warning ("aria N has skills_digest A; disk is now B; run `figaro rehydrate` to update"). Non-blocking.

**Order-sensitive deliverables.** Sequence matters for byte-stability test continuity:

1. **Add `chalkboard.system.skills` shape and the digest helper.** No projection changes yet. Bootstrap writes the new keys; nothing reads them yet. Existing FormatSkills + Scribe path still produces the system prompt.
2. **Move `FormatSkills` from `internal/credo/` to `internal/provider/anthropic/render_skills.go`.** Identical implementation, new home. Verify byte-for-byte identical output via test on representative `[]Skill` input. Wire-format and Scribe behavior remain unchanged at this step.
3. **Drop `FormatSkills` invocation from `Scribe.Build`.** Scribe now produces ONLY the rendered credo body; skills are absent from `system.prompt`. The test suite would fail at this point — wire format changed. Don't ship this step alone.
4. **Anthropic projection reads `system.skills` from the chalkboard snapshot and emits the markdown as a SEPARATE system block** (in addition to the existing prompt block). The `req.System` array becomes:
   - `{"type": "text", "text": "You are Claude Code, ..."}` (OAuth only — Claude Code identity)
   - `{"type": "text", "text": "<credo body>"}` (system.prompt)
   - `{"type": "text", "text": "# Available Skills\n...", "cache_control": ephemeral}` (system.skills, last block)
   The `markCacheBreakpoints` logic targets the last system block; with skills present, it now lands on the skills block. With no skills, on the prompt block. Either way, byte-stable per-aria.
5. **Re-run wire-format byte-stability tests.** Bytes that the model sees are identical to pre-refactor *modulo the block boundary* between credo and skills. Snapshot tests on `req.System` updated to expect the array shape.
6. **Cold-load drift detection.** Compute current disk digest at agent activation; compare to `system.skills_digest`; log mismatch warning.
7. **Rehydrate handler reads skills.** Re-read `~/.config/figaro/skills/`, recompute structured `[]Skill` and digest, diff against current `system.skills`/`system.skills_digest`, emit patch on diff.

**Files touched:**

- `internal/credo/credo.go`: `LoadSkills` stays; `FormatSkills` and `Skill.Content` removed from this package (Content was unused anyway). `Skill` struct moves into `internal/message/skill.go` so it lives in the IR.
- `internal/chalkboard/skills.go` (NEW): `MarshalSkills([]Skill) json.RawMessage`, `UnmarshalSkills(json.RawMessage) ([]Skill, error)`, `SkillsDigest([]Skill) string`.
- `internal/figaro/agent.go`: bootstrap and rehydrate paths populate `system.skills` and `system.skills_digest`.
- `internal/provider/anthropic/render_skills.go` (NEW): the markdown formatter; identical bytes to the prior `credo.FormatSkills`.
- `internal/provider/anthropic/anthropic.go`: `projectBlockWithModel` emits skills as a separate system block.

**Tests:**

- Bootstrap on an aria with N skills: `system.skills` is a JSON array of N entries; `system.skills_digest` matches the helper output.
- Anthropic projection produces identical markdown bytes for the skills block as `FormatSkills` did before the move (snapshot test).
- Wire-format `req.System` is a 2- or 3-element array depending on auth path and skill presence; cache_control is on the last element.
- Add a skill file → run `figaro rehydrate` → patch contains the new skill in `system.skills` and an updated `system.skills_digest`.
- Edit a skill's frontmatter description → run rehydrate → patch contains the updated entry.
- Edit a skill's body only → rehydrate → digest changes (mod-time differs), but the rendered system block bytes are unchanged (description unchanged), so `req.System` bytes match prior. Cache hit possible.
- Cold-load drift: write a fake old digest into chalkboard.json; restart agent; assert warning emitted to angelus.log.

### Stage F — Tests, benchmarks, docs

- Prefix byte-stability regression test extended to cover the bootstrap entry + multiple chalkboard mutations.
- Replay safety: synthetic Patch sidecar baggage round-trips correctly; tool-injection-style synthetic baggage doesn't trigger real tool execution.
- `agents.md` updated with the new invariants (single-typed Block; system.* reserved; bootstrap/rehydrate as control entries; renderers under causal masking).
- `ARCHITECTURE.md` updated for the new on-disk layout, the chalkboard.system namespace, and the rehydrate RPC.
- CHANGELOG entry covering the migration and behavior changes.
- The `docs/system-reminders/audit.md` Stage 4 design is partially superseded; add a note pointing here for the canonical design.

## Successor work — ponder points

This unification is requisite groundwork for a longer-term direction not implemented in this revision: **ponder points** — designated breakpoints in the conversation log where the model is permitted (or expected) to perform deliberate reflection without the user waiting on it, and where the harness can checkpoint reasoning state for resumption, branching, or experimentation.

The specific shape of ponder-point support is deferred. The structural prerequisites are in place once this revision lands:

- **Causal masking** ensures a ponder-point handler can operate on a stable prefix without future-state contamination.
- **Per-message handlers with baggage cache** make it cheap to re-project a partial conversation up to a point and emit synthetic continuations from there.
- **Sidecar patches on messages** mean a ponder-point's state mutation (e.g. "model considered X then concluded Y, here is a pruned summary") can attach to the entry it occurs on without forcing a new IR shape.
- **Reserved `system.*` chalkboard keys** give us a place to store policy knobs (when ponder-points fire, how long they run, what providers support them, etc.) without polluting the conversation IR.

A separate plan document under `plans/ponder-points/` will elaborate. This is mentioned here so future readers of this plan see the throughline: the mostly-architectural moves in this revision exist in service of the ponder-point and caching vision, even though that vision isn't shipping with the unification work itself.

## Resolved decisions (summary)

- Patches are sidecars on the IR message they accompany (or Patch-only entries for bootstrap/rehydrate); not standalone timeline events.
- `Block` has only `Entries []LogEntry`. No Header, no Messages slice.
- System prompt lives in `chalkboard.system.prompt`, set once at bootstrap, mutated only via `figaro.rehydrate`.
- Compaction is omitted by design. Arias are immutable, append-only.
- Renderers are pure handlers under causal masking. No `coalesceSameRole`. No positional metadata in baggage.
- Baggage carries a renderer-config fingerprint; cache is sticky (no auto-invalidation), force-rerender is explicit.
- `chalkboard.State` is the per-aria handle; `chalkboard.Store` retires.
- Logical time goes through `MemStore.AllocLT()` — one counter, no separate chalkboard counter.
- Lazy NDJSON via rewrite-tmp-rename for both `aria.jsonl` and `chalkboard.json`. Strict NDJSON deferred to watermarking.
- Credo Context is sparse: only Provider, FigaroID, Version. Template fails on removed fields.
- Skills are structured `chalkboard.system.skills`; provider-specific rendering at projection time.
- Provider switching mid-aria: rejected by rehydrate validation in v1. Slot exists in `system.provider` for future relaxation.

## Remaining open

- **Per-provider validators for system.* keys.** Currently the rehydrate validator is a flat list of "if `system.reminder_renderer` then check {tag, tool}". As more providers and config keys land, this should become a per-provider validator interface. Out of scope for v1.
- **Operator-friendly view of system.* changes.** A `figaro chalkboard show` or `figaro inspect` command would help users see current system state without reading the JSON files. Trivial follow-up.
- **Cache-busting command.** `figaro chalkboard rerender` to force-invalidate Baggage entries for the active provider, useful when a renderer change should propagate to past entries. Trivial follow-up.
- **Ponder points.** Successor work, separate plan.
