# 🎭 Figaro — Architecture

> *"Largo al factotum della città!"*

## Overview

Figaro is a supervisor-based CLI coding agent written in Go. A single static binary serves three roles — CLI frontend, supervisor daemon (angelus), and agent runtime — selected by invocation flags. All inter-component communication is **JSON-RPC 2.0** over **Unix domain sockets**.

```
┌─────────────────────────────────────────────────────────────────────┐
│                          PROCESS MODEL                              │
│                                                                     │
│  Terminal              ┌───────────┐                                │
│  ┌──────────┐   unix   │  Angelus  │                                │
│  │ CLI (q)  │◄────────►│ supervisor│                                │
│  │ figaro   │ angelus  │           │                                │
│  │          │  .sock   │  Registry │                                │
│  └────┬─────┘          │  PID Mon. │              ┌──────────┐      │
│       │                └─────┬─────┘              │  Figaro  │      │
│       │                      │ spawns/manages     │  Agent   │      │
│       │    unix              │                    │          │      │
│       └──────────────────────┼───────────────────►│  Actor   │      │
│            <id>.sock         │                    │  Store   │      │
│     (prompt + stream)        │                    │  Tools   │      │
│                              └───────────────────►└──────────┘      │
│   ephemeral              1 per user                N per user        │
└─────────────────────────────────────────────────────────────────────┘
```

**Three tiers, one binary:**

| Tier | Role | Lifetime | Socket |
|------|------|----------|--------|
| **CLI** | Stateless translator. Stdio ↔ JSON-RPC. | Ephemeral (one prompt) | connects to angelus + figaro |
| **Angelus** | Supervisor daemon. Registry, PID tracking, agent lifecycle. | Long-lived (per user) | `$XDG_RUNTIME_DIR/figaro/angelus.sock` |
| **Figaro Agent** | Actor-model AI agent. Owns conversation, provider, tools. | Long-lived (per conversation) | `$XDG_RUNTIME_DIR/figaro/figaros/<id>.sock` |

______________________________________________________________________

## Package Map

```
cmd/figaro/
├── main.go              CLI entry point, multi-call binary (figaro / q / l)
└── detach_unix.go       OS-specific process detach for daemon fork

internal/
├── angelus/
│   ├── angelus.go       Supervisor: socket listener, PID monitor, lifecycle
│   ├── protocol.go      Angelus-side JSON-RPC handlers (create/kill/list/bind/resolve); per-aria chalkboard.State + TranslationLog opening on create/RestoreArias
│   ├── registry.go      In-memory figaro registry + PID↔figaro index
│   └── client.go        Typed client for talking to angelus
├── auth/
│   ├── auth.go          OAuth token manager with hush-encrypted storage
│   ├── resolver.go      TokenResolver interface (StaticKey / OAuthResolver)
│   ├── anthropic.go     Anthropic OAuth endpoint config
│   ├── login.go         Interactive PKCE login flow
│   └── pkce.go          PKCE challenge generation
├── causal/
│   └── causal.go        Slice[T] / Sink[T] — typed prefix views for translators
├── config/
│   └── config.go        TOML config loading (~/.config/figaro/)
├── credo/
│   ├── credo.go         Credo body assembly (just the templated body now); Skill / SkillCatalogEntry / FormatSkillCatalog
│   └── default_credo.md Embedded default personality
├── figaro/
│   ├── figaro.go        Figaro interface (ID, Prompt, Context, Info, Kill, Rehydrate)
│   ├── agent.go         Single-inbox actor: event loop, LLM streaming, tool exec, in-progress-tic accumulation, bootstrap, rehydrate, translation-log persistence
│   ├── inbox.go         Selfish/patient priority mailbox
│   ├── protocol.go      Agent-side JSON-RPC socket server + subscriber mgmt
│   └── client.go        Typed client for talking to a figaro agent
├── jsonrpc/
│   └── jsonrpc.go       Minimal JSON-RPC 2.0 client/server (NDJSON framing)
├── message/
│   ├── message.go       Provider-agnostic Message IR + Block + Content types
│   ├── patch.go         Patch type (canonical home; chalkboard aliases it)
│   └── translation.go   ProviderTranslation (per-provider wire-format projection)
├── otel/
│   └── otel.go          OpenTelemetry init (file exporter, span helpers)
├── provider/
│   ├── provider.go      Provider interface (Name, Fingerprint, Models, SetModel, OpenAccumulator, Send); ProjectionSummary; NativeAccumulator
│   └── anthropic/
│       └── anthropic.go Anthropic Messages API (direct HTTP+SSE, no SDK); inline patch rendering as <system-reminder> blocks; per-message + system-block bytes captured in ProjectionSummary
├── rpc/
│   ├── rpc.go           Shared notification types (Delta, ToolStart, Done, etc.)
│   └── methods.go       Method constants + request/response types for both sockets
├── store/
│   ├── store.go         Store interface (Context, Append, Branch, LeafTime)
│   ├── mem.go           In-memory store with optional downstream persistence
│   ├── file.go          Aria FileStore — directory of {aria.jsonl, meta.json}
│   ├── file_backend.go  FileBackend (per-aria FileStore opener)
│   ├── ariastore.go     Aria listing/removal for restore-on-restart
│   └── translog.go      TranslationLog interface + FileTranslationLog (per-aria, per-provider NDJSON)
├── chalkboard/
│   ├── chalkboard.go    Snapshot, Patch (alias), Entry, Diff/Apply/Merge
│   ├── state.go         *State per-aria handle (snapshot + on-disk cache)
│   ├── render.go        Embedded text/template rendering, override loader
│   ├── lint.go          Phrasing lint (imperative / length / dup)
│   └── templates/       Embedded default body templates per key
├── tool/
│   ├── tool.go          Tool interface (Name, Execute, Parameters)
│   ├── bash.go          Shell command execution with streaming output
│   ├── read.go          File reading with offset/limit
│   ├── write.go         File writing (creates parent dirs)
│   └── edit.go          Find-and-replace text editing
└── transport/
    └── transport.go     Endpoint abstraction (unix/tcp), Dial/Listen helpers
```

### Lines of Code (approximate)

| Package | Lines | Status |
|---------|------:|--------|
| `cmd/figaro` | ~880 | ✅ working |
| `internal/angelus` | ~780 | ✅ working, tested |
| `internal/auth` | ~350 | ✅ working (OAuth + PKCE) |
| `internal/chalkboard` | ~600 | ✅ working, tested |
| `internal/config` | ~140 | ✅ working |
| `internal/credo` | ~210 | ✅ working, tested |
| `internal/figaro` | ~1100 | ✅ working, tested |
| `internal/jsonrpc` | ~280 | ✅ working, tested |
| `internal/message` | ~130 | ✅ working |
| `internal/otel` | ~90 | ✅ working, tested |
| `internal/provider/anthropic` | ~750 | ✅ working, tested |
| `internal/rpc` | ~240 | ✅ working, tested |
| `internal/store` | ~370 | ✅ working, tested |
| `internal/tool` | ~440 | ✅ working, tested |
| `internal/transport` | ~100 | ✅ working, tested |
| **Total** | **~6,400** | |

______________________________________________________________________

## Data Flow

### Prompt Lifecycle

```
User types: q explain this function

 1. CLI                  parses args, connects to angelus
 2. CLI → Angelus        pid.resolve(ppid) — find existing figaro for this shell
 3. Angelus              returns figaro ID + endpoint (or CLI creates + binds)
 4. CLI → Figaro         figaro.prompt("explain this function")
 5. Figaro               enqueues eventUserPrompt in actor inbox (returns immediately)
 6. Agent drain loop     processes eventUserPrompt:
    a. Build system prompt via Scribe (credo.md + skills + runtime context)
    b. Append user msg   → Store.Append (MemStore → FileStore on flush)
    c. Start LLM stream  → Provider.Send(block, tools, maxTokens)
 7. Provider (Anthropic) projects Block → native request, streams SSE
 8. SSE events           → eventLLMDelta → fanOut → stream.delta notification
 9. CLI receives         stream.delta → largo markdown renderer → terminal
10. If tool_use:         eventLLMDone → execute tools → eventToolResult → loop to 6c
11. If end_turn:         eventLLMDone → turnComplete → stream.done → CLI exits
12. turnComplete         counts tokens, flushes MemStore → FileStore, drains pending prompts
```

### Notification Flow (Figaro → CLI)

All notifications flow through the agent's `fanOut`:

```
Agent drain loop
    │
    ▼
fanOut ──┬── logEncoder (per-figaro .jsonl file)
         ├── channel subscribers (in-process, for tests)
         └── serverSubs (JSON-RPC Server.Notify → socket → CLI)
```

The CLI receives notifications synchronously on the `jsonrpc.Client` read loop — **wire-ordered, no reordering**.

______________________________________________________________________

## Key Design Decisions

### Actor Model (Single Inbox)

Each figaro agent is an actor. All events — user prompts, LLM deltas, tool results — enter through a single `chan event` inbox. One goroutine drains it. No concurrent dispatch, no races. Events carry a turn generation counter; stale events from crashed or completed turns are silently dropped.

```
eventUserPrompt  ──┐
eventLLMDelta    ──┤
eventLLMDone     ──┼──► inbox (chan event) ──► drainLoop (single goroutine)
eventToolOutput  ──┤
eventToolResult  ──┘
```

Prompts arriving while a turn is active are buffered in `pendingPrompts` and re-enqueued by `turnComplete` in FIFO order.

### Interruption (Ctrl+C / Ctrl+D)

Each prompt runs under a **turn-scoped** context derived from the agent's lifetime context. `prov.Send` and `tool.Execute` both receive this context, so cancelling it cleanly unwinds the LLM HTTP stream and any running tool subprocess.

1. The CLI catches `os.Interrupt` via `signal.NotifyContext` and (if stdin is a TTY) also watches stdin for EOF. Either triggers a context cancel.
2. On cancel, the CLI sends a `figaro.interrupt` JSON-RPC call and waits up to 3s for `stream.done`.
3. The agent enqueues a **selfish** `eventInterrupt` — it cuts the line ahead of pending LLM/tool events.
4. The drain loop handles it: sets `a.interrupted = true`, calls `turnCancel()`, emits `stream.error("interrupted") → stream.done("interrupted")`, and calls `endTurn` which drops pending tool calls and yields the inbox.
5. Stragglers from the cancelled provider/tool goroutines surface as `eventLLMError` / `eventToolResult`; the `a.interrupted` guard suppresses them silently — no panic, no races, no duplicate notifications.
6. The next prompt (patient event) releases through `Yield()` and the agent is fully reusable.

### Provider-Agnostic IR with Translation Cache

Messages use a canonical `message.Message` type. Per-aria, per-provider wire-format projections live in a **parallel timeline**: `arias/{id}/translations/{provider}.jsonl` (append-only NDJSON; one entry per figaro logical time covered, plus a system-block-array entry tied to the bootstrap flt). Entries are `{alt, figaro_lts, messages, fingerprint}`; `alt` is the translation timeline's own monotonic counter, decoupled from figaro lt; `figaro_lts` is the foreign-key list back to the figaro timeline.

The translation log is a **derivable cache**, not a source of truth: `aria.jsonl` is canonical, and the log can be regenerated by walking the figaro prefix through `Provider.EncodeOutbound`. On startup, if any persisted entry's fingerprint disagrees with `Provider.Fingerprint()`, the agent clears the log and lets it repopulate. Stage D.2f write-through persistence captures the wire bytes the provider *actually sent* on each Send (per-message + system-block array), so a restored aria sends byte-identical bytes back through the cache_control prefix.

On re-send to the same provider, the agent passes a `causal.Slice[ProviderTranslation]` parallel-indexed with `block.Messages` to `Provider.Send`; the provider reuses cached entries when present and re-renders from the IR on miss.

### Store Layering

```
Agent ──► MemStore ──► FileStore ──► disk
              │              │
          hot copy      atomic JSON
        (authoritative)  (checkpoint)
```

- **MemStore**: All reads/writes during a turn. Fast, in-process.
- **FileStore**: Flushed at turn boundaries. Atomic write-to-tmp + rename.
- **Restore**: On angelus restart, `RestoreArias` scans the store dir, re-creates agents from persisted metadata + messages.

### PID Binding

The angelus maintains a **strict 1:1 map** of `PID → figaro ID`. Your shell's PID is bound to one figaro at a time. The CLI resolves via `os.Getppid()`, so repeated invocations from the same shell reuse the same conversation.

- `figaro new` — unbinds, creates fresh
- `figaro attend <id>` — rebinds to a different figaro
- PID monitor (2s poll via `kill(pid, 0)`) reaps dead bindings

### Panic Recovery

The agent runs inside `runWithRecovery`. On panic: log stack trace to stderr, reset store (re-seed from last FileStore checkpoint), notify subscribers (advisory `error` + terminating `done`), restart the drain loop. The registry entry, PID bindings, socket, **and credo** all survive — recovery is invisible to the model. The only artifact is the stderr log line.

### Chalkboard

A **chalkboard** is the union of an aria's configuration and per-turn context — every structured value about the aria that is not a conversation message. Examples: cwd, datetime, model, label, project root, last truncation event. The `system.*` key namespace is harness-reserved (set at bootstrap, refreshed on `figaro.rehydrate`); clients must not write under it.

```
CLI ──prompt + context──▶ Agent (eventUserPrompt)
                            │
                            ├─▶ applyChalkboardInput → chalkboard.State.Apply
                            │      (patch attached to in-progress tic)
                            │
                            ▼
                          finalizeAndSend → memStore.Append(tic) → Provider.Send
                                    │
                                    ▼ (inside Provider)
                          projectMessages renders Patches as inline
                          <system-reminder> content blocks on the same
                          user-role wire message
```

The patch is the canonical unit (lives in `internal/message/`, aliased from `internal/chalkboard/`):

```go
type Patch struct {
    Set    map[string]json.RawMessage
    Remove []string
}
```

**Wire protocol.** `figaro.prompt` extends with an optional `chalkboard` field:

- `patch` only → apply patch directly
- `context` only (a snapshot) → server diffs vs persisted, applies the diff (system.* keys excluded from the diff)
- `context` + `patch` → diff for drift detection, patch on top
- neither → no-op

**Persistence.** Per-aria, in-line on the user-role tic. The tic Message carries `Patches []Patch` directly in `aria.jsonl`; the chalkboard's current snapshot is cached at `arias/{id}/chalkboard.json` (atomic rewrite at `endTurn` — `chalkboard.State.Save`). The figaro timeline is the source of truth; the snapshot is replayable from it.

**Bootstrap.** On a fresh aria, `NewAgent` runs the Scribe once, snapshots the system prompt + skills catalog into `chalkboard.system.{prompt,model,provider,skills}`, and emits a state-only tic carrying that patch. Subsequent turns read the system prompt from the chalkboard snapshot — Scribe doesn't re-run per turn.

**Rehydrate.** `figaro.rehydrate [--dry-run]` re-runs the Scribe and reloads skills from disk; the diff is applied as a state-only tic. Used after editing `~/.config/figaro/credo.md` or adding/removing skill files.

**Templates.** Each chalkboard key has a body template (Go `text/template`, embedded via `//go:embed` from `internal/chalkboard/templates/`). User overrides at `~/.config/figaro/chalkboard/<key>.tmpl`. Keys without a template are stored but not surfaced to the model. Templates today are read-only on `Entry.NewString` / `IsRemoval`; `Entry.Old` is plumbed but unused by the default templates.

**Provider rendering.** Patches are rendered **inline** as `<system-reminder name="…">…</system-reminder>` text blocks on the same user-role wire message that carries the tic's text and tool_results. There is no separate "renderer" surface anymore (the old `renderTag` / `renderTool` files retired in Stage C.3). Rendering happens per-tic during `projectMessages`, with a running snapshot threaded through the loop so a future "render full snapshot every turn" policy can swap in without restructuring.

**System blocks (Anthropic).** The system block array carries up to three blocks at projection time: the OAuth Claude Code identity prefix (when OAuth), the credo body (from `chalkboard.system.prompt`), and the skills catalog (from `chalkboard.system.skills`, formatted by `credo.FormatSkillCatalog`). Each is captured cache_control-free in the per-Send `ProjectionSummary` and persisted to the translation log keyed by the system flt.

**Cache control.** With the prefix byte-stable, `markCacheBreakpoints` sets `cache_control: ephemeral` on the last system block, the last tool definition, and the second-to-last message (the leaf at the most recent `endTurn` = everything that was on disk before the new prompt arrived). Caching engages on auth paths that allow client-controlled `cache_control`. The OAuth + `claude-code-20250219` beta path silently ignores it (verified empirically; documented in `markCacheBreakpoints` and in `plans/cache-control/prompt-caching-limitations.md`).

______________________________________________________________________

## Configuration Layout

```
~/.config/figaro/
├── config.toml              # default_provider, default_model, log settings
├── credo.md                 # system prompt template (Go text/template)
├── skills/                  # skill markdown files (name + description in frontmatter)
│   ├── websearch.md
│   ├── docker.md
│   └── ...
├── chalkboard/              # optional per-key body template overrides
│   ├── cwd.tmpl
│   └── ...
└── providers/
    └── anthropic/
        ├── config.toml      # model, max_tokens, api_key, reminder_renderer ("tag"|"tool")
        └── auth.toml        # hush-encrypted OAuth tokens
```

## Runtime Layout

```
$XDG_RUNTIME_DIR/figaro/     # (or /tmp/figaro)
├── angelus.sock             # supervisor socket
├── angelus.pid              # for clean shutdown
└── figaros/
    ├── a1b2c3d4.sock        # per-agent sockets
    └── ...

~/.local/state/figaro/
├── angelus.log              # supervisor log (also captures stderr from provider/agent)
├── rpc.jsonl                # CLI-side RPC log
├── traces.jsonl             # OpenTelemetry trace export
├── figaros/
│   ├── a1b2c3d4.jsonl       # per-agent event log
│   └── ...
└── arias/
    ├── a1b2c3d4/
    │   ├── aria.jsonl       # canonical figaro timeline (NDJSON of message.Message)
    │   ├── chalkboard.json  # cached current chalkboard snapshot (atomic rewrite at endTurn)
    │   ├── meta.json        # AriaMeta (provider, model, cwd, root, label) — transitional
    │   └── translations/
    │       └── anthropic.jsonl  # per-provider translation cache, append-only NDJSON
    └── ...
```

Patches no longer have their own `chalkboards/<id>/log.json` — they ride on the user-role tic Messages in `aria.jsonl` directly (Stage C). Translation cache is per-provider in its own NDJSON file (Stage D.2d), not a field on Message.

______________________________________________________________________

## JSON-RPC Protocol

### Angelus Socket (`angelus.sock`)

| Method | Direction | Purpose |
|--------|-----------|---------|
| `figaro.create` | CLI → Angelus | Create new agent (provider, model, ephemeral) |
| `figaro.kill` | CLI → Angelus | Terminate agent + remove aria |
| `figaro.list` | CLI → Angelus | List all agents with metadata |
| `pid.bind` | CLI → Angelus | Bind PID → figaro |
| `pid.resolve` | CLI → Angelus | Look up figaro for PID |
| `pid.unbind` | CLI → Angelus | Remove PID binding |
| `angelus.status` | CLI → Angelus | Uptime, counts |
| `angelus.save_bindings` | CLI → Angelus | Persist PID bindings before `figaro rest` (`--keep-pids`) |

### Figaro Socket (`<id>.sock`)

| Method | Direction | Purpose |
|--------|-----------|---------|
| `figaro.prompt` | CLI → Agent | Enqueue prompt (returns immediately); optional `chalkboard` field carries patch / context |
| `figaro.interrupt` | CLI → Agent | Cancel the current turn (Ctrl+C / Ctrl+D) |
| `figaro.context` | CLI → Agent | Get full message history |
| `figaro.info` | CLI → Agent | Agent metadata |
| `figaro.set_model` | CLI → Agent | Hot-swap model |
| `figaro.set_label` | CLI → Agent | Set human-readable aria label |
| `figaro.rehydrate` | CLI → Agent | Re-run Scribe + reload skills; emit state-only tic with the diff (`--dry-run` returns the diff without applying) |
| `stream.delta` | Agent → CLI | LLM text chunk (notification) |
| `stream.thinking` | Agent → CLI | Extended thinking chunk |
| `stream.tool_start` | Agent → CLI | Tool execution beginning |
| `stream.tool_output` | Agent → CLI | Streaming tool output chunk |
| `stream.tool_end` | Agent → CLI | Tool result (success/error) |
| `stream.message` | Agent → CLI | Full message appended to store |
| `stream.done` | Agent → CLI | Turn complete |
| `stream.error` | Agent → CLI | Error notification |

______________________________________________________________________

## CLI Commands

| Command | Description |
|---------|-------------|
| `q <prompt>` / `figaro -- <prompt>` | Prompt (auto-resolves or creates figaro for shell) |
| `figaro new -- <prompt>` | New conversation + prompt |
| `figaro list` | List all figaros (ID, state, model, messages, PIDs) |
| `figaro kill <id>` | Kill a figaro + delete its aria |
| `figaro attend <id>` | Rebind this shell to a different figaro |
| `figaro context` | Dump current figaro's message history as JSON |
| `figaro models` | List available models from all providers |
| `figaro login <provider>` | OAuth PKCE login flow |
| `figaro rest` | Shut down the angelus daemon |

______________________________________________________________________

## Dependencies

| Dependency | Purpose |
|------------|---------|
| [`hush`](https://github.com/jack-work/hush) | Secret encryption at rest (age-based, agent model) |
| [`largo`](https://github.com/jack-work/largo) | Incremental streaming markdown rendering |
| `go.opentelemetry.io/otel` | Distributed tracing (file exporter) |
| `BurntSushi/toml` | TOML config parsing |
| `google/uuid` | Agent ID generation |
| `stretchr/testify` | Test assertions |

Both `hush` and `largo` are local replace directives (active development).

______________________________________________________________________

## Status & Roadmap

### ✅ Working

- Supervisor architecture (angelus + agent lifecycle)
- Actor-model event loop (single inbox, no races)
- JSON-RPC 2.0 over Unix sockets (custom, ordered, no reordering)
- Anthropic provider (direct HTTP+SSE, OAuth + API key)
- Tool execution (bash, read, write, edit) with streaming output
- Incremental markdown rendering via largo
- Conversation persistence (MemStore → FileStore, restore on restart)
- PID binding + dead-PID reaping
- OAuth PKCE login with hush-encrypted token storage
- Panic recovery with automatic restart (credo persists across panics)
- OpenTelemetry tracing to file
- Configurable personality (credo.md + skills)
- Chalkboard: structured per-aria state surfaced as system reminders, persisted as append-only patch log + cached snapshot
- `cache_control` wiring on the Anthropic provider (system / tools / leaf-1 message); cache hit measurement via `figaro list` CACHE column

### 🔮 Future (*Il futuro*)

- **WAL-backed store** — replace atomic JSON with write-ahead log for crash safety
- **More providers** — OpenAI, local models. The `Provider` interface is ready.
- **Rich frontends** — browser, chat apps. Just JSON-RPC clients over sockets.
- **WebSocket transport** — `transport.go` abstracts Dial/Listen; websocket is next.
- **Agent pooling** — reusable processes assigned to arias on demand.
- **Network isolation** — sandbox boundaries for tool execution.
- **Context compaction** — summarize early conversation into Block.Header.
- **Child-process agents** — currently goroutines in the angelus; TODO: `--figaro` flag for full isolation.
