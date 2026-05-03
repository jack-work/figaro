# Figaro

*Largo al factotum della città.*

A coding agent that lives behind a JSON-RPC socket. One Go binary plays the CLI, the supervisor (the *angelus*), and the agent itself — pick your role with a flag.

```
q explain this function
q now refactor it, per favore
figaro list
```

`q` is *"Figaro, qua!"* — the call that summons the barber.

## What it is today

- A bidirectional translator between Anthropic's wire format and a provider-agnostic IR — both directions, byte-stable in the cache prefix. Re-encoding a turn's prefix yields the **same bytes**, so Anthropic's `cache_control` actually hits.
- A JSON-RPC protocol over Unix sockets. Three sockets, three roles. The wire is the API; build any frontend.
- A CLI. Not a TUI. Streaming markdown via [largo](https://github.com/jack-work/largo) renders incrementally to your terminal.
- An actor-model agent. One inbox, one drain loop, no races. Survives panics with credo intact.
- Persistent conversations (*arias*) on disk. `q` from any shell finds your aria; `figaro list` shows them all.
- OAuth via [hush](https://github.com/jack-work/hush). Tokens encrypted at rest.

## What it's becoming

A lingua franca of assistantry. The provider interface is small (`Encode`, `Decode`, `Send`, `Assemble`) and provider-agnostic; the IR sits between. New backends slot in. Richer client IRs follow.

## Setup

```bash
nix profile install github:jack-work/figaro      # or go install
figaro login anthropic
q buongiorno
```

Config lives at `~/.config/figaro/`. Personality in `credo.md`, skills in `skills/`.

## The cache works

The translator stream caches per-message wire bytes once (`Encode`) and splices them verbatim into the next request. No re-encoding per turn. The prefix is byte-identical across requests within an aria's lifetime — that's the invariant Anthropic's prompt cache needs.

---

*Tutti mi chiedono, tutti mi vogliono.* MIT.
