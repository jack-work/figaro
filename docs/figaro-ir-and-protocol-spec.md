# Figaro IR & RPC Protocol Specification

**Audience:** an implementer (human or agent) writing a figaro client, an alternative
provider, or a tool that reads aria files directly. Authoritative as of branch
`docs/stream-log-redesign` (base commit `37feae0`).

**Authority rule.** Where this document and `ARCHITECTURE.md` disagree, **this document
and the Go source win** — `ARCHITECTURE.md` is partly stale (it lists figaro methods
`set_model`/`set_label`/`info` that no longer exist, and a single `aria.jsonl` log file
that is now a figwal segment directory). Where an *on-disk aria* and the current Go
source disagree, **the current source defines the format**; figaro is forward-only and
does not shim old on-disk shapes (`agents.md` invariant). §4.8 lists the known historical
drifts so you can recognize old data.

Two surfaces are specified:

1. **The wire protocol** — JSON-RPC 2.0 over a Unix socket, between a client (CLI,
   frontend) and the angelus/figaro servers. Includes the streaming notifications a
   client renders.
2. **The on-disk format** — what is written under `arias/<id>/`: the append-only IR
   write-ahead log, the per-provider translator cache, the chalkboard, and derived
   metadata.

---

## 1. System topology & sockets

One Go binary, three roles (`ARCHITECTURE.md`):

| Role | Lifetime | Socket | Speaks |
|---|---|---|---|
| **CLI** (`q`, `l`, `figaro`) | one invocation | — (dials others) | client |
| **Angelus** — supervisor: registry, PID bindings, lazy restore, shared log cache | one per user | `$XDG_RUNTIME_DIR/figaro/angelus.sock` | server |
| **Figaro** — actor: one conversation ("aria"), one inbox, one drain goroutine | one per aria | `$XDG_RUNTIME_DIR/figaro/figaros/<id>.sock` (chmod `0600`) | server |

`<id>` is an 8-hex-char aria id (e.g. `0080ba7c`). Sockets are created by the listener,
`os.Remove`'d first if stale (`internal/figaro/protocol.go:13`).

**Transport** (`internal/transport/transport.go`). Endpoints are `{scheme, address}`
with `scheme ∈ {unix, tcp}`. TCP is wired but unused in practice. An `Endpoint` is also
the JSON shape returned by `figaro.create`/`figaro.attach`/`pid.resolve`:

```json
{"scheme": "unix", "address": "/run/user/1000/figaro/figaros/0080ba7c.sock"}
```

---

## 2. Transport & framing (jkrpc / JSON-RPC 2.0)

The framing library is `github.com/jack-work/jkrpc@v0.1.0`. A re-implementation in any
language interoperates if it follows this section exactly.

### 2.1 Framing

**NDJSON.** One JSON object per line, terminated by a single `\n` (`0x0A`). No length
prefix, no `Content-Length` header, no other delimiter. Encoder = Go `json.Encoder`
(emits compact JSON + `\n`); decoder = Go `json.Decoder` (reads one object, consumes the
newline). UTF-8.

### 2.2 The envelope

Every frame is one `Message` object:

```go
type Message struct {
    JSONRPC string          `json:"jsonrpc"`          // always "2.0"
    ID      *int64          `json:"id,omitempty"`     // present iff request or response
    Method  string          `json:"method,omitempty"`
    Params  json.RawMessage `json:"params,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *Error          `json:"error,omitempty"`
}

type Error struct {
    Code    int             `json:"code"`
    Message string          `json:"message"`
    Data    json.RawMessage `json:"data,omitempty"`
}
```

Frame classification (by presence of `id` and `method`):

| Kind | `id` | `method` | other |
|---|---|---|---|
| **Request** | present | present | `params` optional |
| **Response (ok)** | present | absent | `result` (omitted if handler returned `nil`) |
| **Response (err)** | present | absent | `error` present |
| **Notification** | absent | present | `params` optional; no reply expected |

Concrete frames:

```json
{"jsonrpc":"2.0","id":1,"method":"figaro.qua","params":{"text":"hi"}}
{"jsonrpc":"2.0","id":1,"result":{"ok":true}}
{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found: figaro.bogus"}}
{"jsonrpc":"2.0","method":"stream.delta","params":{"text":"Su","content_type":"text"}}
```

### 2.3 IDs, multiplexing, ordering

- `id` is a signed 64-bit integer, **monotonically increasing from 1 per client
  connection** (`atomic.Int64.Add(1)`). Never reused within a connection.
- Multiple requests may be in flight on one connection; responses are routed back to the
  waiting caller by `id`. **Responses may arrive out of request order.** A response for an
  unknown `id` is discarded.
- Wire delivery is FIFO per direction. Server-initiated **notifications share the same
  connection and ordering stream** as responses — a streaming turn interleaves many
  `stream.*` notifications with (eventually) the response to the triggering request.
- Write side is mutex-serialized (safe from many goroutines); read side is single-reader.

### 2.4 Errors

- Handler returns a typed `*Error` → passed through verbatim (`code`, `message`, `data`).
- Handler returns a plain `error` → wrapped as `{code:-32000, message: err.Error()}`.
- Unknown method → `{code:-32601, message:"method not found: <m>"}`.
- Result marshal failure → `{code:-32603, message:"failed to marshal result"}`.

Figaro-specific application codes (`internal/rpc/methods.go:33`), all carrying `data` of
type `ErrorData {available_providers?, loadout?, name?, search_paths?}`:

| Code | Meaning |
|---|---|
| `-32010` | `ErrNoDefaultLoadout` — no `default_loadout` configured and request omitted one |
| `-32011` | `ErrNoProvider` — resolved loadout has no `system.provider` key |
| `-32012` | `ErrLoadoutNotFound` — named loadout not on disk |

### 2.5 Server semantics

The figaro/angelus server loop dispatches **requests** to handlers and **ignores inbound
notifications**. Clients are the only notification *consumers* (via an `onNotify(method,
params)` callback). Servers are the only notification *producers* (via `Notify`).

---

## 3. The Figaro IR (message model)

Package `internal/message`. This is the provider-agnostic intermediate representation —
the canonical content of the log and of `figaro.context` / `stream.message`. Per-provider
wire bytes are a *derived cache* (§4.5), never authoritative.

### 3.1 Enumerations

```go
type Role string
const (
    RoleUser            Role = "user"             // prompts AND tool_result tics AND state-only patch tics
    RoleAssistant       Role = "assistant"
    RoleToolResult      Role = "tool_result"      // reserved; tool results currently ride a user tic (§3.5)
    RoleSystem          Role = "system"           // compacted summary header
    RoleSystemInterrupt Role = "system.interrupt" // sentinel for dangling tool calls (§3.6)
)

type ContentType string
const (
    ContentText       ContentType = "text"
    ContentImage      ContentType = "image"
    ContentThinking   ContentType = "thinking"
    ContentToolInvoke ContentType = "tool_invoke"  // assistant-authored tool call
    ContentToolResult ContentType = "tool_result"  // result of a completed tool call
    ContentInterrupt  ContentType = "interrupt"    // one per dangling tool_call_id
)

type StopReason string
const (
    StopEnd        StopReason = "stop"
    StopLength     StopReason = "length"
    StopToolInvoke StopReason = "tool_invoke"
    StopError      StopReason = "error"
    StopAborted    StopReason = "aborted"
)

type InterruptReason string
const (
    InterruptFault         InterruptReason = "fault"
    InterruptUserInterrupt InterruptReason = "user_interrupt"
    InterruptAgentExit     InterruptReason = "agent_exit"
)
```

`InterruptReason` is open-coded: unknown values pass through rather than erroring.

### 3.2 `Content` — the heterogeneous block union

```go
type Content struct {
    Type ContentType `json:"type"`

    Text string `json:"text,omitempty"`   // text/thinking body; also the tool_result body

    MimeType string `json:"mime_type,omitempty"` // image
    Data     string `json:"data,omitempty"`      // image, base64

    ToolCallID string                 `json:"tool_call_id,omitempty"` // tool_invoke/tool_result/interrupt
    ToolName   string                 `json:"tool_name,omitempty"`
    Arguments  map[string]interface{} `json:"arguments,omitempty"`    // tool_invoke, decoded args

    IsError bool            `json:"is_error,omitempty"` // tool_result
    Reason  InterruptReason `json:"reason,omitempty"`   // interrupt
}
```

`type` is the discriminator; a renderer/encoder dispatches on it. Field population per kind:

| `type` | Populated fields |
|---|---|
| `text` | `text` |
| `thinking` | `text` (the thinking body) |
| `image` | `mime_type`, `data` (base64) |
| `tool_invoke` | `tool_call_id`, `tool_name`, `arguments` |
| `tool_result` | `tool_call_id`, `tool_name`, `text` (result), `is_error` |
| `interrupt` | `tool_call_id`, `tool_name`, `reason`, `text` (human description) |

`arguments` is a decoded JSON object (`map[string]interface{}`), not a string — the
provider parses the streamed partial-JSON before it lands in the IR.

### 3.3 `Usage`

```go
type Usage struct {
    InputTokens      int `json:"input_tokens"`
    OutputTokens     int `json:"output_tokens"`
    CacheReadTokens  int `json:"cache_read_tokens"`
    CacheWriteTokens int `json:"cache_write_tokens"`
}
```

### 3.4 `Message`

```go
type Message struct {
    Role    Role      `json:"role"`
    Content []Content `json:"content"`

    Patches []Patch `json:"patches,omitempty"` // chalkboard mutations on this tic (§7)

    // Assistant-only metadata:
    Model      string     `json:"model,omitempty"`
    Provider   string     `json:"provider,omitempty"`
    Usage      *Usage     `json:"usage,omitempty"`
    StopReason StopReason `json:"stop_reason,omitempty"`

    // Deprecated: superseded by tool_result Content blocks. May be absent.
    ToolCallID string `json:"tool_call_id,omitempty"`
    ToolName   string `json:"tool_name,omitempty"`

    LogicalTime uint64 `json:"logical_time"` // see §3.7
    Timestamp   int64  `json:"timestamp"`    // unix millis when the tic was created
}
```

A single `Message` carries an **ordered list of heterogeneous blocks**. A typical
assistant turn that calls a tool: `Content = [ {type:text}, {type:tool_invoke} ]` with
`stop_reason:"tool_invoke"`.

### 3.5 How a turn maps onto messages

The actor appends, in order, **separate** log entries (`internal/figaro/turn.go`):

1. **User tic** — `role:"user"`, the prompt text as a `text` block, plus any chalkboard
   `patches` (the first turn folds the boot patch: credo, skills, cwd, etc.).
2. **Assistant tic** — `role:"assistant"`, `text`/`thinking`/`tool_invoke` blocks,
   `model`/`provider`/`usage`/`stop_reason`. Appended atomically when the provider stream
   completes (this is when it gets its index).
3. If there were tool calls: **tool-result tic** — currently `role:"user"` with one
   `tool_result` block per call (matched by `tool_call_id`), then loop back to step 2.

So `tool_invoke` (assistant message) and its matching `tool_result` (next user message)
live in **adjacent log entries with consecutive indices**, never in the same entry.
A state-only `figaro.set` produces a `role:"user"` tic with `patches` and no `content`.

### 3.6 The interrupt sentinel

If a prior assistant turn left `tool_invoke` blocks with no matching results (interrupt,
crash, agent exit), an append-only sentinel reconciles the dangling calls instead of
truncating the log:

- `role:"system.interrupt"`, one `interrupt` block per dangling `tool_call_id`, each with
  `reason` and a human `text`.
- The translator (§4.5) is responsible for emitting a provider-acceptable surrogate (e.g.
  a synthetic `tool_result`) so the next request body is well-formed. The IR stays honest.

### 3.7 `logical_time` vs. the durable index

**The authoritative per-message index is the log envelope's `LT` (§4.3), not the
`logical_time` field inside the payload.** On disk `logical_time` is frequently `0`; it is
**stamped from `Entry.LT` when messages are read out** (`unwrapMessages`,
`internal/figaro/agent.go:250`). Treat in-payload `logical_time` as advisory; trust the
envelope `LT` / the `logical_time` field of `stream.message` / the `lt` of `aria.read`.

---

## 4. On-disk format (the write-ahead log & aria directory)

State root: `~/.local/state/figaro/` (alongside it: `angelus.log`, `traces.jsonl`,
`figaros/<id>.jsonl` event logs). Each aria is a directory:

```
arias/<id>/
├── aria/                              IR write-ahead log (figwal segment dir)  ← canonical
│   └── 00000000000000000001.jsonl     segment, base index 1
├── aria.jsonl                         LEGACY single-file IR log (only if pre-figwal)
├── chalkboard.json                    current chalkboard snapshot (atomic rewrite)
├── meta.json                          AriaMeta summary (what `figaro list` reads)
├── translations/
│   ├── <provider>/                    translator cache (figwal segment dir)
│   │   └── 00000000000000000001.jsonl
│   ├── <provider>.jsonl               LEGACY single-file translator cache
│   └── <provider>.meta.json           TranslationMeta summary
└── derived/
    ├── usage.json                     derived token totals
    └── meta.json                      derived self-contained snapshot for dormant list
```

### 4.1 Two log formats, and how to tell them apart

A `Log[T]` column is backed by either **figwal** (a directory of segments) or the
**legacy** single NDJSON file. Selection (`internal/store/file_backend.go:68`,
`pickLogFormat`): **if the legacy file exists, use legacy; otherwise use figwal.** New
arias are always figwal. Both expose identical semantics (`internal/store/log.go`):

```go
type Log[T any] interface {
    Read() []Entry[T]                       // all entries, ascending
    Lookup(figaroLT uint64) (Entry[T], bool)
    PeekTail() (Entry[T], bool)
    ScanFromEnd(n int) []Entry[T]
    Append(e Entry[T]) (Entry[T], error)    // stamps a fresh LT
    Clear() error                           // translator caches only
    Close() error
}
```

### 4.2 The `Entry` envelope (both formats)

Every record on every log is an `Entry[T]` (`internal/store/log.go:5`), **serialized with
Go default (capitalized) field names** because the struct has no JSON tags:

```go
type Entry[T any] struct {
    LT          uint64 // monotonic per-log index, == the offset
    FigaroLT    uint64 // foreign key (see below)
    Payload     T      // message.Message for the IR log; []json.RawMessage for translator
    Fingerprint string // encoder-config fingerprint (translator entries; "" on IR)
}
```

On the wire/disk the JSON keys are literally `LT`, `FigaroLT`, `Payload`, `Fingerprint`.

- **`LT`** — the durable index. **1-based, gapless, monotonic, single-writer.** Append
  computes `LT = LastIndex()+1` (figwal) / `nextLT++` from 1 (legacy). It is *the* offset
  used by `aria.read` (`from`/`next_from`) and `stream.message.logical_time`.
- **`FigaroLT`** — a foreign-key column. On the **IR log** it equals `LT`. On a
  **translator** log it is the IR `LT` of the message that entry translates (so a
  translator entry can be looked up by the IR index it caches). Defaults to `LT` if zero.
- **`Fingerprint`** — set on translator entries to the encoder config hash (e.g.
  `"anthropic-sdk/tag/v1"`); empty on IR entries. A mismatch invalidates the cache (§4.5).

### 4.3 figwal segment format (the current WAL)

Library `github.com/jack-work/figwal@v0.2.0`, opened with the **JSONL codec**.

**Directory & naming.** Segments live directly in the log dir, named
`%020d<ext>` where the 20-digit zero-padded number is the segment's **base global index**
(first `LT` in the segment) and `<ext>` is `.jsonl` (JSONL codec) or `.seg` (binary codec;
not used by figaro). Example: `00000000000000000001.jsonl` (base index 1). A new segment
rotates in when the active one passes the size limit (default 64 MiB); its filename's base
index is the index of the entry that triggered the roll. Old segments are never merged
automatically. Optional fork markers `.fork` / `.fork-pending` may appear if the
fork primitive is used (out of scope here).

**Line format (JSONL codec).** Each line is the `Entry` JSON **plus two reserved sidecar
keys**, re-marshaled in canonical form (keys sorted lexicographically by byte, compact):

- `_idx` (uint64) — the global index, equals `Entry.LT`.
- `_hash` (string) — 16 hex chars = first 8 bytes of SHA-256 over the canonical JSON of
  the line **without** the sidecars. Integrity check; recomputed and verified on read.

Because the `Entry` keys are capitalized (`F`,`L`,`P` = 0x46/0x4C/0x50) and the sidecars
start with `_` (0x5F), **the sidecars sort last**. A real IR line (pretty-printed here;
on disk it is one physical line):

```json
{"FigaroLT":2,"Fingerprint":"","LT":2,
 "Payload":{"content":[{"text":"Subito!","type":"text"},
                       {"arguments":{"command":"figaro status"},
                        "tool_call_id":"toolu_01KTb...","tool_name":"bash","type":"tool_invoke"}],
            "logical_time":0,"model":"claude-opus-4-8","provider":"anthropic",
            "role":"assistant","stop_reason":"tool_invoke","timestamp":1779040850000,
            "usage":{"cache_read_tokens":0,"cache_write_tokens":0,"input_tokens":4558,"output_tokens":72}},
 "_hash":"8ecd6cb06a43547c","_idx":2}
```

**Reading without the library:** split on `\n`; for each non-empty line, `json.Unmarshal`
into the `Entry` shape; the sidecars `_hash`/`_idx` are advisory (verify `_hash` if you
want integrity; `_idx` mirrors `LT`). Reserved keys must never collide with payload keys —
the codec rejects a payload that already contains `_idx`/`_hash`.

**Recovery & durability.** On open, figwal scans each segment frame-by-frame, rebuilds the
in-memory offset index, and **truncates a torn tail** (an incomplete final line, or a
hash mismatch) back to the last good record. Writes fsync by default. `FirstIndex()` =
first segment's base; `LastIndex()` = last record's index. Reads go through a lock-free
atomic snapshot cache (`figwal/log`), so a reader (the angelus serving `aria.read`) and
the writer (the agent) can share one instance without locking — this is what the shared
`LogCache` (§4.7) exploits.

### 4.4 Legacy single-file format (`aria.jsonl`)

Plain NDJSON: one `Entry` JSON object per line, **no sidecars** (`internal/store/file_log.go`).
Loaded fully into memory at open; appended with `O_APPEND`. Present only on arias created
before the figwal migration; recognized because the file exists (§4.1).

### 4.5 The translator stream (`translations/<provider>/`)

A per-provider **derived cache** of input-ready wire bytes. Same `Log`/`Entry`/figwal
machinery, but `Payload` is `[]json.RawMessage` — the provider-native message(s) that one
IR message encodes to. `FigaroLT` links each translator entry back to its IR `LT`;
`Fingerprint` records the encoder config.

Contract (`agents.md` invariants #2–#5):

- **`aria/` is canonical; the translator is regenerable** by walking the IR through
  `Provider.Encode`. On a `Provider.Fingerprint()` mismatch the agent `Clear()`s the whole
  translator log and repopulates.
- **Entries are input-ready** and byte-stable: assistant entries are re-encoded so that
  response-only fields (`stop_reason`/`model`/`usage`, which the API rejects on input) stay
  on the IR Message and out of the cache. Per-message bytes are written **exactly once**,
  giving a byte-identical request prefix across turns (so Anthropic `cache_control` markers
  actually hit).
- **Chalkboard reminders are rendered into the wire here**, not into the IR. A real
  translator line shows the IR user text followed by injected `text` blocks like
  `<system-reminder name="cwd">…</system-reminder>` (§7.3). The IR user message has none of
  that — it carries the raw text plus `patches`.

`translations/<provider>.meta.json` is a `TranslationMeta` summary
`{provider, entry_count?, total_bytes?, fingerprint?, last_trans_lt?, last_update_ms?}`
(omitempty; small values omitted).

### 4.6 Chalkboard, meta, derived files

- **`chalkboard.json`** — a single flat JSON object: chalkboard key → raw-JSON value
  (§7). Atomic rewrite (tmp+rename) at turn end. It is a **cache of a projection**: the
  authoritative source is the `patches` recorded on user tics in the IR log; on load with
  an empty/missing file the agent replays all patches to rebuild it.
- **`meta.json`** — `AriaMeta {message_count?, turn_count?, tokens_in?, tokens_out?,
  cache_read_tokens?, cache_write_tokens?, last_active_ms?, last_figaro_lt?}` (omitempty).
  Example: `{"message_count":8,"turn_count":4,"tokens_in":18908,"tokens_out":238,"last_active_ms":1779040868856}`.
- **`derived/usage.json`**, **`derived/meta.json`** — richer derived snapshots written by
  the derivation fanout (`internal/figaro/derived.go`); `derived/meta.json` mirrors
  `rpc.FigaroInfoResponse` field names so the angelus can serve a dormant aria's `figaro
  list` row without restoring it.

### 4.7 Cross-process access: the shared `LogCache`

The angelus holds a refcounted, TTL-evicted `LogCache` (`internal/store/cache.go`). Both
the live agent and the angelus's `aria.read` handler `AcquireIR(id)` the **same** figwal
`Log` instance, so reads run lock-free against the agent's writes. Eviction happens only
after every borrower releases and the entry idles past the TTL. This is why `aria.read`
(§5.10) is safe to call against a live aria.

### 4.8 Known historical on-disk drifts

Forward-only means old arias may carry shapes the current code no longer writes. When
parsing arbitrary on-disk data, tolerate:

- `Content.type == "tool_call"` (renamed to `tool_invoke`) and `stop_reason == "tool_use"`
  (renamed to `tool_invoke`).
- `logical_time` populated with `0` (now always; the envelope `LT` is truth — §3.7).
- `timestamp == 0` on older assistant tics.
- Legacy single-file `aria.jsonl` / `translations/<provider>.jsonl` instead of segment dirs.

The current code is authoritative for anything **newly written**.

---

## 5. RPC surface — Angelus socket

`$XDG_RUNTIME_DIR/figaro/angelus.sock`. Methods (`internal/angelus/protocol.go:65`);
request/response Go types in `internal/rpc/methods.go`. All examples show only `params` /
`result` bodies.

### 5.1 `figaro.create` — create (or restore) an aria

```jsonc
// params: CreateRequest
{"id": "", "loadout": "default", "patch": {"set": {...}, "remove": [...]}, "ephemeral": false}
// result: CreateResponse
{"figaro_id": "0080ba7c", "endpoint": {"scheme":"unix","address":"…/figaros/0080ba7c.sock"}}
```

`id` empty ⇒ auto-generated. `loadout` selects the on-disk loadout (resolves credo, skills,
provider, model knobs); omitting it with no `default_loadout` configured ⇒ error `-32010`.
`patch` applies an initial chalkboard delta. `ephemeral:true` ⇒ no aria persisted.

### 5.2 `figaro.attach` — restore a dormant aria without binding a PID

```jsonc
// params: AttachRequest {"figaro_id":"0080ba7c"}
// result: AttachResponse {"figaro_id":"0080ba7c","endpoint":{...}}
```

### 5.3 `figaro.kill` — kill the agent and delete the aria

```jsonc
// params: KillRequest {"figaro_id":"0080ba7c"}
// result: KillResponse {"ok":true}
```

### 5.4 `figaro.list` — list all arias (live + dormant)

```jsonc
// params: none
// result: ListResponse {"figaros": [ FigaroInfoResponse, … ]}
```

`FigaroInfoResponse` (also the per-aria info shape elsewhere):

```go
{
  "id":"0080ba7c", "state":"idle|active", "provider":"anthropic", "model":"claude-opus-4-8",
  "message_count":8, "tokens_in":18908, "tokens_out":238,
  "cache_read_tokens":0, "cache_write_tokens":0,
  "context_tokens":12044, "context_exact":true,        // est. next-turn input; exact iff from a Usage watermark
  "created_at":1779040849000, "last_active":1779040868856,  // unix millis
  "bound_pids":[12345]
}
```

### 5.5–5.7 PID binding (`pid.bind`, `pid.resolve`, `pid.unbind`)

A shell PID maps to at most one aria (1:1), so `q`/`l` resolve "the current shell's aria".

```jsonc
// pid.bind    params BindRequest    {"pid":12345,"figaro_id":"0080ba7c"} → {"ok":true}
// pid.resolve params ResolveRequest {"pid":12345} → {"figaro_id":"0080ba7c","endpoint":{...},"found":true}
// pid.unbind  params UnbindRequest  {"pid":12345} → {"ok":true}
```

### 5.8 `angelus.status`

```jsonc
// params: none → StatusResponse
{"uptime_ms":3600000, "figaro_count":3, "bound_pids":2}
```

### 5.9 `angelus.save_bindings`

```jsonc
// params: none → SaveBindingsResponse {"ok":true, "count":2}
```

### 5.10 `aria.read` — windowed history read by index (cross-process)

The catch-up primitive. Served through the shared `LogCache` (§4.7), so it is consistent
with a live agent's writes.

```jsonc
// params: AriaReadRequest
{"figaro_id":"0080ba7c", "from":1, "limit":100}
// result: AriaReadResponse
{"entries":[{"lt":1,"payload":{…message…}}, {"lt":2,"payload":{…}}],
 "total":8, "next_from":3}
```

Semantics: `from` is **inclusive on `LT`** (`0` = from the beginning). `limit` is capped
server-side at **1000** (`ariaReadHardCap`); `limit<=0` means "server max". `entries[i].lt`
is the durable index; `entries[i].payload` is the raw `message.Message` JSON (the figaro
envelope is stripped). `total` is the entry count of the whole aria. `next_from` is the
`LT` to pass next for pagination, or `0`/absent when the window reached the end. Unknown /
typo'd `figaro_id` ⇒ error (no aria on disk).

> Note: `angelus.info` exists as a constant (`MethodAngelusInfo`) but is **not** registered
> in the handler map and is not callable.

---

## 6. RPC surface — Figaro socket

`$XDG_RUNTIME_DIR/figaro/figaros/<id>.sock`. The agent registers each connection as a
notification subscriber for the lifetime of the connection (`internal/figaro/protocol.go:52`),
so opening a connection is implicitly "subscribe to this aria's live stream." Request
methods (`internal/figaro/server.go:19`):

### 6.1 `figaro.qua` — submit a prompt (async)

```jsonc
// params: QuaRequest
{"text":"can you call figaro status?",
 "chalkboard": {"context": {"cwd":"\"/x\""}, "patch": {"set":{…}, "remove":[…]}}}
// result: QuaResponse {"ok":true}
```

Returns immediately once the user tic is enqueued; the assistant response arrives as
`stream.*` notifications (§6.7). The optional `chalkboard` field carries per-send state
(§7.2).

### 6.2 `figaro.interrupt` — abort the in-flight turn

```jsonc
// params: InterruptRequest {} → InterruptResponse {"ok":true}
```

Idempotent when idle. Cancels the turn context; stragglers from cancelled goroutines are
suppressed; a dangling tool call is reconciled with an interrupt sentinel (§3.6).

### 6.3 `figaro.context` — full history dump

```jsonc
// params: none → ContextResponse {"messages":[ message.Message, … ]}
```

Every message, in order, each with `logical_time` stamped from its `LT`. No windowing
(use `aria.read` for paging).

### 6.4 `figaro.set` — apply a chalkboard patch directly (no model round-trip)

```jsonc
// params: SetRequest {"patch":{"set":{"mode":"\"concise\""}, "remove":["truncation"]}}
// result: SetResponse {"ok":true, "set":["mode"], "remove":["truncation"]}
```

Records a `role:"user"` tic carrying the patch, applies it to the chalkboard, persists.

### 6.5 `figaro.loadout` — apply a named loadout additively

```jsonc
// params: LoadoutRequest {"name":"go-dev"}
// result: LoadoutResponse {"ok":true, "set":["system.credo","system.skills",…]}
```

Additive: keys whose value already equals the snapshot are skipped; nothing is removed.

### 6.6 `figaro.chalkboard` — read the current snapshot

```jsonc
// params: none → ChalkboardResponse {"snapshot": { "<key>": <raw json>, … }}
```

### 6.7 Streaming notifications (the rendering wire)

Server→client notifications during a turn. **No `id`.** Methods and param shapes
(`internal/rpc/methods.go`, `internal/rpc/rpc.go`):

| Method | Params | Fires when |
|---|---|---|
| `stream.delta` | `{text, content_type}` | a text (or thinking) token chunk; `content_type` ∈ IR `ContentType` |
| `stream.thinking` | `{text}` | a thinking chunk (alternative surface) |
| `stream.tool_invoke_start` | `{tool_call_id, tool_name}` | assistant *begins authoring* a tool call |
| `stream.tool_invoke_delta` | `{tool_call_id, partial_json}` | partial tool-argument JSON (best-effort; droppable) |
| `stream.tool_invoke_ready` | `{tool_call_id, tool_name, arguments}` | tool-arg JSON fully decoded |
| `stream.tool_start` | `{tool_call_id, tool_name, arguments}` | tool *execution* begins |
| `stream.tool_output` | `{tool_call_id, tool_name, chunk}` | a chunk of tool stdout |
| `stream.tool_end` | `{tool_call_id, tool_name, result, is_error}` | tool execution finished |
| `stream.message_end` | `{stop_reason}` | the assistant message reached `message_stop` (metadata before body) |
| `stream.message` | `{logical_time, message}` | the **full** assembled `message.Message`, with its durable `LT` |
| `stream.done` | `{reason}` | the turn is complete (idle); `reason` is the stop reason or an error string |
| `stream.error` | `{message}` | an error during the turn |

Distinct vocabularies on purpose: `tool_invoke_*` describe the model *authoring* a call;
`tool_start`/`tool_output`/`tool_end` describe the harness *executing* it (these may run
speculatively, in parallel, before the assistant message is sealed).

**Ordering contract (per turn):**

1. For each assistant round: a stream of `stream.delta` / `stream.thinking` /
   `stream.tool_invoke_start` / `stream.tool_invoke_delta` / `stream.tool_invoke_ready`,
   interleaved with execution events `stream.tool_start` / `stream.tool_output` /
   `stream.tool_end` for any speculatively-dispatched tools.
2. `stream.message_end` (carries `stop_reason` so a renderer can commit layout decisions).
3. `stream.message` — the full assembled message. **Guaranteed to arrive after all
   deltas/tool-invoke/message_end of that round** (the drain loop flushes pending deltas
   before fanning out the message). This is the seal for the round; the message's
   `logical_time` is its durable `LT`.
4. Rounds repeat (tool calls → results → next assistant message).
5. `stream.done` once, at turn end. `stream.error` may precede `stream.done` on failure.

A client that wants exact history uses `stream.message`/`stream.done` for the durable log
and the finer events purely for live rendering. Today the deltas carry **no index or block
id** — a renderer folds them by arrival order and resets on `stream.message`. (The
companion design doc `duck/2026-06-13-resumable-message-log-stream.md` proposes addressing
this.)

### 6.8 Crash recovery surface

On a panic the agent restarts its drain loop, restores context from the log, and emits
`stream.error` (`"agent crashed and was restarted; context restored from last
checkpoint"`) then `stream.done`. The aria id, socket, registry entry, PID bindings, and
credo survive — invisible to the model.

---

## 7. Chalkboard protocol

Per-aria structured state, surfaced to the model as system reminders. Open schema: keys
are arbitrary dotted strings, values are raw JSON (`internal/chalkboard`).

### 7.1 Snapshot & patch

```go
type Snapshot map[string]json.RawMessage           // full key→raw-JSON view
type Patch struct {                                 // == message.Patch
    Set    map[string]json.RawMessage `json:"set,omitempty"`
    Remove []string                   `json:"remove,omitempty"`
}
```

`Apply`: set wins, then removes delete. `Merge(p,q)`: q wins on conflict (a set cancels a
prior remove and vice-versa). On the wire (`figaro.set`, `figaro.create.patch`,
`figaro.qua.chalkboard.patch`) a patch is `ChalkboardPatch {set, remove}`. In the IR a
user tic carries `patches: [Patch, …]`.

### 7.2 `figaro.qua` chalkboard input — context vs. patch

`ChalkboardInput {context?, patch?}` on a prompt has two contracts:

- **`context`** (`map[key]rawjson`) is **purely additive**: the agent sets keys whose
  value differs from the snapshot, but never derives a removal from absence. Lets a client
  ship its whole view without racing another shell. **Keys in `system.*` are dropped from
  `context`** — the harness owns that namespace and a stale client view must not clobber it.
- **`patch`** (`{set, remove}`) is explicit, trusted mutation (`figaro set`/`unset`).
  `system.*` is allowed here (the user is explicit).

### 7.3 Reserved namespace & rendering

The `system.*` namespace is harness-reserved; providers read those keys directly (model,
credo, max_tokens, cache policy, …) and they are **not** rendered as reminders. Every
**non-`system.*`** key on a patch is rendered into the request wire as a text block:

```xml
<system-reminder name="cwd">
Working directory: /home/gluck/dev/figaro-qua/main
</system-reminder>
```

Rendering (`internal/chalkboard/render.go`): a key with a matching `*.tmpl` template
renders via that template; otherwise a generic body (the bare string for string values,
the JSON for object/array values). **Reminders live only in the translator/wire bytes, not
in the IR** (§4.5) — the IR user message keeps the raw `patches`.

### 7.4 Well-known keys (advisory catalog)

Not enforced; drives CLI completion (`internal/chalkboard/known_keys.go`). Modes: **U**ser-settable,
**S**ystem-managed, **E**phemeral-per-turn.

| Key | Mode | Meaning |
|---|---|---|
| `system.credo` | U | Credo source (string, or `{content,filePath,frontmatter}`); providers use it as the system prompt |
| `system.tags` | U | Per-LT annotations (e.g. cache-control markers) |
| `system.cache_control` | U | Auto cache-marker policy (`"ephemeral"` enables) |
| `system.environment.<name>` | U | Allowlisted env-var capture |
| `system.cwd` | S | Canonical working directory (set at create) |
| `system.model` / `model` | S | Active model id |
| `system.provider` | S | Provider name |
| `system.max_tokens` | S | Output token cap |
| `system.root` / `root` | S | Project root |
| `token_budget` | S | Context-window usage indicator |
| `truncation` | S | Last tool-truncation notice |
| `cwd` | E | Per-turn shell working directory |
| `datetime` | E | Per-turn wall-clock time |

(Observed extra keys in the wild, e.g. `system.prompt`, `system.skills`, are valid under
the open schema; the catalog is partial.)

---

## 8. Provider interface (context for implementers)

A provider is the only component that touches a vendor API (`internal/provider`). It is
out of the client's view but defines how IR ↔ wire conversion and streaming work:

```go
type Provider interface {
    Name() string
    Fingerprint() string                  // encoder-config hash; mismatch clears the translator cache
    Models(ctx) ([]ModelInfo, error)
    SetModel(model string)
    Send(ctx, SendInput, Bus) error       // drives one round end-to-end
}

type SendInput struct {
    AriaID    string
    FigLog    store.Log[message.Message]   // canonical IR log
    Snapshot  chalkboard.Snapshot
    Tools     []Tool
    MaxTokens int
}

type Bus interface {                       // sink the provider pushes streaming output to
    PushDelta(content message.Content)               // → stream.delta / stream.thinking
    PushToolInvokeStart(toolCallID, toolName string) // → stream.tool_invoke_start
    PushToolInvokeDelta(toolCallID, partialJSON string) // → stream.tool_invoke_delta
    PushToolReady(call message.Content)              // → stream.tool_invoke_ready (+ speculative dispatch)
    PushMessageEnd(stopReason string)                // → stream.message_end
    PushFigaro(msg message.Message)                  // appended to FigLog, then → stream.message
}
```

The provider parses vendor SSE, accumulates a whole assistant `message.Message`, appends
it to `FigLog` (assigning its `LT`), and pushes the streaming events above; the agent
fans them out as the §6.7 notifications. `Tool` is `{name, description, parameters}` where
`parameters` is a JSON schema.

---

## 9. Quick reference

**Durable index (`LT`):** uint64, 1-based, gapless, monotonic, single-writer, assigned at
append. The offset for `aria.read` and `stream.message.logical_time`. Authoritative over
in-payload `logical_time`.

**Aria files:** `aria/` (figwal IR, canonical) · `aria.jsonl` (legacy IR) ·
`translations/<p>/` (derived wire cache) · `chalkboard.json` (state projection) ·
`meta.json` + `derived/*` (summaries).

**Angelus methods:** `figaro.create`, `figaro.attach`, `figaro.kill`, `figaro.list`,
`pid.bind`, `pid.resolve`, `pid.unbind`, `angelus.status`, `angelus.save_bindings`,
`aria.read`.

**Figaro methods:** `figaro.qua`, `figaro.interrupt`, `figaro.context`, `figaro.set`,
`figaro.loadout`, `figaro.chalkboard`.

**Figaro notifications:** `stream.delta`, `stream.thinking`, `stream.tool_invoke_start`,
`stream.tool_invoke_delta`, `stream.tool_invoke_ready`, `stream.tool_start`,
`stream.tool_output`, `stream.tool_end`, `stream.message_end`, `stream.message`,
`stream.done`, `stream.error`.

**Source index:** IR `internal/message/` · log/WAL `internal/store/{log,figwal_log,file_log,file_backend,cache}.go`
· RPC types `internal/rpc/{rpc,methods}.go` · angelus handlers `internal/angelus/protocol.go`
· figaro server `internal/figaro/{server,protocol,agent,turn}.go` · chalkboard
`internal/chalkboard/` · transport `internal/transport/transport.go` · framing
`github.com/jack-work/jkrpc` · WAL `github.com/jack-work/figwal`.
