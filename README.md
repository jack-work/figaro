# 🎭♪ Figaro

*A coding agent. Pronto prontissimo.*

> *"Largo al factotum della città!" — Il barbiere di Siviglia, G. Rossini (1816)*

---

## About

Figaro is named for the resourceful barber-factotum of Beaumarchais' plays and Rossini's opera — the clever servant who is everywhere he's needed, solves everyone's problems, and does it all with style. The agent embodies this spirit: brisk, witty, and relentlessly practical. Its system prompt (the *credo*) gives it Figaro's voice; its architecture gives it Figaro's reach.

---

## *Ecco qua* — What Is This?

A CLI coding agent with a supervisor architecture. You talk, Figaro listens, schemes, and delivers. Conversations outlive your terminal. Come back tomorrow — *il factotum* remembers.

```
figaro -- explain this function to me
figaro -- now refactor it, per favore
figaro list
figaro kill abc123
```

Every component speaks JSON-RPC 2.0 over unix sockets. Build a frontend in whatever language suits your fancy — Figaro doesn't care who's asking, only that the question is interesting.

## *¿Por qué?* — Why Another One?

The existing tools have merit, *naturalmente*, but also baggage:

- **No runtime bloat.** Single static binary. No Node, no Bun, no dependency tree deeper than a Sevillian well. Written in Go — *veloce e leggero*.
- **No TUI chains.** Plain stdout streaming. Rich frontends come later, as separate clients, not load-bearing walls.
- **Secrets handled.** OAuth tokens encrypted at rest via [hush](https://github.com/jack-work/hush). No plaintext keys lounging on disk like an unguarded letter on Rosina's balcony.
- **Supervisor architecture.** Agents outlive terminals. The angelus watches over its figaros — restarting the fallen, tracking the living.
- **Protocol-first.** JSON-RPC everywhere. The socket is the API. Any language, any frontend, any machine.

## Architecture

```
CLI (ephemeral) → Angelus (supervisor) → Figaro agents
```

- **CLI**: Stateless translator. Stdio ↔ JSON-RPC. Arrives, delivers, departs — like a well-timed entrance.
- **Angelus**: The quiet guardian. Registry, PID tracking, health monitoring. Auto-starts on first invocation.
- **Figaro**: *Il factotum.* Owns a conversation, a model, a prompt queue. Each on its own socket, each with its own personality defined by a *credo* — a templated soul file that shapes behavior without pretending to grant consciousness.

## Status — *Lavori in corso*

Working: OAuth login, streaming responses, conversation continuity, `list`/`kill`/`context`/`models`, OpenTelemetry tracing, configurable personality via `credo.md`, skills from markdown with frontmatter, panic recovery.

## *Il futuro*

- **Arias.** Persistent conversation contexts — in-memory, then WAL, then database.
- **Tool execution.** Bash, file I/O. *Il barbiere* picks up his razor.
- **Frontends.** Rich CLI, browser, chat applications — all just JSON-RPC clients.
- **Scaling.** Multi-node figaros. Transport abstraction already supports TCP and websocket endpoints.
- **Pooling.** Reusable agent processes assigned to arias on demand.
- **Isolation.** Network boundaries for tool execution. *Un factotum* who reads his master's letters is no longer trusted with them.

## Setup

```bash
go install github.com/jack-work/figaro/cmd/figaro@latest
figaro login anthropic
figaro -- buongiorno, Figaro
```

Configuration: `~/.config/figaro/`. Personality: `credo.md`. Skills: `skills/`. Provider settings: `providers/anthropic/config.toml`.

---

*Tutti mi chiedono, tutti mi vogliono.* MIT.
