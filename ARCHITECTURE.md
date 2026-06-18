# Figaro — Architecture

Single Go binary, three roles selected by invocation: CLI, supervisor (*angelus*), agent (*figaro*). All IPC is JSON-RPC 2.0 over Unix sockets.

```
┌────────┐  unix   ┌──────────┐  spawns  ┌────────────┐
│ q / l  │◄───────►│ angelus  │─────────►│  figaros   │
│  CLI   │ angelus │ registry │          │  (actors)  │
└────────┘  .sock  └──────────┘          └────────────┘
ephemeral          1 per user            N per user
```

## Tiers

| Tier | Lifetime | Socket |
|------|----------|--------|
| **CLI** — stateless translator, stdio ↔ JSON-RPC | one prompt | dials angelus + figaro |
| **Angelus** — supervisor: registry, PID monitor, lifecycle | per user | `$XDG_RUNTIME_DIR/figaro/angelus.sock` |
| **Figaro** — actor: one inbox, one drain loop | per conversation | `$XDG_RUNTIME_DIR/figaro/figaros/<id>.sock` |

## Packages

```
cmd/figaro/                multi-call entry: figaro / q / l + --angelus daemon
internal/angelus/          supervisor, PID monitor, JSON-RPC handlers
internal/auth/             OAuth + PKCE, hush-encrypted token storage
internal/chalkboard/       per-aria structured state + system-reminder rendering
internal/config/           TOML config loader
internal/credo/            personality body + skill catalog
internal/figaro/           actor: agent loop, inbox, translator orchestration
internal/message/          provider-agnostic IR (Message, Content, Patch)
internal/otel/             OpenTelemetry init + span helpers
internal/provider/         Provider interface + anthropic implementation
internal/rpc/              shared notification types + method constants
internal/store/            generic Stream[T]; FileBackend for arias
internal/tokens/           context-window accounting
internal/tool/             tool interface + bash/read/write/edit
internal/transport/        unix/tcp endpoint abstraction
```

## Streams

Every aria is a multi-column log. Two columns today:

```
arias/{id}/
├── aria.jsonl                        figaro IR — Stream[message.Message], canonical
├── chalkboard.json                   per-aria snapshot, atomic rewrite at turn end
├── meta.json                         AriaMeta — what `figaro list` reads
└── translations/
    └── anthropic.jsonl               translator cache — Stream[[]json.RawMessage]
```

**Figaro IR** is the source of truth. **Translator stream** caches per-provider wire bytes, FK'd back via `Entry.FigaroLT`. Translations are derivable from the IR; on `Provider.Fingerprint()` mismatch the agent clears the stream and lets `synchronize` repopulate.

## Actor loop

One inbox per agent (selfish/patient mailbox), one drain goroutine. Every event — user prompt, live SSE delta, tool result, interrupt — enters through `Recv` and is processed in order.

```
Recv → synchronize → dispatch
         │
         ├── catchUpFigaroDelta   live deltas → UI events
         ├── condenseLive          on SendComplete: Assemble + Decode + figStream.Append + translator.Condense
         └── catchUpTranslator    encode any new figStream entries into the translator cache
```

`synchronize` is the sole owner of bidirectional translator orchestration. `startLLMStream` just projects `translator.Durable()` to `[][]json.RawMessage` and hands it to `Provider.Send`; the provider assembles the request body internally.

## Provider interface

```go
type Provider interface {
    Name() string
    Fingerprint() string
    Models(ctx) ([]ModelInfo, error)
    SetModel(string)

    Encode(msg Message, prevSnapshot Snapshot) ([]json.RawMessage, error)
    Decode(payload []json.RawMessage) ([]Message, error)
    Send(ctx, SendInput, Bus) error
    Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error)
}
```

- `Encode` is per-message; cached in the translator stream.
- `Decode` is uniform — handles both durable per-message bytes and live tail delta payloads (returns partial Messages for deltas).
- `Send` takes `SendInput {PerMessage, Snapshot, Tools, MaxTokens}`, assembles the request internally, ships, and pushes raw native events to the bus.
- `Assemble` accumulates the live tail into one assembled assistant nativeMessage at end-of-turn.

## Cache prefix

The translator stream stores **input-ready** bytes. Assistant entries get re-encoded via `Encode` before they're stored, so `stop_reason` / `model` / `usage` (which the API rejects on input) live only on the figaro IR Message. Splice is verbatim — no per-request stripping.

The per-message bytes are written exactly once and reused on every subsequent turn. The prefix is **byte-identical** across requests within an aria's lifetime — Anthropic's `cache_control` markers actually hit.

## Credo

Providers read `system.credo` from the chalkboard and inject it as the API's system prompt. The credo is a literal string (or a `ContentEnvelope` `{content, frontmatter, filePath}` when sourced via the outfitter's `fileName=` loader). No derivation, no templating — what you put in `system.credo` is what the model sees. To pick up edits to the on-disk credo file, re-apply the loadout: `figaro loadout <name>`.

## Chalkboard

Structured per-aria state. Patches ride on the user-role tic in `aria.jsonl`; the current snapshot is cached at `chalkboard.json`. Each key has a body template (`internal/chalkboard/templates/`); the provider renders patches as `<system-reminder name="…">…</system-reminder>` text blocks on the user message that carries them.

The `system.*` namespace is harness-reserved. Clients write under any other key.

## JSON-RPC surface

**Angelus socket:** `figaro.create`, `figaro.attach`, `figaro.kill`, `figaro.list`, `aria.read`, `pid.bind`, `pid.resolve`, `pid.unbind`, `angelus.status`, `angelus.save_bindings`.

**Figaro socket (requests):** `figaro.qua` (prompt; optional `chalkboard`, `delta_mode`; returns the user tic's index), `figaro.read` (windowed log read: `from`/`last`/`limit`/`follow`/`delta_mode`), `figaro.interrupt`, `figaro.context`, `figaro.set`, `figaro.loadout`, `figaro.chalkboard`.

### Streaming: the log.* tail model

What travels on the socket is the serialized Figaro IR. A conversation is an append-only log of `message.Message` at gapless 1-based indices (`LT`); a client consumes catch-up history and live streaming through one shape — a read that may stay open. Notifications:

- `log.entry` `{index, message}` — a sealed, immutable message (the bare IR). Render once.
- `log.open` `{index, version, open, message}` — the current full state of the open (unsealed) tail (full mode).
- `log.patch` `{index, version, from, ops}` — block-addressed deltas against the open tail (delta mode; opt-in per subscription).
- `log.abort` `{index, reason}` — the open tail was burned before sealing (interrupt/fault); its index is reused by the next message.
- `turn.done` `{reason}` — the turn went idle.

A turn streams its assistant reply as an open message that seals into a `log.entry`; if it called tools, their execution streams as an open `tool_result` message that seals in turn. The seal *is* the arrival of the closed `log.entry` at that index. Provider `Bus` calls are folded into the open tail by the drain loop's `openBuilder`; providers are unchanged.

## CLI

| Command | Description |
|---------|-------------|
| `q <prompt>` / `figaro -- <prompt>` | Prompt (auto-resolves or creates) |
| `figaro send [--id <id>] [-e] [-r] [-x] -- <prompt>` | Explicit prompt; `-e` ephemeral, `-r` raw, `-x` bash exec (orthogonal flags) |
| `figaro new -- <prompt>` | New conversation with chosen loadout/patch |
| `figaro list` | List all arias (live + dormant) |
| `figaro kill <id>` | Kill + delete aria |
| `figaro attend <id>` | Rebind this shell |
| `figaro context` | Dump message history as JSON |
| `figaro models` | List provider models |
| `figaro login <provider>` | OAuth PKCE |
| `figaro rest` | Shut down the angelus |

## Layout

```
~/.config/figaro/                 config, credo.md, skills/, providers/
$XDG_RUNTIME_DIR/figaro/          angelus.sock, angelus.pid, figaros/<id>.sock
~/.local/state/figaro/
├── angelus.log                   supervisor log
├── traces.jsonl                  OTel exporter
├── figaros/<id>.jsonl            per-agent event log
└── arias/<id>/...                see "Streams" above
```

## Roadmap

- More providers (the interface is small; the wiring isn't there)
- Browser / chat frontends (just JSON-RPC clients)
- WebSocket transport (unix/tcp already abstracted)
- Agent pooling
- Tool-execution sandboxing
- Context compaction
- Child-process agents (currently goroutines under the angelus)
