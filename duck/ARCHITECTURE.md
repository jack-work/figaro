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
internal/store/            generic Log[T]; XWAL backend, turn journal, fork tree
internal/tokens/           context-window accounting
internal/tool/             tool interface + bash/read/write/edit
internal/transport/        unix/tcp endpoint abstraction
```

## Streams

Every aria is one XWAL trunk with related channels:

```
arias/
├── ir/                               canonical message.Message timeline
├── chalkboard/                       reducible state patches
├── translations-v2/
│   ├── anthropic/                    opaque provider request objects
│   ├── copilot-messages/
│   └── copilot-responses/
└── _meta/{id}.json                   AriaMeta for metadata-only listing
```

**Figaro IR** is the source of truth. Translator channels cache per-provider
wire bytes, FK'd by main LT. `translations-v2` channels are opaque so forking
and reopen preserve payload bytes. The first open for a provider snapshots
that provider's legacy bytes across each trunk before installing the new
channel, then migrates each lineage independently with its original metadata
and fingerprint. An incomplete or unscoped migration is an explicit error;
Figaro never silently regenerates potentially signed or encrypted legacy
items. Translations remain fingerprint-invalidated.

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

## Durability and the ack contract

The store is memory-first: appends land in RAM and return; a background
flusher persists lineage-coherent cuts every FlushInterval (1s), bounded
in bytes by MaxUnflushedBytes. A client ack (including the rendered echo
of a user prompt) therefore precedes durability by up to the flush lag;
user tics call Kick to expedite the next flush but do not wait for it.
Cooperative shutdown (drain + Close) loses nothing; a hard crash loses at
most the flush lag plus the in-flight partial stream.

## In-progress turn durability

In-progress turn state is memory-only (`turnState` in turn_seal.go): the IR
already gets per-message appends, interrupt/SIGTERM/panic seal the in-memory
partial as an interrupted turn, and open-time tail repair appends interrupted
tool results for unresolved tool calls. Checkpoint cadence no longer exists
per second for streamed prose/tool output; periodic checkpoints sync, and
interrupt/error paths force a final sync. The payload records turn
identity/generation, target next IR LT, assistant/tools phase, the partial
assistant with only args-ready tool calls, ordered call identities, bounded
tool-output tails/status, and timestamp.

Recovery reads only the newest journal record at `IR tail + 1`. A canonical IR
append retires its checkpoint by advancing the tail; stale records need no
rewrite or scan. Assistant-phase recovery appends the partial assistant with
`stop_reason=aborted`, then interrupted user-role tool results for any ready
calls. Tools-phase recovery appends one ordered interrupted tool-result
message. Terminal `ok` and `error` tool results retain their persisted output;
only pending/running calls receive synthetic interruption errors. A tools
checkpoint is reserved at the following LT immediately before the assistant
append; if recovery still sees the preceding tail, it seals that same payload
as assistant-phase work instead. Recovery runs before rendered history
construction and after actor panic, never resumes a remote stream, never calls
the provider, and is idempotent because the recovery append advances the IR
tail. Successful turn completion or recovery clears the branch-local journal
head, bounding retained checkpoint history without clearing forked lineages.
After panic or seal failure, the live aria server is rebuilt from canonical IR
so it neither commits a speculative-only unit nor hides a durable assistant.

Manual sync intentionally permits live frames after the last periodic
checkpoint to rewind by less than one second after process death. Journal
append/sync failure suppresses the corresponding frame and ends the turn as an
explicit error; it never emits a success-shaped fallback.

`PushFigaro` carries the canonical assistant candidate plus its exact
input-ready provider cache payload, namespace, and fingerprint. The actor
appends canonical IR at the predicted LT, appends and syncs the opaque
translation entry on the same continuation best-effort, then acknowledges.
Providers never append the assistant cache entry themselves.
Direct Anthropic supplies the sanitized accumulated native response with signed
and redacted thinking intact, the SDK supplies `Message.ToParam()`, and
Responses supplies the original encrypted output items. Process death between
any of these writes is reconciled idempotently from the journal without
recalling the provider. Forks cannot enter the commit window, and the journal
is retired only after both IR and cache are durable.

## Provider interface

```go
type Provider interface {
    Name() string
    Fingerprint() string
    Models(ctx) ([]ModelInfo, error)
    SetModel(string)

    Send(ctx, SendInput, Bus) error
}
```

- Providers own their per-message IR projection and translation cache.
- `Send` takes the canonical `FigLog`, current chalkboard snapshot, tools,
  and token limit; it rebuilds its provider-native request and emits
  provider-native streaming events through the bus.
- The Copilot provider routes catalog models by their advertised endpoint:
  Anthropic Messages-compatible models keep the Messages transport and
  Responses-compatible models use a direct WebSocket Responses transport.
  Both paths replay from Figaro's translator cache rather than depending on
  external session state.
- Copilot Responses reads `system.model`, `system.max_tokens`,
  `system.context_tier`, `system.max_context_tokens`,
  `system.reasoning_context`, `system.reasoning_summary`, `system.thinking_effort`,
  `system.verbosity`, `system.temperature`, `system.top_p`, and
  `system.parallel_tool_calls` from the chalkboard for every turn. The
  context tier selects a catalog-backed replay budget; `reasoning_context`
  maps directly to the Responses API. A Responses model change creates a
  new translation-cache fingerprint so opaque reasoning never crosses models.

## Cache prefix

The translator stream stores **input-ready** bytes. Production assistant
entries are captured from each provider's exact native response and sanitized
only for response-only fields (`stop_reason`, `model`, `usage`). The
provider-agnostic IR encoder remains a cache-miss fallback and cannot recreate
signed/redacted thinking. Splice is verbatim — no per-request stripping.

The per-message bytes are written exactly once and reused on every subsequent turn. The prefix is **byte-identical** across requests within an aria's lifetime — Anthropic's `cache_control` markers actually hit.

## Credo

Providers read `system.credo` from the chalkboard and inject it as the API's system prompt. The credo is a literal string (or a `ContentEnvelope` `{content, frontmatter, filePath}` when sourced via the outfitter's `fileName=` loader). No derivation, no templating — what you put in `system.credo` is what the model sees. To pick up edits to the on-disk credo file, re-apply the loadout: `figaro loadout <name>`.

## Chalkboard

Structured per-aria state. Patches ride on the user-role tic in `aria.jsonl`; the current snapshot is cached at `chalkboard.json`. Each key has a body template (`internal/chalkboard/templates/`); the provider renders patches as `<system-reminder name="…">…</system-reminder>` text blocks on the user message that carries them.

The `system.*` namespace is harness-reserved. Clients write under any other key.

## JSON-RPC surface

**Angelus socket:** `figaro.create`, `figaro.attach`, `figaro.kill`, `figaro.list`, `aria.read`, `pid.bind`, `pid.resolve`, `pid.unbind`, `angelus.status`, `angelus.save_bindings`.

**Figaro socket (requests):** `figaro.qua` (prompt; optional `chalkboard`), `figaro.interrupt`, `figaro.context`, `figaro.set`, `figaro.loadout`, `figaro.chalkboard`.

### Streaming: the live-render node model

What travels on the socket is a **typed node list**, not the IR. Each conversational message (the user's prompt, then the agent's turn) is one unit: an append-only, positionally stable list of nodes — `prose` (a markdown span) or `tool` (`{name, args, status, output}`). The two long streamed fields, prose `markdown` and tool `output`, mutate by single-region splices; nodes are appended, never reordered. The `message.Content` IR stays canonical on disk and is the provider's input; the producer *translates* a turn into nodes (`internal/compose`), each consumer renders nodes its own way — prose as markdown, tools as native widgets (`internal/cli` tool widget / `web/conversation.html`) — and the node model + diff/apply (`internal/livedoc`) is shared by both ends. Notifications:

- `log.snapshot` `{role, nodes}` — establish the current live unit's full node list.
- `node.open` `{index, node}` — append a node.
- `node.patch` `{index, field, at, del, ins}` — a rune-aligned, single-region splice on a node's `markdown` or `output`.
- `node.set` `{index, status}` — update a tool node's status (`running`→`ok`/`error`).
- `log.commit` — freeze the live unit; the next snapshot/op is a new one.
- `turn.done` `{reason}` — the turn went idle (reason carries an error string on failure).

A turn emits the user prompt as one committed unit, then the agent reply as a live unit: `snapshot` (empty) → `open`/`patch`/`set` ops as the node list grows (the drain loop recomposes from the IR and `livedoc.DiffNodes`) → `commit`. Each tool is an independently addressable node, so parallel tools stream side by side without contending for one document; a running tool animates its spinner on the **consumer** (zero wire traffic), and output is clamped to its last N lines. There is no unit index — the server copy is authoritative and a faulted client reconnects and re-snapshots (`figaro.read` returns committed units + the live unit as node lists). Provider `Bus` calls are unchanged.

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
