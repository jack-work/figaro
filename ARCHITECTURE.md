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
├── main.go              CLI entry point, multi-call binary (figaro / q)
└── detach_unix.go       OS-specific process detach for daemon fork

internal/
├── angelus/
│   ├── angelus.go       Supervisor: socket listener, PID monitor, lifecycle
│   ├── protocol.go      Angelus-side JSON-RPC handlers (create/kill/list/bind/resolve)
│   ├── registry.go      In-memory figaro registry + PID↔figaro index
│   └── client.go        Typed client for talking to angelus
├── auth/
│   ├── auth.go          OAuth token manager with hush-encrypted storage
│   ├── resolver.go      TokenResolver interface (StaticKey / OAuthResolver)
│   ├── anthropic.go     Anthropic OAuth endpoint config
│   ├── login.go         Interactive PKCE login flow
│   └── pkce.go          PKCE challenge generation
├── config/
│   └── config.go        TOML config loading (~/.config/figaro/)
├── credo/
│   ├── credo.go         System prompt assembly (template + skills)
│   └── default_credo.md Embedded default personality
├── figaro/
│   ├── figaro.go        Figaro interface (ID, Prompt, Context, Info, Kill)
│   ├── agent.go         Single-inbox actor: event loop, LLM streaming, tool exec
│   ├── protocol.go      Agent-side JSON-RPC socket server + subscriber mgmt
│   └── client.go        Typed client for talking to a figaro agent
├── jsonrpc/
│   └── jsonrpc.go       Minimal JSON-RPC 2.0 client/server (NDJSON framing)
├── message/
│   └── message.go       Provider-agnostic message IR + Block type
├── otel/
│   └── otel.go          OpenTelemetry init (file exporter, span helpers)
├── provider/
│   ├── provider.go      Provider interface (Send, Models, SetModel)
│   └── anthropic/
│       └── anthropic.go Anthropic Messages API (direct HTTP+SSE, no SDK)
├── rpc/
│   ├── rpc.go           Shared notification types (Delta, ToolStart, Done, etc.)
│   └── methods.go       Method constants + request/response types for both sockets
├── store/
│   ├── store.go         Store interface (Context, Append, Branch, LeafTime)
│   ├── mem.go           In-memory store with optional downstream persistence
│   ├── file.go          JSON file store (atomic write-to-tmp + rename)
│   └── ariastore.go     Aria listing/removal for restore-on-restart
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
| `cmd/figaro` | 837 | ✅ working |
| `internal/angelus` | 762 | ✅ working, tested |
| `internal/auth` | ~350 | ✅ working (OAuth + PKCE) |
| `internal/config` | ~130 | ✅ working |
| `internal/credo` | ~210 | ✅ working, tested |
| `internal/figaro` | ~900 | ✅ working, tested |
| `internal/jsonrpc` | ~280 | ✅ working, tested |
| `internal/message` | ~130 | ✅ working |
| `internal/otel` | ~90 | ✅ working, tested |
| `internal/provider/anthropic` | ~450 | ✅ working |
| `internal/rpc` | 207 | ✅ working, tested |
| `internal/store` | ~370 | ✅ working, tested |
| `internal/tool` | 433 | ✅ working, tested |
| `internal/transport` | 99 | ✅ working, tested |
| **Total** | **~5,100** | |

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

### Provider-Agnostic IR with Baggage

Messages use a canonical `message.Message` type. Each message carries a `Baggage` map (`map[string]json.RawMessage`) keyed by provider name. The originating provider stashes its native wire format in baggage. On re-send to the same provider, it reads from baggage directly — no round-trip through the IR.

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

The agent runs inside `runWithRecovery`. On panic: log stack trace, reset store (re-seed from last FileStore checkpoint), inject crash system prompt, notify subscribers, restart the drain loop. The registry entry, PID bindings, and socket all survive.

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
└── providers/
    └── anthropic/
        ├── config.toml      # model, max_tokens, api_key
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
├── angelus.log              # supervisor log
├── rpc.jsonl                # CLI-side RPC log
├── traces.jsonl             # OpenTelemetry trace export
├── figaros/
│   ├── a1b2c3d4.jsonl       # per-agent event log
│   └── ...
└── arias/
    ├── a1b2c3d4.json        # persisted conversation (FileStore)
    └── ...
```

______________________________________________________________________

## JSON-RPC Protocol

### Angelus Socket (`angelus.sock`)

| Method | Direction | Purpose |
|--------|-----------|---------|
| `figaro.create` | CLI → Angelus | Create new agent (provider, model) |
| `figaro.kill` | CLI → Angelus | Terminate agent + remove aria |
| `figaro.list` | CLI → Angelus | List all agents with metadata |
| `pid.bind` | CLI → Angelus | Bind PID → figaro |
| `pid.resolve` | CLI → Angelus | Look up figaro for PID |
| `pid.unbind` | CLI → Angelus | Remove PID binding |
| `angelus.status` | CLI → Angelus | Uptime, counts |

### Figaro Socket (`<id>.sock`)

| Method | Direction | Purpose |
|--------|-----------|---------|
| `figaro.prompt` | CLI → Agent | Enqueue prompt (returns immediately) |
| `figaro.interrupt` | CLI → Agent | Cancel the current turn (Ctrl+C / Ctrl+D) |
| `figaro.context` | CLI → Agent | Get full message history |
| `figaro.info` | CLI → Agent | Agent metadata |
| `figaro.set_model` | CLI → Agent | Hot-swap model |
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
- Panic recovery with automatic restart
- OpenTelemetry tracing to file
- Configurable personality (credo.md + skills)

### 🔮 Future (*Il futuro*)

- **WAL-backed store** — replace atomic JSON with write-ahead log for crash safety
- **More providers** — OpenAI, local models. The `Provider` interface is ready.
- **Rich frontends** — browser, chat apps. Just JSON-RPC clients over sockets.
- **WebSocket transport** — `transport.go` abstracts Dial/Listen; websocket is next.
- **Agent pooling** — reusable processes assigned to arias on demand.
- **Network isolation** — sandbox boundaries for tool execution.
- **Context compaction** — summarize early conversation into Block.Header.
- **Child-process agents** — currently goroutines in the angelus; TODO: `--figaro` flag for full isolation.
