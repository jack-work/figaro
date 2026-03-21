# Figaro

*"Largo al factotum della città!"*

A coding agent, built in Go. Work in progress.

## What

Figaro is a CLI coding agent with a supervisor architecture. You talk to it from your terminal. It remembers your conversation, streams responses, and — eventually — writes and runs code on your behalf.

```
figaro -- explain this function
figaro -- refactor it to use channels
figaro list
figaro kill abc123
```

## Why

We wanted something simpler. The existing tools have their merits, but also their baggage:

- **No Node/Bun runtime.** Single static Go binary.
- **No TUI dependency.** Plain stdout streaming. Fancy frontends come later, as separate clients.
- **Built-in secret handling.** OAuth tokens encrypted at rest via [hush](https://github.com/jack-work/hush). No plaintext API keys on disk.
- **Supervisor architecture.** Agents outlive your terminal. Come back tomorrow, pick up where you left off.
- **JSON-RPC everywhere.** Every component speaks JSON-RPC 2.0 over unix sockets. Build a frontend in any language.

## Architecture

```
CLI (ephemeral) → Angelus (supervisor) → Figaro agents (goroutines)
```

- **CLI**: Stateless. Translates stdio ↔ JSON-RPC. Exits when done.
- **Angelus**: Long-lived supervisor. Registry, PID tracking, health monitoring. Auto-starts on first use.
- **Figaro**: An agent. Owns a conversation, a model, a prompt queue. Streams responses via server-push notifications.

## Status

Working today:
- OAuth login via Anthropic Max subscription
- Streaming responses (word-by-word)
- Conversation continuity per terminal session
- `list`, `kill`, `context`, `models` subcommands
- OpenTelemetry tracing
- Configurable personality via credo.md template
- Skills loaded from markdown files with frontmatter
- Panic recovery (agents restart without taking down the supervisor)

## Future

- **Arias.** Persistent conversation contexts, decoupled from agent instances. In-memory → JSONL WAL → database.
- **Tool execution.** Bash, file read/write/edit. The agent becomes a coder.
- **Streaming frontend.** Rich CLI rendering — thinking indicators, tool call display, syntax highlighting.
- **Browser & chat frontends.** Any JSON-RPC client can connect to the figaro socket.
- **Multi-node scaling.** Figaros on remote machines. Transport abstraction already supports TCP/websocket endpoints.
- **Figaro pooling.** Reusable agent processes assigned to arias on demand.
- **Network isolation.** Single and multi-system security boundaries for tool execution.

## Setup

```bash
# Install
go install github.com/jack-work/figaro/cmd/figaro@latest

# Login (Anthropic Max subscription)
figaro login anthropic

# Talk
figaro -- hello, Figaro
```

Configuration lives in `~/.config/figaro/`. See `credo.md` for personality, `providers/anthropic/config.toml` for model settings.

## License

*"Tutti mi chiedono, tutti mi vogliono."*

MIT.
