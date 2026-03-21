# Figaro Angelus — Implementation Plan

## Overview

A per-user process supervisor (angelus) managing long-lived AI agent sessions (figaros),
with short-lived CLI processes that attach and detach. All IPC is JSON-RPC 2.0 over unix
sockets, making every component a language-agnostic service.

## CLI Ergonomics

Prompts use POSIX `--` to separate from subcommands. Everything after `--` is the prompt:

```
figaro -- explain this code       # prompt (resolved via ppid → figaro)
figaro new -- start fresh         # unbind old figaro, create new, bind, prompt
figaro context                    # show chat history (resolved via ppid)
figaro context <id>               # show chat history for specific figaro
figaro list                       # list all figaros
figaro kill <id>                  # kill a figaro
figaro models                     # list available models
figaro login <provider>           # OAuth login (client-side, no angelus)
```

### PID Index Invariant

The pid→figaro index is a strict **1:1 map**. A pid maps to exactly one figaro.

- `Bind(pid, figaro_id)` always unbinds the pid first if it was bound elsewhere.
- The registry enforces this at the data structure level — attempting to create a
  duplicate mapping returns an error (belt and suspenders beyond the auto-unbind).
- `figaro new` flow: Resolve(ppid) → Unbind(ppid) → Create() → Bind(ppid, new_id).
  The old figaro is NOT killed — it goes idle. The pid just points to the new one.

## Architecture

```
Frontend (any language)
  │
  ├── supervisor socket ──► Angelus (registry, pid index, lifecycle)
  │                          angelus.sock
  │
  └── agent socket ────────► Figaro (conversation, prompt FIFO, subscribers)
                              figaros/<id>.sock
```

Supervisor is consulted for session resolution only. All agent interaction is direct.

## Dependencies

- `github.com/creachadair/jrpc2` — typed JSON-RPC 2.0 (used by gopls)
- `go.opentelemetry.io/otel` — observability, file exporter for now
- `github.com/stretchr/testify` — assertions + require for tests
- `golang.org/x/sys/unix` — PID monitoring via kill(pid, 0)
  // NOTE: Linux/macOS only. Windows will need build-tagged alternative.

## Steps

### Step 1: OpenTelemetry foundation ✅
- [x] Add otel dependency
- [x] Create `internal/otel/otel.go` — tracer provider, file exporter, span helpers
- [x] Write unit tests verifying spans are recorded (4 tests)
- [x] Wire into `cmd/figaro/main.go` (init on startup, flush on shutdown)
- [x] Update CLI to use `--` prompt separator
- **Validated**: `figaro -- hello` works, `~/.local/state/figaro/traces.jsonl` contains `figaro.prompt` span

### Step 2: JSON-RPC foundation + shared types ✅
- [x] Add `creachadair/jrpc2` dependency
- [x] Refactor `internal/rpc/rpc.go` — notification types (no figaro_id, direct socket)
- [x] Define `internal/rpc/methods.go` — method constants + typed request/response structs for both protocols
- [x] Write fixture-based tests for serialization round-trips (10 tests)
- **Validated**: `go test ./internal/rpc/...` passes, all fixtures round-trip cleanly
- **Fixtures**: `testdata/` with 8 JSON fixtures covering both protocols

### Step 3: Figaro agent package ✅ (core)
- [x] Create `internal/figaro/figaro.go` — interface definition (Figaro + FigaroInfo)
- [x] Create `internal/figaro/agent.go` — goroutine implementation
  // TODO: convert to child process via --figaro flag
- [x] Prompt FIFO queue (buffered channel, single drain goroutine — actor model)
- [x] Subscriber fan-out (multiple channels, add/remove, non-blocking send)
- [x] Unit tests: 9 tests with mock provider (prompt→response, context, FIFO ordering,
  multi-subscriber, unsubscribe, kill, info)
- [ ] Create `internal/figaro/protocol.go` — jrpc2 handler map (prompt, context, subscribe, info)
- [ ] Create `internal/figaro/client.go` — typed client for connecting to figaro socket
- [ ] Panic recovery with crash prompt injection
- **Validated**: all 9 tests pass; figaro processes prompts FIFO, fans out to multiple subscribers
- NOTE: protocol.go and client.go deferred to Step 5 (needs angelus wiring first)

### Step 4: Angelus supervisor package ✅ (core)
- [x] Create `internal/angelus/registry.go` — figaro registry, pid index
  - pid index is strict 1:1 (auto-unbind on rebind, no-op on same-bind)
  - Reverse index: figaro → []pid for cleanup on Kill
  - 16 tests covering all invariants
- [x] Create `internal/angelus/angelus.go` — supervisor struct, Run(), socket listener
  - PID monitor goroutine (poll 2s, kill(pid, 0), unbind dead PIDs)
  // NOTE: uses golang.org/x/sys/unix. Windows needs build-tagged alternative.
  - Stale socket cleanup on startup
  - 6 tests (socket creation, PID reaping, stale cleanup, etc.)
- [ ] Create `internal/angelus/protocol.go` — jrpc2 handler map for supervisor methods
- [ ] Create `internal/angelus/client.go` — typed client for CLI → supervisor
- **Validated**: 22 tests pass; registry invariants verified, PID monitor reaps dead PIDs
- NOTE: protocol.go and client.go deferred to Step 5 (wiring)

### Step 5+6: CLI + main.go wiring ✅
- [x] `--angelus` flag dispatches to supervisor mode (fork/exec with Setsid)
- [x] Auto-start angelus on first CLI use (fork, wait for socket)
- [x] POSIX `--` prompt parsing, subcommand dispatch
- [x] `figaro -- <prompt>`: resolve ppid → figaro, create if needed, prompt, display response
- [x] `figaro new -- <prompt>`: unbind old, create new, bind, prompt
- [x] `figaro list`: table of all figaros
- [x] `figaro kill <id>`: kill a figaro
- [x] `figaro context`: show chat history for current shell's figaro
- [x] `figaro models`: list available models (client-side)
- [x] `figaro login <provider>`: OAuth flow (client-side)
- [x] ProviderFactory injected into angelus for creating providers
- [x] Platform-specific detach (detach_unix.go with Setsid)
- [ ] Proper notification streaming (currently polls context instead)
- **Validated**: full manual flow works:
  - `figaro -- "What is 2+2?"` → auto-starts angelus, creates figaro, returns "4"
  - `figaro list` → shows figaro with 2 messages
  - `figaro kill <id>` → removes it
  - 55 tests pass

### Post-steps: completed since step 6
- [x] Notification streaming restored (word-by-word via jrpc2 server push)
- [x] Sequenced event envelopes (fix jrpc2 reordering bug)
- [x] Otel in angelus process + figaro span attributes
- [x] SimpleSpanProcessor (no drops)
- [x] Delta tracing on both figaro emit and CLI receive sides
- [x] stream.thinking rendering (basic stderr)
- [x] Conversation continuity verified (os.Getppid() returns stable shell pid)

## Remaining Work

### Step 7: Panic recovery
- [ ] Wrap figaro drain loop in recover()
- [ ] On panic: log stack trace, restart goroutine, inject crash system prompt
- [ ] Registry entry (id, pid bindings) survives — only chat context lost
- [ ] Test: mock provider that panics, verify figaro restarts and is usable

### Step 8: System prompt from config
- [ ] Add `system_prompt` field to provider config TOML
- [ ] Angelus reads it when creating figaros
- [ ] Fallback to sensible default if not set
- [ ] Test: fixture config with custom prompt, verify it reaches the agent

### Step 9: RPC event logging
- [ ] Restore JSONL event log (all notifications logged to file)
- [ ] Log path from config (existing log.rpc_file)
- [ ] Angelus writes events from all figaros (tagged with figaro ID)
- [ ] Or: each figaro writes its own event log

### Step 10: Chat persistence
- [ ] JSONL append log per figaro in ~/.local/state/figaro/figaros/<id>/
- [ ] On figaro restart, reload context from log
- [ ] Crash prompt no longer needed once persistence works
- [ ] Test: create figaro, prompt, kill angelus, restart, verify context survives

### Step 11: Tool execution
- [ ] Wire existing tools (bash, read, write, edit) into figaro config
- [ ] Tools passed to agent.Agent when constructing in processPrompt
- [ ] Tool execution renders stream.tool_start / stream.tool_end in CLI
- [ ] Test: mock tool, verify tool call → result → assistant response flow
- [ ] Security: confirm tool execution runs in angelus process (or future child process)

## Package Layout

```
cmd/
  figaro/
    main.go

internal/
  cli/
    cli.go
  angelus/
    angelus.go
    protocol.go
    registry.go
    client.go
  figaro/
    figaro.go          # interface
    agent.go           # goroutine impl + socket listener
    protocol.go        # jrpc2 handlers
    client.go          # typed client
  rpc/
    rpc.go             # shared notification types
    methods.go         # method name constants
  otel/
    otel.go            # tracer provider, file exporter, helpers
  config/
    config.go          # (existing) provider config
  auth/
    auth.go            # (existing) OAuth + token management
  provider/
    provider.go        # (existing) provider interface
    anthropic/
      anthropic.go     # (existing) Anthropic implementation
  agent/
    agent.go           # (existing) tic loop — used inside figaro goroutine
  message/
    message.go         # (existing) IR types
  store/
    store.go           # (existing) store interface
    mem.go             # (existing) in-memory store
```
