# Figaro, Architecture

Single Go binary, three roles selected by invocation: CLI, supervisor (*angelus*), agent (*figaro*). All IPC is JSON-RPC 2.0 through one door per daemon: a Unix socket, or any listener `Angelus.Serve` is handed.

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
| **CLI**: stateless translator, stdio ↔ JSON-RPC | one prompt | dials the door |
| **Angelus**: supervisor: registry, PID monitor, lifecycle | per user | `$XDG_RUNTIME_DIR/figaro/angelus.sock`, the door |
| **Figaro**, actor: one inbox, one drain loop | per conversation | none: addressed through the door by `figaro_id` |

## Packages

```
cmd/figaro/                multi-call entry: figaro / q / l + --angelus daemon
sdk/                       Go client: angelus + aria connections
skills.go                  embeds skills/figaro into the binary

api/aria/                  wire types of the paginated read API (Page, TurnPart, deltas)
api/decide/                the ?? option grammar, -C choices, @refs; renders a decision
api/form/                  per-aria structured state + system-reminder templates
api/livedoc/               live-render protocol core: one markdown blob, splice deltas
api/message/               provider-agnostic IR (Message, Content, Patch)
api/quote/                 quoted-coordinate grammar at the head of a prompt
api/rpc/                   JSON-RPC types + method constants; methods.go is the whole surface
api/sandbox/               the tool-runtime contract; its own module, so a sandbox agent need not import figaro
api/transport/             unix/tcp endpoint abstraction

internal/actor/            single-writer runtime: FIFO queue, one drain goroutine
internal/angelus/          supervisor, PID monitor, JSON-RPC handlers
internal/auth/             provider credentials; OAuth + PKCE, hush-encrypted tokens
internal/authz/            authn/authz seam for the RPC surface
internal/peer/             node door, peer links, directory of remote owners
internal/cli/              the CLI: commands, transcript pager, line editor, keymap; figtree/ tree tables
internal/cmdkit/           CLI command routing
internal/compose/          IR turn -> live-render unit of typed nodes
internal/config/           TOML config loader
internal/decide/           figaro.decide: window, focus, @refs, budget; System One and OpenAI wires, through a provider
internal/figaro/           actor: agent loop, inbox, translator orchestration; wire/ trunk capability
internal/formdelta/        per-record form state a transcript reader needs
internal/livelog/          aria/ page server + field deltas; render/ terminal differential renderer
internal/logring/          bounded in-memory slog tail
internal/mark/             timing sink for transcript benchmarks
internal/otel/             OpenTelemetry init + span helpers
internal/outfit/           assembles an aria's form from on-disk config; unpacks bundled skills
internal/partialjson/      string field from a truncated JSON prefix
internal/provider/         Provider interface; anthropic (direct HTTP+SSE), anthropicmodels,
                           copilot, meta, openaichat, responses, routed
internal/render/           markdown -> ANSI rows (glamour)
internal/store/            generic Log[T]; XWAL backend, forms, libretto, fork tree;
                           disk/ log/ segment/ tree/ xwal/ layers
internal/tape/             record/replay of the aria wire
internal/term/             terminal color, width, TTY detection
internal/tokens/           context-window accounting
internal/tool/             tool interface + bash/read/write/edit, image fitting
internal/toolout/          bounds and coalesces streamed tool text
internal/topo/             aria hierarchy for rendering and deletion
internal/tui/              bubbletea first-run components
internal/turns/            fig IR turn arithmetic
internal/uiir/             UI IR projection
internal/update/           release discovery and upgrade
internal/wirelog/          RoundTripper tracing + optional raw dumps
```

## Streams

Every aria is one XWAL trunk with related channels:

```
arias/
├── ir/                               canonical message.Message timeline
├── form/                       reducible state patches
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

One inbox per agent (selfish/patient mailbox), one drain goroutine. Every event: user prompt, live SSE delta, tool result, interrupt: enters through `Recv` and is processed in order.

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

In-progress turn state is memory-only (`turnState` in `turn_repair.go`): the IR
gets per-message appends (assistant at seal, tool-result tics at completion);
interrupt/SIGTERM/panic seal the in-memory partial as an interrupted turn, and
open-time `repairInterruptedTail` (`repair.go`) appends honest interrupted
tool results when the IR tail has unresolved tool calls. There is no turn
journal and no checkpoint record. `journal.go` is the agent's one append door
to the IR (append, then announce), not a recovery log. Recovery never resumes
a remote stream and never calls the provider. After panic or seal failure, the
live aria server is rebuilt from canonical IR so it neither commits a
speculative-only unit nor hides a durable assistant.

`PushFigaro` carries the canonical assistant candidate plus its exact
input-ready provider cache payload, namespace, and fingerprint. The actor
appends canonical IR at the predicted LT, appends and syncs the opaque
translation entry on the same continuation best-effort (a cache failure
degrades to re-encoding; it never blocks the turn), then acknowledges.
Providers never append the assistant cache entry themselves.
Direct Anthropic supplies the sanitized accumulated native response with signed
and redacted thinking intact, the SDK supplies `Message.ToParam()`, and
Responses supplies the original encrypted output items. Forks cannot enter the
commit window.

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
- `Send` takes the canonical `FigLog`, current form snapshot, tools,
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
  `system.parallel_tool_calls` from the form for every turn. The
  context tier selects a catalog-backed replay budget; `reasoning_context`
  maps directly to the Responses API. A Responses model change creates a
  new translation-cache fingerprint so opaque reasoning never crosses models.

## Cache prefix

The translator stream stores **input-ready** bytes. Production assistant
entries are captured from each provider's exact native response and sanitized
only for response-only fields (`stop_reason`, `model`, `usage`). The
provider-agnostic IR encoder remains a cache-miss fallback and cannot recreate
signed/redacted thinking. Splice is verbatim: no per-request stripping.

The per-message bytes are written exactly once and reused on every subsequent turn. The prefix is **byte-identical** across requests within an aria's lifetime, Anthropic's `cache_control` markers actually hit.

## Credo

Providers read `system.credo` from the form and inject it as the API's system prompt. The credo is a literal string (or a `ContentEnvelope` `{content, frontmatter, filePath}` when sourced via the outfitter's `fileName=` loader). No derivation, no templating: what you put in `system.credo` is what the model sees. To pick up edits to the on-disk credo file, re-apply the outfit: `figaro outfit <name>`.

## Form

Structured per-aria state. Patches ride on the user-role tic in `aria.jsonl`; the current snapshot is cached at `form.json`. Each key has a body template (`api/form/templates/`); the provider renders patches as `<system-reminder name="…">…</system-reminder>` text blocks on the user message that carries them.

The `system.*` namespace is harness-reserved. Clients write under any other key.

## JSON-RPC surface

All IPC is JSON-RPC 2.0 framed as one JSON object per line (NDJSON).

One door serves every method, and every request passes `authz.Guard` once.

**Daemon methods:** `figaro.create`, `form.create`, `form.bind`,
`figaro.fork`, `figaro.promote`, `figaro.import`, `figaro.normalize`,
`figaro.gc`, `figaro.kill`, `figaro.list`, `figaro.attach`, `pid.bind`,
`pid.resolve`, `pid.unbind`, `angelus.status`, `angelus.mem.collect`,
`angelus.outfits`, `outfit.reload`, `angelus.configure`,
`angelus.save_bindings`, `angelus.provider_ledger`, `figaro.ir`,
`figaro.lineage`. `figaro.attach` proves an aria exists without waking it.

**Aria methods**, each naming its aria in `params.figaro_id`
(`rpc.AriaKey`): `figaro.qua` (prompt; optional `form`), `figaro.interrupt`,
`figaro.set`, `figaro.queued`, `figaro.queue.update`, `figaro.queue.delete`,
`figaro.study`, `figaro.drop`, `figaro.cast`, `figaro.follow`, and the reads
`figaro.read`, `figaro.context`, `figaro.form`, which a live agent answers and
the store answers otherwise.

`api/rpc/methods.go` and `api/rpc/address.go` are the list of record.

A connection follows an aria once it opens or steers its stream there,
`figaro.follow` or `figaro.qua` (`rpc.MethodFollows`), and only where the
caller holds `listen`. Reads and every other
method never subscribe, so a connection used for lookups collects no pushes.
Pushes carry `figaro_id`, so one connection may follow several arias. When an
aria's endpoint closes (kill, drain) its followers get `figaro.closed` and
their connections stay up.

A second, web door is configured by `[door.web]` (`path`, `mode`, default
0660; `max_conn_age`, default 8h; `authn`): a unix socket accepting an HTTP
upgrade to WebSocket, then the same JSON-RPC, one frame per text message. Its
authenticator (`upstream` or `bearer`) admits at the upgrade and belongs to
that listener alone; a credential it refuses gets 401, on which the wss client
invalidates and retries once. Any other `authn` and the door does not start.
Admission and the policy are in the Authorization section of
`skills/figaro/reference/architecture.md`. A connection
reaching its age is closed with 1008 and must upgrade again, because a
fronting proxy authorizes only the upgrade.

A third, node door is configured by `[door.tls]` (`listen`, `cert`, `key`,
`client_ca`) and serves the same methods to peers over mutual TLS. Peers are
static, `[peers.<node id>]` (`endpoint = "tls://host:port"`, `identity`, the
URI their certificate must carry, and the `ca`, `cert`, `key` this node dials
them with). A peer's first line is a passage: empty when the node speaks for
itself, or `forwarded` with the caller's bearer, which the owner verifies
against `[authz.bearer]`; nodes never vouch for a caller.

An aria lives on the node that minted it, forks included. A request naming
an aria this node does not hold (by id, prefix, or `<node>/<id>`) goes once,
whole, to its owner on a link kept per caller connection, and the owner's
pushes come back the same way. The owner authorizes; the entry node only
requires a bearer to forward. A forwarded request is served where it lands or
refused. `internal/peer` holds the door, the links and the directory of
remote owners, which learns from forks, listings and `node.resolve`
lookups. `figaro.list` returns this node's rows and every peer's, marked with
their `node`, plus an `unreachable` entry per peer that did not answer.

### Streaming: the turn-shaped aria page

The wire carries the **UI IR**, not canonical `message.Message` IR. The one
conversation shape is `aria.Page`, used by both pull and push:

```go
type Page struct {
    Parts   []TurnPart `json:"parts,omitempty"`
    More    More       `json:"more"`
    Metrics *Metrics   `json:"metrics,omitempty"`
}
```

A `TurnPart` embeds a turn (`turn`, `inquiry`, `at`, provenance `lts`, `sealed`,
`nodes`, optional `live`) and adds `from`, `clipped_head`, and `clipped_tail`.
`Inquiry` is the question that opened the turn; nodes are agent `prose`,
`thinking`, `tool` lifecycles, or user `steering` interjections. Every part
repeats its inquiry so a listener joining mid-stream does not depend on one
opening frame.

The UI coordinate is `(turn, node ordinal)`. Snapshot node `i` is addressed as
`from+i`. A node may still serialize a legacy string `id` carrying fig-IR or
provider provenance; clients must not use it as UI identity. `Live.From` marks
the mutable suffix and each `NodeDelta` addresses a positional ordinal with a
`uint64 id`. Deltas merge `set` fields, remove `unset` fields, and splice
`markdown`/`output` through `patch`.

Notifications:

- `figaro.aria`: one `Page`, either a snapshot/catch-up-compatible part, a
  live delta, a live-suffix close marker, or a sealed turn;
- `turn.done` `{reason,idle}`: control state saying the turn ended and whether
  queued work remains;
- `form.delta`: one committed form patch, in the shape a client sends one.

`figaro.read` is the pull half. Its current request retains pre-turn field
names: `sinceLT` is a forward **turn** cursor; `before` and `before_node` form a
backward `(turn,node)` anchor; `limit` is a byte budget. A missed live version
causes a re-read from the client's highest fully sealed turn. Read/push overlap
is valid and client application is idempotent.

The canonical IR remains on disk and is the provider input. `internal/uiir`
projects it to turns/nodes; `internal/livelog/aria.Server` derives pages and
field deltas; consumers choose their own presentation. Tool output is bounded
in the UI projection while the full result remains in canonical IR. Running
spinners animate on the consumer, producing no timer traffic on the wire.

## CLI

| Command | Description |
|---------|-------------|
| `q <prompt>` / `figaro -- <prompt>` | Prompt (auto-resolves or creates) |
| `figaro send [--id <id>] [-e] [-r] [-x] -- <prompt>` | Explicit prompt; `-e` ephemeral, `-r` raw, `-x` bash exec (orthogonal flags) |
| `figaro new -- <prompt>` | New conversation with chosen outfit/patch |
| `figaro list` | List all arias (live + dormant) |
| `figaro kill <id>` | Kill + delete aria |
| `figaro attend <id>` | Rebind this shell |
| `figaro show <id> -j` | Dump materialized UI turns as a `Page` |
| `figaro models` | List provider models |
| `figaro login <provider>` | OAuth PKCE |
| `figaro listen <id>` | Follow an aria without sending a prompt |
| `figaro stop` | Shut down the angelus |

## Layout

```
~/.config/figaro/                 config, credo.md, skills/, providers/
$XDG_RUNTIME_DIR/figaro/          angelus.sock, angelus.pid
~/.local/state/figaro/
├── angelus.log                   supervisor log
├── traces.jsonl                  OTel exporter
└── arias/<id>/...                see "Streams" above
```

## Roadmap

- **A public frontend protocol.** `figaro.hello` carries a version and
  capabilities; still owed are compatibility rules, the wire/folding client
  outside `internal/`, and a language-neutral schema. Exact build matching is
  the temporary guard.
- Isolate subscriber backpressure with bounded per-client delivery; one slow
  external listener must not block the aria's ordered fan-out.
- More providers.
- Agent pooling.
- Context compaction.
- Child-process agents (currently goroutines under the angelus).
