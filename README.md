# Figaro

*Largo al factotum della città.*

An LLM harness written in Go. One binary serves as a CLI, a supervisor daemon, and the agent runtime. Everything talks JSON-RPC over unix sockets — the wire is the API.

Figaro manages persistent conversations called *arias*. Each aria is an append-only message log in a provider-agnostic IR, with per-provider translation caches that preserve byte-stability for prompt caching. Arias bind to your shell by PID, survive daemon restarts, and can be addressed by name from any terminal.

```
figaro -- explain this function
figaro -- now refactor it, per favore
figaro list
```

The default verb is `figaro.qua` — the call that summons the barber.

## Shape of the thing

- Provider-agnostic message IR. Anthropic backend today; the interface is small.
- Chalkboard: per-aria structured state that travels as patches on the message stream and surfaces to the model as system reminders.
- Tools: bash, read, write, edit. Parallel dispatch when the model emits multiple calls.
- Loadout system: TOML configs with inheritance, file/dir inlining, templated system prompts.
- Durable derivations: background workers that materialize per-aria views (usage stats, translator metadata) on each turn.
- OAuth via [hush](https://github.com/jack-work/hush). Tokens encrypted at rest.

## Status

In active development. The core loop works and I use it daily. The goal is a general-purpose harness that can be scripted, composed, and extended to cover as much of my working surface as possible — not a product, but a tool that grows with the work.

## Setup

```bash
nix profile install github:jack-work/figaro      # or go install
figaro login anthropic
figaro -- buongiorno
```

Config lives at `~/.config/figaro/`.

---

*Tutti mi chiedono, tutti mi vogliono.* MIT.
