# System Reminders / Chalkboard — Stage 1 Audit

Per `plans/SYSTEM-REMINDERS.md`. Stage 1 deliverable; surface for review before Stage 2.

The plan describes this work as a "system reminders" feature. The audit concludes it is more accurately a **state-management refactor** that delivers system reminders as a consequence. The unifying thesis is cache prefix stability.

______________________________________________________________________

## 1. Thesis

**The long-term goal is byte-stable conversation prefixes so the Anthropic API's `cache_control` actually pays off.**

Today nothing in `internal/provider/anthropic` sets `cache_control` (verified: `grep cache_control` returns no hits in the package). Even if it did, the prefix isn't stable enough for the cache to hit consistently — the system prompt mutates every hour (`Current time:`), every `cd` (`Working directory:`), every `figaro set_model`. Adding `cache_control` to a churning prefix is wasted work.

So the work has two prongs:

1. **Make the prefix immutable.** Move every volatile or conditional value out of the system prompt and into a structured, append-only state record co-located with the conversation log. This is the **chalkboard**.
1. **Wire `cache_control`** so the now-stable prefix is actually cached. This is cheap (~30 lines) and can land in parallel for measurement.

Reminders, in this framing, are *what the model sees when chalkboard state changes*. The mechanism is the chalkboard; reminders are its visible surface.

## 2. Prior context

- **Memory** (`/home/gluck/.claude/projects/.../memory/`) — empty.
- **Codebase grep** (`reminder|notice|inject`) — only relevant hit is the `crashPrompt` constant in `agent.go:353` and `staticScribe` swap in `runWithRecovery` (`agent.go:429`). This is the existing one-shot reactive-reminder pattern by another name; it will be removed entirely (see §5).
- **Plans** — `plans/PLAN.md` mentions WAL-backed persistence as a future step; `plans/aria-persistence.md` documents the current full-state-overwrite FileStore. Both connect directly to the at-rest discussion in §10.
- **Live system reminders in this Claude Code session** — the harness running this audit fires real reminders at the agent (TaskCreate nudge, deferred-tool schemas, available skills, currentDate context block). Worth noting: the TaskCreate nudge is reactive and uses *factual* phrasing ("if you're working on tasks that would benefit from tracking…") not imperative. The currentDate block hedges with "this context may or may not be relevant." Both are working examples of the phrasing guardrails the plan asks for in Stage 2.
- **OAuth identity prefix** in `anthropic.go:213-220` is out of scope (provider+auth-conditional, not under Scribe). Confirmed.

## 3. What currently breaks the cache prefix

Two kinds of entropy: content (the credo's template variables) and code (the harness mutating the prefix even when bytes are stable).

### Content entropy

Source: `internal/credo/default_credo.md:36-42`. Every template field below mutates the system prompt's bytes whenever the value changes:

| Field | Mutates when | Approx tokens |
|---|---|---:|
| `{{.DateTime}}` | Every hour (formatted to "Monday, January 2, 2006, 3PM MST") | ~10 |
| `{{.Cwd}}` | User changes shells / `cd` between figaros | ~10 |
| `{{.Root}}` | Same as Cwd | ~10 |
| `{{.Provider}}` | Effectively never; redundant with OAuth identity prefix | ~3 |
| `{{.Model}}` | `figaro set_model` (or restored aria with different model) | ~10 |
| `{{.FigaroID}}` | Effectively never per session, but distinct per-aria. Session and aria are more or less the same. | ~10 |
| `{{.Version}}` | Build-stable | ~3 |

### Code entropy

| Site | Issue |
|---|---|
| `credo.go:84` (Scribe cache check) | Compares `ctx == s.cachedCtx`; any field difference rebuilds the prompt. Even if rendered output would be identical bytes, the cache invalidates on Context inequality. |
| `agent.go:793-797` | `block.Header` is reassigned from `a.systemPrompt` on every `startLLMStream` call. Even when bytes are identical, the reassignment is a liability — a future bug could mutate without anyone noticing. Header should be set **once** at construction. |
| `tool/registry.go` | Need to verify deterministic `List()` iteration order. If it's `map`-backed, the tool list bytes can shuffle between turns, breaking the tools-block cache prefix. |
| Skills appendix (`credo.go:FormatSkills`) | Appended after the templated body. Mutates whenever skills directory changes. Currently bundled into the same byte stream as the credo body. |

After chalkboard relocation: the credo body alone (lines 1-33 of `default_credo.md`) is the static system prompt. That string is byte-stable as long as the user doesn't edit `credo.md`. Scribe builds it once, caches it, and `block.Header` carries it for the agent's lifetime.

## 4. The chalkboard

### Concept

A **chalkboard** is the union of an aria's configuration and per-turn context — every structured value about the aria that is not a conversation message. Examples: cwd, model, time, label, project root, last truncation event, token-budget watermark.

Operations on a chalkboard are **patches**: a set of key/value updates plus a list of removals. Patches are computed by diffing snapshots and are the canonical unit of communication between layers — wire, agent, provider, persistence.

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

func (s Snapshot) Diff(prev Snapshot) Patch
func (s Snapshot) Apply(p Patch) Snapshot
func (p Patch) Entries(prev Snapshot) []Entry
```

### Wire protocol

`figaro.prompt` is extended with an optional chalkboard input. The shape carries two optional fields, `context` (a full snapshot from the client's perspective) and `patch` (a precomputed patch). The presence of `patch` is the discriminator.

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

Server-side handling:

- **`patch` only** — apply patch directly to current persisted snapshot.
- **`context` only** — server computes `context.Diff(current)` and applies the resulting patch. Used by dumb clients (`q`) that don't track state.
- **`context` + `patch`** — server computes the diff from `context` against current (informational; lets the server detect drift), then applies `patch` on top of that. Used by clients that track their own state and want to ship explicit intent.
- **Patches that cannot apply to the active state are rejected** — e.g., when the client's `context` declares a base state that diverges from the server's current state in a way the patch presumes. Detect by validating the base; surface the mismatch to the client.

Schema is open — keys are whatever the client sets. Absence of a key in `context` is deletion.

### Persistence — append-only, time-ordered with messages

Chalkboard patches and conversation messages share a single logical-time space. Each patch gets a `LogicalTime` from the same monotonic counter as messages (`message.Message.LogicalTime`). The on-disk log is ordered, append-only, and reconstructable by replay.

For v1 we keep the JSON file shape but split it:

```
~/.local/state/figaro/arias/<id>.log.json       append-only stream of LogEntry
~/.local/state/figaro/arias/<id>.snapshot.json  cached current snapshot (rewritten)
```

`LogEntry` carries either a message or a patch:

```go
type LogEntry struct {
    LogicalTime uint64
    Kind        string // "message" | "chalkboard_patch"
    Message     *message.Message   // when Kind == "message"
    Patch       *chalkboard.Patch  // when Kind == "chalkboard_patch"
}
```

The split costs one extra file per aria but stops conflating "append" and "rewrite" at the storage layer. When sqlite arrives, both files collapse into tables — `aria_log` (event stream) and `aria_snapshot` (one row per aria). The interface (`chalkboard.Store`, `store.Backend`) does not encode the file shape, so the migration is a swap.

`AriaMeta` retires. Label, model, cwd, root, provider all become chalkboard keys. The label-preservation hack at `agent.go:199-204` disappears — chalkboard's append-only nature is the formalism that hack was approximating.

### Provider integration

`Provider.Send` takes the patch directly. The provider owns rendering — each implementation chooses tag-style or tool-injection-style based on its config.

```go
type Provider interface {
    Name() string
    Models(ctx) ([]ModelInfo, error)
    SetModel(model string)
    Send(ctx, *message.Block, []Tool, *chalkboard.Patch, []chalkboard.RenderedEntry, maxTokens) (<-chan StreamEvent, error)
}
```

`RenderedEntry` is the harness's pre-rendered body for each patch entry, produced by templates (§ next). Provider gets both: the structured patch (for deciding *where* in the wire payload) and the rendered text (for *what* to embed). Rendering text is harness-owned by default; per-provider divergence is possible if a provider needs it.

For Anthropic: a `reminder_renderer: "tag" | "tool"` config flag selects between two implementations:

- **Tag.** `<system-reminder name="…">…</system-reminder>` blocks appended as an additional content block on the latest user message. (Or as a synthetic user turn if the latest message is assistant-role — flag in tests, since that usually indicates a fire at the wrong lifecycle point.)
- **Tool injection.** Synthetic `assistant: tool_use(…)` + `user: tool_result(…)` pair appended to the wire-time message stream. **No synthetic tool declared in the `tools` parameter.** Historical `tool_use` blocks for undeclared tools are accepted by the API and read by the model as "I called something, got something back"; the model can't call the synthetic going forward because there's no schema. This collapses the plan's "sentinel ID + executor short-circuit" complexity to nothing.

Both renderers attach to the **latest** user message or append after it. Neither rewrites `block.Header` or any earlier message. The cache prefix is preserved.

### Templates

Per-key body templates, Go `text/template`, embedded in the binary via `//go:embed`. Same engine Scribe already uses for `default_credo.md`, so no new dependency.

```
internal/chalkboard/templates/
├── cwd.tmpl          "Working directory: {{.New}}"
├── model.tmpl        "Model: {{.New}}{{if .Old}} (was {{.Old}}){{end}}"
├── datetime.tmpl     "Current time: {{.New}}"
├── label.tmpl        "Aria label: {{.New}}"
├── truncation.tmpl   "The previous read was truncated..."
└── ...
```

Default templates ship in the binary. User overrides under `~/.config/figaro/chalkboard/<key>.tmpl` follow the same lookup pattern as `skills/`. Keys without a template render to nothing (silent — they're chalkboard state but not surfaced to the model).

**Future direction.** The harness owns body placement and templates today. A natural next step is to let the client (CLI or a richer frontend) supply a template for how reminders are wrapped at the wire layer — letting external tooling model the Anthropic system-reminder block or tool-use block shape directly. The renderer interface is designed to accept this extension; for v1 we hardcode placement per provider.

Each template binding is a `chalkboard.Entry`:

```go
type RenderedEntry struct {
    Key  string
    Body string
}

func Render(p Patch, prev Snapshot, tmpls *template.Template) []RenderedEntry
```

### Disk-authoritative semantics, formalized

The chalkboard is the disk-authoritative state record. On agent reconstruction (cold start, angelus restart, panic recovery), the chalkboard is loaded from disk. The agent does **not** overwrite it with values from the construction Config — Config supplies the running-process operational state (which the next prompt's chalkboard input will update normally), not the persisted state.

This formalizes the pattern that today exists only as the label-preservation hack. After this work: the boundary is type-level, not convention.

## 5. Crash recovery — remove the model-visible part entirely

Today: `runWithRecovery` swaps `a.scribe` to `staticScribe{prompt: crashPrompt}` permanently. After the first panic, every subsequent turn runs on the 40-byte crash text — the credo (identity, voice, secrets rules, soul) is gone for the agent's lifetime. Only `Kill` + recreate restores it. The crash text is also misleading: since the FileStore checkpoint work, conversation context is *not* lost — only the in-flight assistant turn (partial response, pending tool calls, unflushed tool output) is discarded.

The whole notion of injecting anything into the conversation history when a panic occurs is dropped. The model is not told. The credo persists. The agent recovers and continues.

Concretely: `runWithRecovery` keeps its existing recovery sequence (drain-loop restart, store reset from FileStore checkpoint, inbox swap, subscriber notification of an Error + Done) but **drops** the scribe swap and the `crashPrompt` constant entirely. The stderr log line `figaro %s: restarted after panic` remains — operators see the event, the model does not.

This makes the harness's behavior consistent: we never inject "previous instructions are voided, here are new ones" content into the model's view. Recovery becomes invisible. Disclosure rule for `agents.md`: the harness does not inject overrides into the model's conversation; state changes flow exclusively through the chalkboard and its renderers.

CHANGELOG: *"Panic recovery no longer modifies the agent's system prompt or injects any conversation content. The credo persists across panics; recovery is logged to stderr and not surfaced to the model."*

## 6. Lifecycle events

Existing `event` types in `agent.go:25-37` map to chalkboard fire-points:

| Event | Chalkboard moment |
|---|---|
| `eventUserPrompt` | **`turn_start`.** Apply incoming chalkboard input from the prompt RPC. Render and embed any non-empty patch. |
| `eventLLMDelta` | None. |
| `eventLLMDone` | If tool calls: apply `pre_tool_batch` triggers (rare). If `end_turn`: apply `turn_end` triggers. |
| `eventLLMError` | Optional `post_error` trigger. Low priority for v1. |
| `eventToolOutput` | None. |
| `eventToolResult` | **`post_tool_use`.** If `evt.isErr`: also `post_tool_failure`. Read-tool truncation surfaces here as a chalkboard event. |
| `eventInterrupt` | None — turn is being torn down. |

New synthesized fire-points (all on the drain-loop goroutine, no new threading):

- `pre_tool_use` — at the `go a.runToolAsync(...)` sites in `agent.go:607` and `agent.go:696`.
- `turn_end` — at the top of `endTurn`, before `Yield`. Token-budget threshold check happens here. Disk flush, snapshot rewrite, and cache_control marker advancement are the existing-and-future subscribers to this point.
- `post_compaction` — reserved name; not wired until compaction lands.

Internal triggers (reactive, harness-driven) write chalkboard patches the same way client snapshots do. The drain loop merges client-driven and trigger-driven patches before handing them to the provider:

```go
case eventUserPrompt:
    clientPatch := computeClientPatch(evt.chalkboard)         // diff vs persisted
    triggerPatch := triggers.Fire("turn_start", harnessState) // reactive
    patch := mergePatches(clientPatch, triggerPatch)
    persistPatch(patch)
    rendered := chalkboard.Render(patch, prevSnapshot, templates)
    a.startLLMStream(turnCtx, inbox, &patch, rendered)
```

Debouncing falls out for free: a key whose value didn't change is not in the diff, so the patch is empty for that key, so the renderer emits nothing. The plan's "same Name+Body fired twice in the same turn appears once" guarantee is what `Snapshot.Diff` produces by construction.

## 7. Provider IR — what changes

`internal/message/message.go` is unchanged. `Block` and `Message` need no new fields. `provider.Tool` does not need to move out of `provider/` (no import cycle now that renderer extras are gone).

`internal/provider/provider.go` extends `Send` with the patch and rendered entries (§4). The Anthropic implementation:

1. Looks up its configured renderer (tag or tool).
1. The renderer mutates the wire-time `nativeMessage` slice (or the `message.Block` before projection — implementation choice) to embed the rendered entries. **It never touches `block.Header` or earlier `block.Messages`.**
1. Cache_control breakpoints are set per §8.

Per-provider renderers live inside their respective provider packages (e.g. `internal/provider/anthropic/render_tag.go`, `render_tool.go`). They share helper code from `internal/chalkboard/render` if useful. There is no centralized renderer registry — `RendererFor(provider, config)` from the original plan is replaced by "each provider knows what to do."

## 8. `cache_control` wiring (Stage 0.5)

Cheap, parallel, can land before the chalkboard work. Adds `cache_control: {type: ephemeral}` to:

- The last system block in `projectBlockWithModel` (~lines 213-225).
- The last tool definition in `projectTools` (~line 337).
- The second-to-last message in `projectMessages` — i.e. the leaf at the most recent `endTurn`, which is everything that was on disk before the current user prompt arrived.

The breakpoint placement aligns with `endTurn`: the agent's existing "yield to user" lifecycle event. At endTurn we already flush to disk; the same hook conceptually advances the "stable prefix" marker. For v1 the marker is implicit — we compute it as `len(messages) - 2` at request-construction time. If we later need explicit control (e.g. multiple breakpoints across very long history), we add a `last_cache_breakpoint` chalkboard key.

`message_start.usage.cache_read_input_tokens` is already parsed (`anthropic.go:589-600`) and surfaced through `Usage.CacheReadTokens`. Wiring `cache_control` immediately gives us a cache-hit-rate metric we can graph against the chalkboard relocation work — the win becomes quantitative.

With the credo footer still volatile, the hit rate will be poor. That's the point of measurement: we land Stage 0.5, observe the floor, then watch the rate climb as Stage 4's relocations roll in.

**Tool ordering is load-bearing.** The cache breakpoint after the tools block requires `projectTools` to produce **byte-identical output across requests**. This means:

- `tool.Registry.List()` must return tools in deterministic order (e.g. registration order or sorted by name).
- The Anthropic baggage round-trip (any place we serialize/deserialize `nativeMessage` content carrying tool definitions) must preserve byte ordering.
- Stage 0.5 includes a round-trip test: `projectTools(registry.List())` produces equal bytes when invoked twice; `projectTools` output round-tripped through JSON encode/decode and re-projected is equal bytes.

If `Registry` is `map`-backed today, the fix is to add a stable iteration order and the round-trip test. If it's already deterministic, only the test is new.

## 9. Entropy relocation table (the deliverable)

Each row becomes a chalkboard key. Templates render bodies. Removals from the credo template happen in Stage 4 alongside the wiring.

| Source | From | Chalkboard key | Template | Trigger | Notes |
|---|---|---|---|---|---|
| Current time | `default_credo.md:37` | `datetime` | `datetime.tmpl` | Client (CLI) populates per turn | Hour-precision; diffs only on hour change |
| Working directory | `default_credo.md:38` | `cwd` | `cwd.tmpl` | Client populates per turn | Diffs on shell change |
| Project root | `default_credo.md:39` | `root` | `root.tmpl` | Client populates per turn | |
| Provider | `default_credo.md:40` | *(drop)* | — | — | Redundant with OAuth identity prefix |
| Model | `default_credo.md:41` | `model` | `model.tmpl` | `SetModel` writes a trigger patch | |
| Figaro ID | `default_credo.md:42` | *(drop)* | — | — | Operationally unused by the model |
| Skills appendix | `credo.go:FormatSkills` | *(stays in Scribe)* | — | — | **Not relocated.** Skills stay inside the cached system block. When the skills directory changes, the system bytes change and the cache invalidates once on the next request, then re-caches. This is acceptable because skills changes are user-initiated edits, not turn-driven. Putting them past the cache breakpoint would be wrong — they're stable enough to belong with the credo, not the chalkboard. |
| Crash prompt | `agent.go:crashPrompt` + `staticScribe` swap | *(remove entirely)* | — | — | See §5. The constant and the scribe swap are both deleted; nothing surfaces to the model. |
| Read-tool truncation | not currently surfaced | `last_truncation` | `truncation.tmpl` | Read tool sets when `TruncationResult.Truncated`; cleared at next turn | Event-shaped |
| Token-budget warning | not currently surfaced | `token_budget_status` | `token_budget.tmpl` | Trigger at endTurn on first crossing of 50/80/95% thresholds | Persistent (stays at last threshold until reset on new aria) |
| Aria label | `AriaMeta.Label` | `label` | `label.tmpl` (optional — may be unrendered) | `SetLabel` writes a patch | Persistent |
| Compaction notice | future | `post_compaction` | `compaction.tmpl` | Reserved | Implement when compaction lands |
| OAuth identity | `anthropic.go:213-220` | *(out of scope)* | — | — | Provider+auth-conditional, not under Scribe |

Net effect: the static credo (lines 1-33 of `default_credo.md`) is the entire system prompt. Scribe builds it once. `block.Header` is set once. The bytes are stable for the agent's lifetime, modulo user editing `credo.md`. Cache prefix invariant achieved.

## 10. At-rest immutability

The chalkboard's append-only design pulls the storage layer in the same direction. Past messages don't change; past chalkboard patches don't change; the only event that legitimately rewrites the prefix is compaction (future).

The current FileStore rewrites the entire JSON file at every flush. With chalkboard added, we'd be rewriting more state more often — every chalkboard mutation as well as every message. That's the wrong write pattern for the data shape.

The interim split (§4) — `<id>.log.json` (append-only) + `<id>.snapshot.json` (rewritten) — is the right shape now and the natural sqlite migration target later. The interfaces (`chalkboard.Store`, `store.Backend`) hide the file layout so the migration to sqlite changes the implementation without touching call sites.

When sqlite lands:

- `aria_log(aria_id, logical_time, kind, payload)` — append-only event stream. Replaces `<id>.log.json`. Cross-aria queries become trivial.
- `aria_snapshot(aria_id, snapshot_json)` — one row per aria; UPSERT on chalkboard mutation.
- The compaction event becomes a row that semantically truncates earlier `aria_log` rows for that aria.

Not in scope for this work. The point is to design the interfaces so it's a swap, not a rewrite.

## 11. Stage plan, revised

The original plan's stages still apply but the scope has shifted. New ordering:

| Stage | Scope |
|---|---|
| **0.5** | Wire `cache_control` on system, tools, and last stable message in `anthropic.go`. Verify deterministic tool-list iteration. Surface `Usage.CacheReadTokens` in `figaro list` / `info`. **Lands before chalkboard for measurement baseline.** |
| **1** | This audit. ✅ |
| **2** | Chalkboard core: `chalkboard.{Snapshot, Patch, Entry, Render}`, in-memory diff/apply, embedded default templates, `chalkboard.Store` interface with v1 file-backed implementation (the log/snapshot split). No agent integration yet. Full unit-test coverage including phrasing lint. |
| **3** | Agent integration: drainLoop reads/writes chalkboard, persists patches, fires triggers, hands `(*Patch, []RenderedEntry)` to provider. Crash recovery migrated. `AriaMeta` retires; its fields become chalkboard keys. |
| **4** | Wire protocol extension: `figaro.prompt` accepts `chalkboard` (snapshot or patch). CLI populates with cwd/time/model/label. Anthropic provider learns tag and tool renderers; reminder renderer config flag. **Credo footer relocations land here.** Cache-hit rate (from Stage 0.5 instrumentation) should jump. |
| **5** | Tests: prefix byte-stability regression (the strong version — three consecutive turns with chalkboard mutations, prefix bytes identical), replay safety, lint warnings catalog. |
| **6** | Docs: update `agents.md` (cache-stability invariant, chalkboard hot-spot, prefix-mutation disclosure rule), `ARCHITECTURE.md` (chalkboard section, log/snapshot split, cache_control), CHANGELOG. |

Each stage commits independently and reviewable on its own.

## 12. Open questions — resolved

Resolutions captured here so future readers can see how each was settled.

1. **Per-provider rendering vs. harness rendering for body text.** Resolved: harness-owned templates with provider-internal placement. Providers may ignore the rendered body and re-render from the patch if they ever need to, but the default and recommended path is harness-rendered. Long-term, the harness client may supply its own templates for system-reminder / tool-use block shape (see §4 Templates → Future direction); for v1 the harness places everything itself.

2. **Skills handling.** Resolved: skills stay in the system prompt (Scribe-handled) — *not* relocated to chalkboard. Skills changes are user-initiated edits, rare, and fit better with the credo than with the volatile per-turn state. The cache invalidates once when the skills directory changes, then re-caches. See §9.

3. **`tool.Registry.List()` iteration order.** Resolved: must be deterministic and byte-stable across the round-trip. Stage 0.5 verifies and fixes if needed, plus adds a round-trip test. See §8.

4. **Snapshot file rewrite frequency / cache breakpoint advancement.** Resolved: at `endTurn`, alongside the existing `MemStore.Flush` call. One rewrite per turn, one cache breakpoint advancement per turn. Mid-turn chalkboard mutations append to the log but do not rewrite the snapshot until the turn yields. Mid-turn cache breakpoints don't move. See §8 and §11.

5. **Wire-protocol discriminator for `ChalkboardInput`.** Resolved: the presence of the `patch` field is the discriminator. `patch` alone applies directly; `context` alone triggers a server-side diff; both means the server uses `context` to validate the client's expected base, computes a diff for drift detection, and applies `patch` on top. Patches that cannot apply to the active state are rejected. See §4 Wire protocol.

## Stop here.

Per the plan, Stage 1 surfaces for review before Stage 2. All open questions resolved; next step is to convert this audit into the actionable plan at `plans/SYSTEM-REMINDERS.md`.
