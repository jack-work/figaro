# Figaro Plans — Roadmap

> Index of architectural work, ordered by status (done → planned → future). Each entry links to the canonical plan document and lists its high-level steps.

## 1. Cache control — **done** (commits `99f051d` … `3e1b7a6`)

Plan: [`cache-control/SYSTEM-REMINDERS.md`](./cache-control/SYSTEM-REMINDERS.md). Followup: [`cache-control/prompt-caching-limitations.md`](./cache-control/prompt-caching-limitations.md).

Goal: byte-stable conversation prefix; wire `cache_control` on the Anthropic API.

- **0.5** — `cache_control` plumbing on `system` / `tools` / leaf-1 message; deterministic `tool.Registry.List`; `Usage.CacheReadTokens` / `CacheWriteTokens` surfaced in `figaro list`.
- **1** — Audit. Reframed "system reminders" as state-management refactor for cache-prefix stability.
- **2** — Chalkboard core: `Snapshot`, `Patch`, `Render`, embedded templates, `chalkboard.Store` + `FileStore`, phrasing lint.
- **3** — Agent integration: `chalkboard.Store` wired through angelus + agent; patches persisted; `staticScribe` / `crashPrompt` removed (credo persists across panics).
- **4** — Wire protocol + Anthropic renderers: `figaro.prompt` accepts chalkboard input; tag and tool-injection renderers; CLI populates cwd / datetime.
- **5** — Hardening tests: prefix byte-stability across three turns with mutations; renderer-placement assertions; multi-provider baggage.
- **6** — Docs + benchmarks: `agents.md` invariants #11 + #12; `ARCHITECTURE.md` chalkboard section; per-op benchmarks (Diff: 1.8µs at 50 keys; Render: 9.6µs; ApplyRenderer tag: 3.85µs).

Outcome: wiring correct; cache engages on API-key auth. The OAuth + `claude-code-20250219` path silently zeroes (pre-2026-03-17) or hard-rejects with HTTP 400 (post-2026-03-17) per Anthropic's structural policy — caching is API-tier-only on third-party clients. Documented in the limitations writeup; no further client-side work helps.

## 2. Aria log unification — **planned**, plan-only commits

Plan: [`aria-storage/log-unification.md`](./aria-storage/log-unification.md). Currently at v2 (single document tracks revision history at top).

Motivation: chalkboard patches don't appear in the persisted aria log; `lt` collisions between message and chalkboard counters; `Block.Header` is dual-purpose (compaction + system prompt); Scribe re-templates every prompt; provider rendering is locked into Anthropic's tag-inside-user-message pattern.

Architectural moves the plan locks in:

- Patches as **sidecars on the IR message** they accompany (or Patch-only entries for bootstrap and rehydrate); not standalone timeline events.
- `Block` becomes literally `Entries []LogEntry` — single-typed list. `Block.Header` retires; system prompt moves to `chalkboard.system.prompt`.
- Renderers are pure handlers under **causal masking** — read `self` + prior entries + chalkboard snapshot; never future entries. No global cleanup pass; no `coalesceSameRole`.
- Baggage is a flat list of wire messages with **no positional metadata**. Two-layer shape: `map[provider]ProviderBaggage{Messages, Fingerprint}`. Cache is sticky; force-rerender is explicit.
- **Compaction omitted by design.** Arias are immutable, append-only.
- `chalkboard.system.*` reserved namespace for durable per-aria configuration. Mutations only via `figaro.rehydrate` control message.
- `chalkboard.Store` retires; replaced by `*chalkboard.State` (per-aria value handle).
- One logical-time counter via `MemStore.AllocLT()`; collisions become impossible.
- Lazy NDJSON via rewrite-tmp-rename for both `aria.jsonl` and `chalkboard.json`.
- `Scribe` runs exactly twice per aria: bootstrap and rehydrate. Subsequent turns read `system.prompt` from the chalkboard.
- Credo `Context` struct trimmed to `{Provider, FigaroID, Version}`; entropic fields removed from template eligibility (`DateTime` removed entirely, supplied by client via chalkboard).

Six implementation stages, each independently committable:

- **A** — IR types: `LogEntry`, `Block.Entries`, `Patch` moved to `message/`, `Baggage{Messages, Fingerprint}`. Header / Messages-slice deprecated. No storage or projection changes yet.
- **B** — Storage: NDJSON shape under `arias/{id}/aria.jsonl` + `arias/{id}/chalkboard.json`. Lazy rewrite-tmp-rename. Cold-load migration from legacy layout (one-shot per aria).
- **C** — Chalkboard `*State`; bootstrap (Scribe-once); `figaro.rehydrate` RPC + `figaro rehydrate [--dry-run]` CLI; `chalkboard.Store` deletion; provider-switching rejected at validation; legacy chalkboard log integration into the unified aria.jsonl during migration.
- **D** — Projection rewrite: handlers under causal masking; baggage as cache with renderer-config fingerprint; `Provider.Send` takes `(*Block, snapshot, ...)` (no more reminders parameter, no more system-prompt parameter — both come from snapshot).
- **E** — Skills as structured chalkboard data: `system.skills` (JSON array of `{name, description, path}`); `system.skills_digest` for drift detection; `FormatSkills` moves from Scribe to `internal/provider/anthropic/render_skills.go`; skills emitted as a separate system block at projection time (cache_control attaches to it as the last system element).
- **F** — Tests, benchmarks, docs: prefix byte-stability extended to bootstrap entries; replay safety; agents.md invariants updated; ARCHITECTURE.md updated for the new layout; CHANGELOG.

After landing: the persisted aria contains a faithful record of what the model saw (system reminders included via patch-sidecar baggage); operators can inspect any aria's `system.*` state and skills history via the log; `figaro rehydrate` evolves config in flight without restarting figaros; provider abstraction is genuinely polymorphic over `system.prompt` (any provider can translate it to its own system field, developer-role message, prepended assistant message, etc.).

## 3. Ponder points — **future / scaffold**

Plan: [`ponder-points/README.md`](./ponder-points/README.md). Placeholder only; no design committed.

Concept: designated breakpoints in the conversation log where the model performs deliberate reflection asynchronously from the user, with reasoning state checkpointed for resumption / branching / experimentation.

Why it lives here as a forward-looking marker:

- Requires causal-masking handlers (delivered by Stage D of unification).
- Requires per-message baggage cache with renderer-config fingerprinting (delivered by Stage D).
- Requires sidecar-on-message pattern for the ponder result to attach without forcing a new IR variant (delivered by Stage A).
- Requires `system.*` reserved namespace for ponder policy knobs (delivered by Stage C).

The architectural prerequisites are exactly the moves the unification plan makes for unrelated reasons (cache stability, faithful audit, single-typed IR). When ponder points get specified, the IR shape, projection algorithm, and storage layout already support them — the work is mostly handler logic and CLI/policy plumbing.

Open at design time:

- What triggers a ponder point (user explicit / per-N-turns / policy-driven)?
- Output shape — synthetic assistant entry, sidecar on triggering entry, separate trace channel?
- Budget controls (tokens / wall-clock / model "thinking")?
- Branching shape (adjacent aria, child aria with parent ref, reified checkpoint)?
- Provider variability (not all providers will support a thinking budget; what's the fallback)?

These are not blocking unification; they are blocking ponder-point implementation. Punt.

## Throughlines

What ties these together architecturally:

1. **Prefix invariance.** Every move favors keeping the conversation prefix bytes stable so prompt caching (when the auth path supports it) and the not-yet-built ponder-point cache both engage. Cache control wired the breakpoints; unification eliminates the residual sources of byte instability (Scribe re-templating, chalkboard patches injected only at wire time, Header dual-purpose churn).
2. **State on messages, not between them.** Chalkboard patches start as their own log type (current state), become sidecars on messages (unification v2), and end up as the substrate for ponder-point reasoning checkpoints (future). Each step preserves immutability; each step tightens the "one IR per timeline event" model.
3. **Providers as pure handlers under causal masking.** Cache control treated providers as wire-format projection; unification formalizes this as a per-message handler with read-only access to causal context; ponder points use the same handler surface to project synthetic continuations cheaply. Provider polymorphism stops being aspirational once `system.prompt` is structured data the provider translates to its own conventions.
4. **Aria as immutable append-only log.** Cache control kept the conversation log immutable but retained the dual-purpose Header. Unification removes the last special slot and makes aria persistence single-typed. Ponder-point checkpoints become entries-in-the-log rather than out-of-band state, which means they audit and replay like everything else.

Each plan is a step along these lines; each completes a slice of the next one's preconditions.
