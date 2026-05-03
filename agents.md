# agents.md

> *"Largo al factotum della città!"*

A standing brief for any AI agent (Claude, otherwise) editing this repository. Read this before touching code. Update it when the truth changes — see [§ Living document](#living-document).

This file is the **first stop**. It does not duplicate `README.md` or `ARCHITECTURE.md`; it points at them and adds the things an agent needs to act well — invariants, conventions, hot spots, and the rules of engagement.

---

## Orientation

- **What this is.** Figaro is a Go CLI coding agent with a supervisor architecture. One static binary plays three roles (CLI / angelus / agent) selected by invocation. All IPC is JSON-RPC 2.0 over Unix sockets. See `README.md`.
- **How it's wired.** Process model, package map, data flow, and design decisions live in `ARCHITECTURE.md`. Treat it as authoritative; if you contradict it, update it in the same change.
- **Design plans.** `plans/*.md` capture intent and rationale for major subsystems (angelus, aria persistence, auth, graceful rest, largo integration). Useful for *why* — but the code is the source of truth for *what*.
- **Personality.** `internal/credo/default_credo.md` shapes the agent's user-facing voice (concise, Italian/Spanish flourishes used sparingly). The same voice appears in user-facing prose throughout this repo. Match it lightly; do not parody it.

## Build, run, test

```bash
go build ./...
go test ./...
go vet ./...

# fast smoke
go run ./cmd/figaro -- "buongiorno"

# nix
nix build
```

- Go module: `github.com/jack-work/figaro`. Go 1.25.
- `hush` and `largo` are local replace directives in active development — expect upstream churn. Don't pin or vendor without asking.
- CGO disabled (`CGO_ENABLED=0`) for the nix build; keep it that way unless explicitly told otherwise.

**Nix support is a priority.** `flake.nix` is a first-class build path, not an afterthought. Whenever you change anything that affects how the binary is built, consider whether the flake needs to follow:

- Adding/changing dependencies in `go.mod` / `go.sum` → `vendorHash` in `flake.nix` will need to be updated (build will fail with the expected vs. actual hash; copy the new one in).
- Adding a new `cmd/` entry point → update `subPackages`.
- Changing build flags, version injection, or CGO posture → update `ldflags` / `env`.
- Adding tooling the dev shell needs → update `devShells.default.buildInputs`.

If you're not sure whether a change touches the flake, run `nix build` and find out. Surface flake updates explicitly in your summary so the user can audit.

## Package map (one line each)

`cmd/figaro/` — entry point. Multi-call binary: `figaro`, `q`, `l`. Dispatches subcommands and the `--angelus` daemon mode.
`internal/angelus/` — supervisor: registry, PID monitor, agent lifecycle, JSON-RPC handlers.
`internal/auth/` — OAuth + PKCE, token resolver, hush-encrypted storage.
`internal/config/` — TOML config loader.
`internal/credo/` — system-prompt assembly (template + skills).
`internal/figaro/` — the actor: agent loop, single-inbox event drain, protocol server, client.
`internal/jsonrpc/` — minimal NDJSON-framed JSON-RPC 2.0 client/server.
`internal/message/` — provider-agnostic IR (Message, Block, Patch, ProviderTranslation).
`internal/causal/` — typed prefix views (CausalSlice, CausalSink) for handing the past to a translator without exposing the future.
`internal/otel/` — OpenTelemetry init + span helpers.
`internal/provider/` — `Provider` interface; `anthropic/` is the only implementation.
`internal/rpc/` — shared notification types and method constants for both sockets.
`internal/store/` — `Store` interface, `MemStore`, `FileStore`, `Backend`, aria management, per-aria `TranslationLog` (parallel timeline at `arias/{id}/translations/{provider}.jsonl`).
`internal/tokens/` — context-window token accounting.
`internal/chalkboard/` — per-aria structured state record. `*State` per-aria handle, snapshots, patches (now living canonically in `internal/message/` and aliased here), embedded body templates. Surfaced to providers as system reminders. The `system.*` key namespace is harness-reserved (set at bootstrap, refreshed on `figaro.rehydrate`); clients must not write under it.
`internal/tool/` — tool interface + bash/read/write/edit tools.
`internal/transport/` — endpoint abstraction (unix/tcp), Dial/Listen.

If you add or rename a package, update this list and `ARCHITECTURE.md` in the same change.

## Conventions

- **Comments:** default to none. Only write a comment when the *why* is non-obvious — a hidden constraint, a workaround, a subtle invariant. Don't restate what the code says. No comments that name callers or tasks ("used by X", "added for Y") — git log handles that. This applies to doc comments too: package-level docs are welcome; function-level docs only when the function's contract isn't already obvious.
- **Errors:** wrap with `%w` when context matters; return raw when the caller will already have context. Don't swallow.
- **Concurrency:** prefer the actor model. Inside a figaro, *do not* introduce new goroutines that touch agent state without going through the inbox. See [§ Invariants](#invariants).
- **Logging:** structured where it matters; avoid `fmt.Printf` debug residue in committed code. The agent has its own per-id `.jsonl` event log via `fanOut` — that's the place for runtime traces.
- **User-facing strings:** terse, with the occasional Italian/Spanish flourish where it doesn't slow comprehension. CLI output is read by humans during real work; clarity beats charm.
- **No emojis** in code or commits unless asked. Running prose may use them sparingly where the existing style does.
- **Imports:** group stdlib / third-party / local with blank lines between groups, `goimports` order.
- **Tests:** `testify` is in. Prefer table-driven tests for parsers and pure functions; integration tests in `_test.go` next to the code they exercise. `internal/angelus/integration_test.go` is the model for cross-component tests.
- **Commits:** for stage-shaped work that spans more than a few files (the C and D stages are a model), break the work into discrete sub-stages — each commit reviewable in isolation, each leaving the tree green at `go build && go test && go vet`. Name them `Stage X.N — short summary` in commit subject; the body explains what changed and why. When a sub-stage adds scaffolding for future work, include a comment in the affected code that points at the future direction so the next reader doesn't think it's stranded.
- **Live verification:** when changes touch the wire path or persistence, run a real prompt through the binary before declaring done. A throwaway script under `/tmp/verify_*.sh` that installs the binary, restarts the angelus, drives a representative conversation, and inspects on-disk state is the standard pattern. Unit tests verify code; live verification verifies behavior.
- **Forward-only refactors in dev mode.** No back-compat shims for old on-disk shapes — when the format changes, document the migration (or back up + delete old data) rather than carrying a dual-read path. The exception is when the user explicitly asks for back-compat.

## Invariants

These are the load-bearing rules. Breaking them produces races, lost messages, or silent corruption. Don't relax one without a plan and a conversation.

1. **Single inbox per figaro.** Every event — user prompt, LLM delta, tool result, interrupt — enters the agent through one `chan event` and is drained by one goroutine. New goroutines that touch agent state must report results back through the inbox, not by mutating fields directly.
2. **Turn generation counter.** Stale events from cancelled or completed turns are silently dropped via the generation guard. Don't bypass it. If you add a new event, route it through the same guard.
3. **Translation log is a derivable cache, not a source of truth.** Per-aria, per-provider wire-format projections live in `arias/{id}/translations/{provider}.jsonl`, append-only NDJSON, keyed by figaro logical times. Entries carry the provider's encoder fingerprint at write time. On `Provider.Fingerprint()` mismatch the agent clears the log and lets it repopulate. The `aria.jsonl` figaro timeline is canonical; the translation log is regenerable from it via `Provider.EncodeOutbound` (post-D.2). Don't treat translation entries as authoritative — if you need the truth, re-derive from the figaro timeline.
4. **Store layering.** `MemStore` is the hot, authoritative copy during a turn. `FileStore` is a checkpoint flushed at turn boundaries via atomic write-to-tmp + rename. Reads during a turn go to MemStore; persistence is the agent's job at `turnComplete`, not the store's job on every `Append`.
5. **PID binding is 1:1.** A shell PID maps to at most one figaro at a time. `pid.bind` / `pid.unbind` / `pid.resolve` go through the angelus. Don't add a side path that mutates this map.
6. **Panic recovery preserves identity.** `runWithRecovery` resets the store to the last FileStore checkpoint and restarts the drain loop. The figaro's id, registry entry, PID bindings, socket, **and credo** all survive a panic — recovery is invisible to the model (logged to stderr only). Anything that lives outside the agent must tolerate a drain-loop restart without leaking.
7. **Interrupt cuts the line.** `eventInterrupt` is a *selfish* event: it jumps ahead of pending LLM/tool events. Stragglers from cancelled provider/tool goroutines are suppressed by the `a.interrupted` guard. Keep them suppressed — surfacing them is a regression.
8. **Notification ordering is wire-ordered.** The CLI receives notifications synchronously on the JSON-RPC client read loop. No reordering, no parallel dispatch. New notification types must be emitted from the drain loop (or the fanOut path it owns), not from arbitrary goroutines.
9. **Secrets never hit disk in plaintext.** Tokens go through `hush`. Don't read or log credentials. Don't write a "convenience" path that bypasses the encrypted store.
10. **One static binary.** No new runtime dependencies (Node, Bun, Python). New tools, providers, and frontends must be reachable through the existing socket protocol — not bundled into the binary.
11. **Cache prefix is byte-stable.** The conversation prefix sent to providers — system blocks, tools, and all messages up through the leaf at the most recent `endTurn` — must be byte-identical across requests within an aria's lifetime, modulo deliberate edits to `~/.config/figaro/credo.md` or `skills/`. Anthropic's `cache_control` breakpoints depend on this. Never mutate `block.Header` or earlier `block.Messages` mid-session. Chalkboard reminders attach to the leaf user message only — never to the prefix. Compaction (future) is the one event that legitimately rewrites the prefix.
12. **Harness does not inject overrides.** Never insert content that voids prior instructions or pretends to speak as the system mid-conversation ("ignore previous", "IMPORTANT: …", staticScribe-style replacements). State changes flow exclusively through the chalkboard and its renderers. The credo persists across panics, model switches, and interrupts; only deliberate user edits to `credo.md` change the agent's identity.

## Hot spots

Places where small changes have outsized blast radius. Read carefully, test thoroughly, and consider asking before editing.

- `internal/figaro/agent.go` — the drain loop. Event dispatch, turn lifecycle, interrupt handling, panic recovery, chalkboard application, in-progress-tic accumulation, translation-log persistence (`persistProjectionSummary`), bootstrap + rehydrate orchestration. Long file; cohesive on purpose.
- `internal/figaro/inbox.go` — the inbox itself, including selfish vs. patient event semantics.
- `internal/angelus/angelus.go` + `registry.go` + `protocol.go` — supervisor lifecycle, PID monitor, draining shutdown. `protocol.go` opens per-aria `chalkboard.State` + `TranslationLog` on `create` and on lazy restoration via `Handlers.Restore(ariaID)` (called from `RestoreBindings` at startup, and from `pid.bind` / `figaro.list` for dormant arias). Arias on disk without a live registry entry are *dormant* — present in `figaro list`, killable, but not consuming an Agent goroutine until first access.
- `internal/store/file.go` + `mem.go` — flush ordering and atomic-rename semantics. Easy to break crash safety here.
- `internal/store/translog.go` — append-only NDJSON translation log per (aria, provider). On-disk shape change requires version negotiation.
- `internal/chalkboard/state.go` — per-aria `*State` handle (in-memory snapshot + cached file). Single-owner under the actor model; no locking today.
- `internal/provider/anthropic/anthropic.go` — direct HTTP+SSE, no SDK. Streaming parser is hand-rolled. `projectMessages` renders patches as inline `<system-reminder>` content blocks; `projectBlockWithModel` returns a `provider.ProjectionSummary` for the agent to persist. Renderer choice (`ReminderRenderer`) feeds into `Fingerprint()` — must never mutate the cache prefix (invariant #11).
- `internal/jsonrpc/jsonrpc.go` — NDJSON framing. Don't switch frame formats without updating both ends and the protocol tables in `ARCHITECTURE.md`.
- `cmd/figaro/main.go` — multi-call dispatch, daemon fork, signal handling. The `q` and `l` symlinks are part of the contract (provided as `postInstall` symlinks in `flake.nix`).

## Disclosure rules

Take freely-reversible local actions. Pause and ask before doing anything in the lists below.

**Always disclose and confirm before:**

- Changing any **JSON-RPC method, notification, or wire payload** (both socket tables in `ARCHITECTURE.md`). Frontends in any language are part of the contract.
- Changing the **on-disk aria, chalkboard, or translation-log format** (`internal/store/file.go`, `internal/store/translog.go`, `internal/chalkboard/state.go`) or any layout under `~/.config/figaro/` or `~/.local/state/figaro/`. Old data must keep loading or migrate explicitly. The current per-aria layout is `arias/{id}/{aria.jsonl, meta.json, chalkboard.json, translations/{provider}.jsonl}`.
- Anything that mutates the **cache prefix** mid-session (system block, tools, or earlier messages). Invariant #11.
- Touching **OAuth / hush flows** in `internal/auth/`. Tokens are users' real credentials.
- Adding a **new runtime dependency**, replace directive, or external service.
- Removing or renaming a **CLI subcommand or flag**. Users have muscle memory and shell history.
- Anything that requires **network egress** the project doesn't already make (the only outbound today is the configured provider).

**Surface but don't block on:**

- Refactors that cross more than two packages.
- Changes to the actor model, store layering, or other items in [§ Invariants](#invariants).
- New top-level files in the repo root.
- Changes to `agents.md`, `README.md`, `ARCHITECTURE.md`, or `credo.md` voice. Edits to facts are fine; edits to tone deserve a sentence of explanation.

**Just do (and mention in the summary):**

- Bug fixes scoped to a single package.
- Tests, including new integration tests.
- Comment cleanup, dead-code removal you're certain is dead.
- Doc fixes that correct stale facts.

## Working with plans

`plans/*.md` are *intent* documents — they describe a design before or during implementation. Treat them as historical record once the work lands. Don't edit a plan to reflect what was actually built; that's what the code and `ARCHITECTURE.md` are for. If a plan is misleading because the implementation diverged, add a short note at the top (`Status: superseded by …`) rather than rewriting.

New plans are welcome for non-trivial work. Keep them in `plans/`, mirror the existing voice, and link them from `ARCHITECTURE.md` if they describe a subsystem that needs ongoing reference.

## Working with `duck/`

`duck/*.md` is for **rubber-duck brainstorms** — captured thinking from design conversations, lower commitment than `plans/`. See `duck/README.md` for the convention. Each file is a session: the user's reasoning preserved as faithfully as possible, with feedback / open questions clearly separable. When the conversation distills into a decision, fold it into a `plans/*.md` and link the duck file as the source. When you spawn a new brainstorm, name it `YYYY-MM-DD-topic.md` and stamp a status (`brainstorm`, `superseded`, `accepted`, `archived`).

The most recent duck doc usually represents in-flight design thinking that hasn't reached `plans/` yet. **Read it before starting on architecturally significant work.**

## Living document

This file is **maintained by agents, audited by the human**. Update it as the project changes — *don't* let it drift.

- **Just edit** for: package added/renamed, build commands changed, a new invariant emerges from a bug fix, a hot-spot file is split, a stale pointer.
- **Edit and mention in the response** for: changes to the disclosure rules, conventions, or the structure of this file.
- **Propose first** for: removing an invariant, narrowing the disclosure rules, replacing a section wholesale, or any change that would alter how *future* agents behave on tasks the user hasn't yet given them.
- **Ask the user** when you're uncertain whether a rule still holds (e.g., a memory or comment says one thing, the code says another).

When you make non-trivial edits to this file in the course of other work, surface them clearly in the end-of-turn summary so the user can audit. The point of this document is to compress hard-won knowledge — let it grow when the project teaches you something, prune it when a rule outlives its reason.

---

*Tutti mi chiedono, tutti mi vogliono.*
