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

### Step 1: OpenTelemetry foundation
- [ ] Add otel dependency
- [ ] Create `internal/otel/otel.go` — tracer provider, file exporter, span helpers
- [ ] Write unit tests verifying spans are recorded
- [ ] Wire into `cmd/figaro/main.go` (init on startup, flush on shutdown)
- **Validate**: `figaro -p "hello"` works, trace file appears at `~/.local/state/figaro/traces.jsonl`
- **Fixture**: `testdata/expected_span.json` for span shape validation

### Step 2: JSON-RPC foundation + shared types
- [ ] Add `creachadair/jrpc2` dependency
- [ ] Refactor `internal/rpc/rpc.go` — notification types carry no figaro_id (direct socket)
- [ ] Define `internal/rpc/methods.go` — method name constants for both protocols
- [ ] Write fixture-based tests for serialization round-trips
- **Validate**: `go test ./internal/rpc/...` passes
- **Fixture**: `testdata/` with JSON-RPC request/response/notification samples

### Step 3: Figaro agent package
- [ ] Create `internal/figaro/figaro.go` — interface definition
- [ ] Create `internal/figaro/agent.go` — goroutine implementation
  // TODO: convert to child process via --figaro flag
- [ ] Create `internal/figaro/protocol.go` — jrpc2 handler map (prompt, context, subscribe, info)
- [ ] Create `internal/figaro/client.go` — typed client for connecting to figaro socket
- [ ] Prompt FIFO queue (buffered channel, single drain goroutine)
- [ ] Subscriber fan-out (multiple channels, add/remove)
- [ ] Panic recovery with crash prompt injection
- [ ] Unit tests: mock provider, verify prompt→response flow, subscriber delivery, FIFO ordering
- **Validate**: unit tests pass; can construct a figaro in a test, send a prompt, receive notifications
- **Fixture**: `testdata/` with sample prompts and expected notification sequences

### Step 4: Angelus supervisor package
- [ ] Create `internal/angelus/angelus.go` — supervisor struct, Run(), socket listener
- [ ] Create `internal/angelus/registry.go` — figaro registry, pid index, Create/Kill/Bind/Resolve
  - pid index is a strict 1:1 map (one pid → one figaro)
  - Bind(pid, id) auto-unbinds the pid if already bound elsewhere
  - Bind returns error if pid is already bound to the same id (no-op guard)
  - Reverse index: figaro → []pid for Info/cleanup
- [ ] Create `internal/angelus/protocol.go` — jrpc2 handler map for supervisor methods
- [ ] Create `internal/angelus/client.go` — typed client for CLI → supervisor
- [ ] PID monitor goroutine (poll 2s, kill(pid, 0), unbind dead PIDs)
  // NOTE: uses golang.org/x/sys/unix. Windows will need build-tagged alternative.
- [ ] Unit tests: mock figaro interface, verify registry ops, pid binding, pid death detection,
  duplicate bind rejection, unbind-on-rebind behavior
- **Validate**: unit tests pass; can start supervisor in a test, create/list/kill figaros
- **Fixture**: `testdata/` with registry state snapshots

### Step 5: CLI package
- [ ] Create `internal/cli/cli.go` — parse args, connect supervisor, resolve/create/bind, connect figaro, translate stdio
- [ ] POSIX `--` prompt parsing, subcommand dispatch (new, context, list, kill, models, login)
- [ ] `new` subcommand: Resolve(ppid) → Unbind(ppid) → Create() → Bind(ppid, new_id) → Prompt
- [ ] Auto-start angelus (fork with `--angelus`, wait for socket)
- [ ] Unit tests: mock both sockets, verify CLI flow for each subcommand
- **Validate**: unit tests pass; full manual flow works end-to-end
- **Fixture**: `testdata/` with expected CLI output for various scenarios

### Step 6: Wire into cmd/figaro/main.go
- [ ] `--angelus` flag dispatches to supervisor mode
- [ ] Default mode dispatches to CLI
- [ ] Integrate otel init/shutdown
- [ ] Integration smoke test
- **Validate**: full manual flow:
  1. `figaro -- hello` — auto-starts angelus, creates figaro, streams response
  2. `figaro -- followup` — resumes same figaro (same shell ppid)
  3. `figaro list` — shows the figaro
  4. `figaro context` — shows chat history
  5. `figaro new -- fresh start` — unbinds old, creates new figaro
  6. `figaro list` — shows both figaros (old is idle, new is active)
  7. `figaro kill <id>` — kills one
  8. Check trace file for spans

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
