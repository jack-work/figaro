# Chalkboard — Implementation Plan

> Make the conversation prefix byte-stable; wire `cache_control`. The system reminder feature falls out as a consequence.

Background and analysis: [`docs/system-reminders/audit.md`](../docs/system-reminders/audit.md). Read the audit before making large design decisions; this document describes execution.

This plan replaces an earlier "Implement System Reminders" sketch. The earlier sketch framed the work as a feature; the audit established it is more accurately a **state-management refactor** for cache-prefix stability that delivers reminders as one of its products.

---

## Goal

Eliminate per-turn entropy in the conversation prefix sent to providers, so `cache_control` on the Anthropic API hits consistently. Move volatile and conditional state out of the system prompt and into a structured, append-only state record co-located with the conversation log: the **chalkboard**. The chalkboard's mutations are the canonical mechanism by which the harness surfaces state changes to the model.

## Design constraints (load-bearing)

1. **Prefix bytes are immutable.** Past messages, past chalkboard patches, the system prompt, and the tools block are all byte-stable across requests within an aria's lifetime, modulo deliberate user edits to `~/.config/figaro/credo.md` or `skills/`. Compaction (future) is the only event that legitimately rewrites the prefix.
2. **No injected overrides into the model's view.** The harness never inserts content that voids prior instructions or pretends to speak as the user/system mid-conversation. State changes flow exclusively through the chalkboard and its renderers. (This kills the existing `crashPrompt` / `staticScribe` swap — see Stage 3.)
3. **Append, never mutate.** Renderers attach to the latest user message or append after it. They do not rewrite `block.Header`, earlier messages, or the tools block.
4. **Tool ordering is stable.** `tool.Registry.List()` returns deterministic order; `projectTools` produces byte-identical output across calls and across JSON round-trips. Verified by test.
5. **Lifecycle alignment.** Disk flush, snapshot rewrite, cache-control breakpoint advancement, and chalkboard log persistence all happen at `endTurn` (the agent's existing yield point).

## Architecture

### Types

```go
// internal/chalkboard/chalkboard.go
package chalkboard

type Snapshot map[string]json.RawMessage

type Patch struct {
    Set    map[string]json.RawMessage `json:"set,omitempty"`
    Remove []string                   `json:"remove,omitempty"`
}

type Entry struct {
    Key string
    Old json.RawMessage // nil if newly set
    New json.RawMessage // nil if removed
}

type RenderedEntry struct {
    Key  string
    Body string
}

func (s Snapshot) Diff(prev Snapshot) Patch
func (s Snapshot) Apply(p Patch) Snapshot
func (p Patch) Entries(prev Snapshot) []Entry
func Render(p Patch, prev Snapshot, tmpls *template.Template) []RenderedEntry
```

### Wire protocol

`figaro.prompt` extends with an optional chalkboard input. The `patch` field is the discriminator.

```go
// internal/rpc/methods.go
type PromptParams struct {
    Text       string            `json:"text"`
    Chalkboard *ChalkboardInput  `json:"chalkboard,omitempty"`
}

type ChalkboardInput struct {
    Context chalkboard.Snapshot `json:"context,omitempty"` // client's view of full state
    Patch   *chalkboard.Patch   `json:"patch,omitempty"`   // explicit mutations on top
}
```

Server handling:

- **`patch` only** — apply directly to current persisted snapshot.
- **`context` only** — compute `context.Diff(current)` and apply.
- **`context` + `patch`** — diff `context` vs current (informational, drift detection), then apply `patch` on top. Reject if base mismatch makes the patch incoherent.

Schema is open. Absence of a key in `context` is deletion.

### Persistence

Append-only log + cached snapshot, time-ordered with messages on a shared logical-time space.

```
~/.local/state/figaro/arias/<id>.log.json       append-only stream of LogEntry
~/.local/state/figaro/arias/<id>.snapshot.json  cached current snapshot (rewritten on endTurn)
```

```go
type LogEntry struct {
    LogicalTime uint64
    Kind        string // "message" | "chalkboard_patch"
    Message     *message.Message   // when Kind == "message"
    Patch       *chalkboard.Patch  // when Kind == "chalkboard_patch"
}
```

Interfaces (`chalkboard.Store`, `store.Backend`) hide the file layout. The eventual sqlite migration (out of scope here) is a swap, not a rewrite.

`AriaMeta` retires. Its fields (label, model, cwd, root, provider) become chalkboard keys. The label-preservation hack at `agent.go:199-204` disappears.

### Provider integration

```go
type Provider interface {
    Name() string
    Models(ctx) ([]ModelInfo, error)
    SetModel(model string)
    Send(ctx, *message.Block, []Tool, *chalkboard.Patch, []chalkboard.RenderedEntry, maxTokens) (<-chan StreamEvent, error)
}
```

Per-provider renderers live inside their respective provider packages (`internal/provider/anthropic/render_tag.go`, `render_tool.go`). Anthropic accepts a `reminder_renderer: "tag" | "tool"` config flag selecting between:

- **Tag** — `<system-reminder name="…">…</system-reminder>` blocks appended as a content block on the latest user message (or a synthetic user turn if the latest message is assistant-role; flagged in tests as a likely caller mistake).
- **Tool injection** — synthetic `assistant: tool_use(…)` + `user: tool_result(…)` pair appended to the wire-time message stream. **No synthetic tool declared in the `tools` parameter.** Historical `tool_use` blocks for undeclared tools are accepted by the API and read by the model as transcript-only context; the model can't call the synthetic tool going forward because no schema exists.

Renderers attach only to the latest user message or append after it. Cache prefix is preserved.

### Templates

Per-key body templates, Go `text/template`, embedded in the binary via `//go:embed`. User overrides under `~/.config/figaro/chalkboard/<key>.tmpl`. Keys without a template render to nothing — they're chalkboard state but not surfaced to the model.

```
internal/chalkboard/templates/
├── cwd.tmpl          "Working directory: {{.New}}"
├── model.tmpl        "Model: {{.New}}{{if .Old}} (was {{.Old}}){{end}}"
├── datetime.tmpl     "Current time: {{.New}}"
├── label.tmpl        "Aria label: {{.New}}"
├── truncation.tmpl   "The previous read was truncated..."
└── ...
```

### Cache control

`cache_control: {type: ephemeral}` is set on:

- The last system block in `projectBlockWithModel`.
- The last tool definition in `projectTools` (requires deterministic ordering — see constraint 4).
- The second-to-last message in `projectMessages` (= the leaf at the most recent `endTurn` = everything that was on disk before the new prompt arrived).

For v1 the third breakpoint is implicit (`len(messages) - 2`). If we later need explicit control across very long histories, we add a `last_cache_breakpoint` chalkboard key.

---

## Stages

Each stage commits independently and is reviewable on its own.

### Stage 0.5 — `cache_control` wiring + tool determinism

Cheap, parallel. Lands before chalkboard for measurement baseline.

**Goal:** wire cache_control breakpoints; guarantee deterministic tool projection.

**Deliverables:**
- `internal/provider/anthropic/anthropic.go`: set `cache_control: {type: ephemeral}` on last system block (in `projectBlockWithModel`), last tool definition (in `projectTools`), and second-to-last message (in `projectMessages`).
- `internal/tool/registry.go`: ensure `List()` returns tools in deterministic order (registration order or sorted by name, decision local to the change).
- Surface `Usage.CacheReadTokens` and `Usage.CacheWriteTokens` in `figaro list` and `figaro info` output for in-the-loop measurement.

**Tests:**
- `projectTools(registry.List())` produces equal bytes when invoked twice in succession.
- `projectTools` output round-tripped through `json.Marshal` + `json.Unmarshal` re-projects to equal bytes.
- A request constructed with the same chalkboard state and message history twice produces equal bytes (excluding any timestamp-bearing field, which there should be none of in the cached prefix).
- Existing tests still pass.

**Done when:** a real prompt to Anthropic returns a non-zero `cache_read_input_tokens` value on the second turn within the cache TTL.

### Stage 1 — Audit ✅

[`docs/system-reminders/audit.md`](../docs/system-reminders/audit.md).

### Stage 2 — Chalkboard core

**Goal:** the `chalkboard` package, in isolation. No agent integration yet.

**Deliverables:**
- `internal/chalkboard/chalkboard.go` — `Snapshot`, `Patch`, `Entry`, `RenderedEntry`, `Diff`, `Apply`, `Entries`.
- `internal/chalkboard/render.go` — `Render(p Patch, prev Snapshot, tmpls *template.Template) []RenderedEntry`, with the phrasing-lint helper.
- `internal/chalkboard/templates/*.tmpl` — embedded defaults for `cwd`, `model`, `datetime`, `label`, `truncation`, `token_budget`. Embedded via `//go:embed`.
- `internal/chalkboard/store.go` — `Store` interface (one method to read current snapshot, one to append a patch with `LogicalTime`); v1 file-backed implementation that reads/writes `<id>.log.json` and `<id>.snapshot.json` per the persistence layout above.
- Phrasing lint helper that flags reminder bodies which (a) start with imperative system-command framing, (b) exceed a configurable length, (c) duplicate the static credo content.

**Tests:**
- Diff and Apply round-trip: `(s.Apply(s.Diff(prev))) == s` for any prev/s.
- Empty diff produces empty patch.
- Patches with `Set` and `Remove` apply correctly; missing keys in `Remove` are no-ops or rejected per the wire-protocol contract (decision: idempotent — log a warning, no error).
- Render: each entry produces the templated body; missing template = empty body; unknown key = silent skip.
- Phrasing lint: imperative bodies fail the lint; factual bodies pass.
- Store: append-then-read; cold-load reconstructs current snapshot from log; snapshot file matches log replay byte-for-byte.

**Done when:** unit-test green; package independently buildable; no agent integration yet.

### Stage 3 — Agent integration; remove `staticScribe`

**Goal:** the drain loop reads/writes chalkboard state and surfaces patches to the provider. `AriaMeta` retires. Crash recovery becomes invisible.

**Deliverables:**
- `internal/figaro/agent.go`:
  - Inject a `chalkboard.Store` into `Agent` config, wired through from `cmd/figaro` and `internal/angelus`.
  - In `eventUserPrompt`: read incoming chalkboard input from the event, apply server-side merge logic per the wire protocol, persist the resulting patch via `chalkboard.Store.Append`, render it, hand `(*Patch, []RenderedEntry)` to `startLLMStream`.
  - In reactive trigger sites (`pre_tool_use`, `post_tool_use`, `turn_end`): write chalkboard patches directly. Triggers and client-driven mutations merge before rendering.
  - In `endTurn`: rewrite the snapshot file alongside the existing `MemStore.Flush` call. One write per turn.
  - **Remove `crashPrompt` constant.** Remove `staticScribe`. `runWithRecovery` keeps recovery sequence minus the scribe swap. Recovery is logged to stderr only; nothing surfaces to the model.
- `internal/store/`:
  - Retire `AriaMeta`. Migrate label, model, cwd, root, provider to chalkboard keys. Delete the label-preservation hack.
  - `RestoreArias` still works; the chalkboard's persistence is what carries the per-aria config now.
- `cmd/figaro/main.go`: when constructing the `figaro.prompt` request, populate the `Chalkboard.Context` field with cwd, time (hour-precision), model, label.
- `internal/credo/default_credo.md`: **delete the footer** (lines 36-42, the template field block). The body alone (lines 1-33) remains.

**Tests:**
- Restart cycle: kill angelus, recreate, send the same chalkboard `Context` as before — diff is empty, no patch persisted, no reminder rendered.
- Mutation: send a different `cwd`, observe a patch in the log + a rendered entry in the next provider request.
- Recovery: induce a panic via test hook, verify drain loop restarts, verify no `crashPrompt`-style content appears in the next request to the provider.
- Migration: an aria with the old `AriaMeta` shape on disk loads correctly into the new chalkboard layout (one-time migration on first read).

**Done when:** all existing tests pass; new tests above pass; `figaro list` and `figaro info` continue to display label/model/cwd/root from the chalkboard rather than `AriaMeta`.

### Stage 4 — Wire protocol + Anthropic renderers

**Goal:** the Anthropic provider learns to consume the patch and render it as either tag or tool-injection.

**Deliverables:**
- `internal/provider/provider.go`: extend `Send` signature with `*chalkboard.Patch` and `[]chalkboard.RenderedEntry`. Update all callers.
- `internal/provider/anthropic/render_tag.go`: tag renderer.
- `internal/provider/anthropic/render_tool.go`: tool-injection renderer.
- `internal/provider/anthropic/anthropic.go`: read `reminder_renderer` from config (default `"tag"` for OAuth, `"tag"` for API key — both supported); look up the configured renderer; invoke before the existing `projectBlockWithModel` projection. Renderers return mutated `*message.Block` and never touch `Header` or earlier messages.
- `internal/config/config.go`: add `reminder_renderer` to the Anthropic provider config.

**Tests:**
- Tag renderer: with one chalkboard mutation, the projected request has a `<system-reminder>` block in the latest user message at the expected position.
- Tool renderer: the projected request has a synthetic `tool_use` + `tool_result` pair appended; the synthetic tool name does NOT appear in the `tools` parameter.
- No mutation: empty patch → byte-identical projected request to the no-chalkboard-input case.
- Cache stability: same chalkboard state across two turns → byte-identical projected prefix (everything up through the second-to-last message).
- Rejection: a patch that fails the wire-protocol base-validation surfaces an error to the caller and does not advance state.

**Done when:** real prompts succeed against Anthropic with both renderers; cache hit rate measurably climbs vs the Stage 0.5 baseline.

### Stage 5 — Hardening tests

**Goal:** lock in the invariants from the Constraints section.

**Deliverables:**
- **Prefix byte-stability regression.** Three consecutive turns on the same aria with at least one chalkboard mutation per turn. Assert: bytes of the system block, the tools block, and all messages up through the second-to-last are identical across all three projected requests.
- **Replay safety.** Save an aria containing tool-injection-style synthetic tool_use/tool_result pairs. Restore. Send a new prompt. Assert: the synthetic pair is in transcript verbatim; no real tool execution fired for it.
- **Wire-protocol coverage.** Drive each branch of the `ChalkboardInput` discriminator (patch only / context only / both, and the rejection path).
- **Phrasing lint catalog.** Run the lint helper across all default templates and the chalkboard's user-override loader. Surface any flagged bodies for human review.

**Done when:** all of the above pass in CI.

### Stage 6 — Docs

**Deliverables:**
- `agents.md`: add cache-stability invariant to the Invariants section; add chalkboard to Hot Spots; add a Disclosure rule that the harness does not inject overrides into the model's conversation (state changes flow exclusively through the chalkboard).
- `ARCHITECTURE.md`: new section on the chalkboard (concept, types, persistence, provider integration); update the Configuration Layout and Runtime Layout sections to show the log/snapshot file split; mention `cache_control` on the JSON-RPC protocol section if relevant.
- `README.md`: add a one-liner under "Working" mentioning chalkboard-backed reminders and prompt caching.
- CHANGELOG entry per Stages 0.5 and 3.

**Done when:** docs reflect reality; reviewer can read `agents.md` + `ARCHITECTURE.md` and explain the cache-stability story without reading code.

---

## Out of scope (deliberately)

- **OAuth identity prefix** in `anthropic.go:213-220`. Provider+auth-conditional, not under Scribe; outside the chalkboard.
- **Crash notification.** No model-visible artifact is added on panic recovery. Recovery is invisible to the model and logged to stderr only.
- **sqlite migration.** Designed for, not implemented. The interfaces (`chalkboard.Store`, `store.Backend`) are shaped to make the swap clean.
- **Compaction.** Future feature; the `post_compaction` event name is reserved.
- **Generic lifecycle hook framework.** Today `endTurn` is implicitly the hook. When the second or third subscriber needs to register, we lift to a formal `OnTurnEnd(state)` registry. Not now.
- **Client-supplied templates for system-reminder / tool-use block shape.** A natural future extension; the renderer interface is designed to accept it. Not in v1.

---

## Operating notes

- Stop and surface findings between stages. Don't chain straight through if a stage reveals something that contradicts this document — flag it.
- When in doubt about reminder phrasing, default to factual / declarative. Imperative system-command framing in non-system positions is what the lint helper exists to catch.
- Keep individual commits small and stage-aligned. A reviewer should be able to review Stage 2 without needing Stages 3+ to exist yet.
- The credo's identity body (`default_credo.md` lines 1-33) is *not* touched by this work. Anything that would change the model's voice or persona is out of scope; this work is plumbing.
