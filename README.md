# 🎭♪ Figaro

*A coding agent. Pronto prontissimo.*

> *"Largo al factotum della città!" — Il barbiere di Siviglia, G. Rossini (1816)*

---

## *Ecco qua* — What Is This?

A CLI coding agent with a supervisor architecture. You talk, Figaro listens, schemes, and delivers. Conversations outlive your terminal. Come back tomorrow — *il factotum* remembers.

```
q explain this function to me
q now refactor it, per favore
figaro list
figaro kill abc123
```

`q` is short for *"Figaro, qua!"* — the call that summons the barber. Every component speaks JSON-RPC 2.0 over unix sockets. Build a frontend in whatever language suits your fancy.

## *¿Por qué?* — Why Another One?

- **No runtime bloat.** Single static binary. No Node, no Bun, no dependency well. Written in Go — *veloce e leggero*.
- **No TUI chains.** Incremental markdown rendering via [largo](https://github.com/jack-work/largo) — rich text streams to your terminal as it arrives, not after. Rich frontends come later, as separate clients, not load-bearing walls.
- **Secrets handled.** OAuth tokens encrypted at rest via [hush](https://github.com/jack-work/hush). No plaintext keys lounging on disk like an unguarded letter on Rosina's balcony.
- **Supervisor architecture.** The angelus watches over its figaros — restarting the fallen, tracking the living. Agents outlive terminals.
- **The actor on stage.** Every figaro is an *attore* — a single event loop, one mailbox, one voice. LLM responses, tool output, errors — all enter through the same door, are processed in order, and exit through the same curtain. No race conditions, no tangled threads. *Come in un'opera ben diretta* — like a well-conducted opera.
- **Protocol-first.** JSON-RPC everywhere. The socket is the API. Any language, any frontend, any machine.

## Architecture

```
CLI (ephemeral) → Angelus (supervisor) → Figaro agents
```

- **CLI**: Stateless translator. Stdio ↔ JSON-RPC. Arrives, delivers, departs.
- **Angelus**: The quiet guardian. Registry, PID tracking, health monitoring. Auto-starts on first invocation.
- **Figaro**: *Il factotum.* An event-driven actor — one inbox, one loop. Owns a conversation, a model, tools. Each on its own socket, each with a *credo* that shapes its voice.

## Status — *Lavori in corso*

Working: incremental markdown rendering, tool execution (bash, read, write, edit), conversation continuity, OAuth login, `list`/`kill`/`context`/`models`/`attend`/`new`/`rest`, OpenTelemetry tracing, configurable personality via `credo.md`, skills from markdown, panic recovery with automatic restart.

## Provider — *Chi suona?*

Anthropic only, for now. Authentication via OAuth requires a **Max subscription**. Tokens are encrypted at rest via [hush](https://github.com/jack-work/hush). API key authentication (`ANTHROPIC_API_KEY` or config) is also supported.

```bash
figaro login anthropic
```

The provider interface is designed for multiple backends — adding one means implementing a single interface. The agent loop, store, and transport are provider-agnostic.

## *Il futuro*

- **Arias.** Persistent conversation contexts. The store interface is in place (in-memory today) — WAL-backed persistence, then a proper database. Conversations that survive restarts.
- **Providers.** More backends beyond Anthropic. The `Provider` interface is ready; the wiring is not.
- **Frontends.** Browser, chat applications — all just JSON-RPC clients over the existing socket protocol.
- **Scaling.** Multi-node figaros. The transport abstraction already implements unix and TCP listeners; websocket is next.
- **Pooling.** Reusable agent processes assigned to arias on demand.
- **Isolation.** Network boundaries for tool execution. *Un factotum* who reads his master's letters is no longer trusted with them.

## Setup

```bash
go install github.com/jack-work/figaro/cmd/figaro@latest
ln -s $(which figaro) ~/go/bin/q    # the factotum's call
figaro login anthropic
q buongiorno, Figaro
```

Configuration: `~/.config/figaro/`. Personality: `credo.md`. Skills: `skills/`. Provider settings: `providers/anthropic/config.toml`.

---

*Tutti mi chiedono, tutti mi vogliono.* MIT.
