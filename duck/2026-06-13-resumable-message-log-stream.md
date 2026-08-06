# Reshaping the figaro stream: from lifecycle events to a resumable append-only log

**Status:** design proposal (rubber-duck). Scope: the Unix-socket JSON-RPC stream only. HTTP/SSE is explicitly out of scope; §4 flags the constraints so the lift stays cheap, but designs nothing.

**TL;DR recommendation:** keep **message-as-log-entry** (one entry = one `message.Message`, keyed by the existing `LT`); express the streaming tail as a sub-stream of **block-addressed delta frames** (`anchor → block_open → delta* → block_close → seal`) where every frame carries `(index, block, seq)`. Unify *all* streaming — assistant text/thinking/tool-args **and** tool-execution output — onto that one frame vocabulary. The durable truth stays sealed-message-granular; deltas are a live-bandwidth optimization that is always reconstructable from the sealed entry, which is what makes resume trivial.

---

## 1. Summary of today's behavior

### 1.1 Process & transport

Three roles, one binary (`ARCHITECTURE.md`): **CLI** (ephemeral translator), **angelus** (per-user supervisor), **figaro** (per-conversation actor). All IPC is JSON-RPC 2.0 over Unix sockets with NDJSON framing (`jkrpc`). Each figaro owns one inbox and one drain goroutine; every event (prompt, SSE delta, tool result, interrupt) is processed in order through that single loop (`internal/figaro/agent.go:402` `act`, invariant #1).

### 1.2 The canonical log

The source of truth is the per-aria IR log: `store.Log[message.Message]` (`internal/store/log.go:22`), append-only, three backends (`FigwalLog`, `FileLog`, `MemLog`). Each record is an `Entry[T]`:

```go
// internal/store/log.go:5
type Entry[T any] struct {
    LT          uint64 // monotonic per-aria index, == figwal global idx
    FigaroLT    uint64 // FK column; == LT on the IR log itself
    Payload     T
    Fingerprint string
}
```

**`LT` is the offset, and it is gapless and 1-based.** Append computes `next = LastIndex()+1` and stamps it (`internal/store/figwal_log.go:148`; `FileLog`/`MemLog` start `nextLT=1` and `++` per append, `file_log.go:120`, `mem_log.go:72`). There are no holes: the log is single-writer (the drain loop) and never reorders or rewrites. This is already a "strictly append-only list with a monotonic index" — the target model's substrate exists.

The payload IR (`internal/message/message.go:97`):

```go
type Message struct {
    Role        Role       // user | assistant | tool_result | system | system.interrupt
    Content     []Content  // heterogeneous blocks
    Patches     []Patch    // chalkboard mutations riding this tic
    Model       string
    Provider    string
    Usage       *Usage
    StopReason  StopReason
    LogicalTime uint64     // stamped from Entry.LT on read
    Timestamp   int64
}
```

Content blocks are already a discriminated union (`message.go:47`): `text`, `image`, `thinking`, `tool_invoke` (assistant-authored calls), `tool_result` (carried on the following user-role tic), `interrupt`. **A single message already holds a list of heterogeneous blocks** — the message-as-entry shape is the status quo.

One thing the prompt lists that is *not* in the IR today: **`signature` and `citations` are not content kinds.** `signature` exists only as an unused `nativeBlock.Signature` field in the Anthropic provider; `citations` is absent entirely. `thinking` is fully wired. §3 designs the slots for all of them, but flag: provider work to *populate* signature/citations lands later.

### 1.3 How a message is produced and indexed

`runTurn` (`internal/figaro/turn.go:118`) appends the user tic (gets its `LT`), then loops `driveOneRound` (`turn.go:233`):

1. Build `provider.SendInput{AriaID, FigLog, Snapshot, Tools, MaxTokens}` and call `prov.Send(ctx, in, bus)` in a goroutine.
2. The provider streams native SSE, accumulates a whole assistant `nativeMessage` **in place**, then **on `message_stop`** decodes it and `FigLog.Append`s it — **this is the only moment the assistant message gets an `LT`.** Until then the tail is *not in the log*; it lives only as a sequence of wire deltas.
3. Tool calls dispatch (speculatively at `content_block_stop`, `turn.go:455`); results are appended as a **separate** user-role message with `tool_result` blocks (`turn.go:397`). So in the IR, `tool_invoke` and `tool_result` are in *different* entries with consecutive `LT`s.
4. Repeat until no tool calls / interrupt / error.

**Consequence:** the durable log only ever contains *sealed* messages. The streaming tail is ephemeral, has no offset, and is reified atomically at `message_stop`. "The tail may be incomplete and still streaming" is currently *not representable* in the log — this is the core gap the redesign closes.

### 1.4 The live wire (lifecycle events)

Every socket connection auto-subscribes (`internal/figaro/protocol.go:52`, `a.Subscribe(srv)`); `fanOut` pushes notifications to all subscribers (`agent.go:478`). The notifications mirror Anthropic's Messages lifecycle (`internal/rpc/methods.go:8`, params in `rpc/rpc.go`):

| Notification | Params | Role |
|---|---|---|
| `stream.delta` | `{text, content_type}` | token delta — **no index, no block id** |
| `stream.thinking` | `{text}` | thinking delta |
| `stream.tool_invoke_start` | `{tool_call_id, tool_name}` | block opens |
| `stream.tool_invoke_delta` | `{tool_call_id, partial_json}` | tool-arg JSON fragment |
| `stream.tool_invoke_ready` | `{tool_call_id, tool_name, arguments}` | args fully decoded |
| `stream.tool_start` / `tool_output` / `tool_end` | `{tool_call_id, …, chunk/result}` | **execution** lifecycle (separate from authoring) |
| `stream.message_end` | `{stop_reason}` | pre-seal metadata |
| `stream.message` | `{logical_time, message}` | **full message re-sent** |
| `stream.done` | `{reason}` | turn idle |
| `stream.error` | `{message}` | error |

Wire ordering is enforced in the drain loop: `stream.message` (the full assembled `Message` with its `LT`) is fanned out only after all deltas/notifs of the turn drain (`turn.go:284` `drainPreFigOnce`, `fanOutFigaro:611`).

The consumer folds deltas purely by **arrival order**: `stream.delta` → `pace.Push(text)`, reset on `stream.message` (`internal/cli/stream_render.go:445,462,293`). There is no offset or block id to fold by.

### 1.5 Catch-up (a *separate*, pull-based path)

History is read two ways, neither tied to the live stream:

- **`figaro.context`** (figaro socket, `server.go:51`) → full dump of every message, no windowing.
- **`aria.read`** (angelus socket, `internal/angelus/protocol.go:543`): windowed by `{figaro_id, from, limit}` → `{entries:[{lt,payload}], total, next_from}`. `from` is **inclusive on `LT`**; `next_from` paginates (`rpc/methods.go:240`). Served through the shared `LogCache` (`internal/store/cache.go:28`) so cross-process reads run lock-free against the live agent's writes. Hard cap 1000.

`aria.read` is already a catch-up-by-index read — the from-index primitive exists. **But it is disjoint from the live stream.**

### 1.6 The five problems to fix

1. **Deltas aren't addressed.** `stream.delta` carries `{text, content_type}` only — the client cannot fold a delta into an identified entry/block; it relies on arrival order.
2. **The seal re-sends the whole message.** `stream.message` ships the full `Message` after streaming the same text as deltas (the "naive last-write-by-id" the prompt wants gone).
3. **Catch-up and live are unsynchronized.** `aria.read`/`context` (pull) vs. socket subscribe (push) have no shared cursor and no dedupe key → a boundary race (miss messages, or duplicate them with no `LT` to dedupe on).
4. **The streaming tail has no offset.** It can't be addressed, resumed, or folded until it's sealed into the log.
5. **Tool-execution output is a parallel ad-hoc lifecycle** (`stream.tool_*`) rather than unified with content deltas.

---

## 2. Proposed spec — the resumable message-log stream

### 2.0 Sequence-numbering scheme

- **`index` (`uint64`)** — the durable offset. **It is exactly today's `LT`.** Gapless, 1-based, single-writer, assigned at `Append`. One `index` per `message.Message`. Subscriptions, replay, and resume are all keyed on it. No new counter on disk.
- **`block` (`uint64`)** — 0-based ordinal of a content block within its message. Stable for the message's lifetime.
- **`seq` (`uint64`)** — per-`index` monotonic delta counter (resets at each new tail), for **live ordering/dedupe within a tail only**. It is *not* a durable cursor (see §2.5 / §4: resume keys on `index` alone).

The provisional `index` of an in-flight (not-yet-sealed) tail is **deterministic**: because the log is gapless and single-writer, the assistant tail's index is `figLog.PeekTail().LT + 1` at the moment streaming starts, and a tool_result tail's index is the assistant index + 1. (Alternative: add `Log.Reserve() uint64` to hand out the slot explicitly — see §4.3. Recommendation: compute from `PeekTail` first; it needs no interface change and the single-writer invariant guarantees it.)

### 2.1 Submit (`figaro.qua`) — unchanged shape, one addition

Keep the existing submit call (`rpc/methods.go:78`):

```go
type QuaRequest struct {
    Text       string           `json:"text"`
    Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"`
}
```

**Addition:** return the index of the user tic it appended, so a client can subscribe precisely from its own message:

```go
type QuaResponse struct {
    OK    bool   `json:"ok"`
    Index uint64 `json:"index"` // LT of the appended user message
}
```

This is async: `qua` returns as soon as the user tic is logged; the assistant response arrives as frames on a subscription.

### 2.2 Subscribe (`figaro.subscribe`) — the catch-up→live primitive

New method on the figaro socket. Replaces today's implicit auto-subscribe with an explicit from-index handshake.

```go
type SubscribeRequest struct {
    From uint64 `json:"from"` // inclusive; 0 == from the beginning
}

type SubscribeResponse struct {
    Tail  uint64 `json:"tail"`  // highest sealed index at subscribe time
    Live  bool   `json:"live"`  // true if a tail is currently streaming
}
```

**Semantics (catch-up subscription, gap-free, dupe-free):**

1. Under the agent lock (`a.mu`), atomically: register the subscriber **and** snapshot `tail = PeekTail().LT`. Frames produced after this instant are buffered for the new subscriber.
2. Reply `SubscribeResponse{Tail, Live}`.
3. Replay sealed entries `[From, Tail]` as `log.entry` frames (full messages — no delta replay needed; the sealed entry is the whole truth).
4. If a tail is mid-stream (`index == Tail+1` exists provisionally), emit its `log.anchor` + every `log.delta`/`log.block_*` accumulated so far.
5. Flush the buffer from step 1, **discarding any frame whose `index <= Tail`** (those were covered by replay). From here, frames flow live.

Because replay is by `index` and the cutover discards `index <= Tail`, there is exactly one copy of every entry and no gap at the boundary. This is the "subscribe → snapshot → replay → drain buffer" pattern, done under one lock so nothing slips between catch-up and live. (Contrast today: `aria.read` + socket subscribe with no shared cursor.)

Frames arrive as JSON-RPC **notifications** on the same connection (as the `stream.*` events do today). The subscribe *request* only returns the ack.

### 2.3 The frame vocabulary

Six log frames + two control frames replace the eleven `stream.*` notifications. Every frame that touches the log carries `index`.

```go
// --- log frames (the append-only log on the wire) ---

// log.entry — a sealed, immutable message at `index`. Used for replay
// of history AND for messages that were never streamed (user tics,
// tool_result tics that completed instantly, interrupt sentinels,
// system summaries).
type EntryParams struct {
    Index   uint64          `json:"index"`
    Message message.Message `json:"message"`
}

// log.anchor — opens a streaming tail. Establishes identity before any
// content arrives. The message is incomplete until log.seal.
type AnchorParams struct {
    Index     uint64       `json:"index"`
    MessageID string       `json:"message_id"` // stable id for this tail
    Role      message.Role `json:"role"`       // assistant | tool_result
}

// log.block_open — opens a block within the tail. The discriminator
// `kind` tells the client which UI to allocate, BEFORE any delta.
type BlockOpenParams struct {
    Index      uint64              `json:"index"`
    Block      uint64              `json:"block"`
    Kind       message.ContentType `json:"kind"`
    ToolCallID string              `json:"tool_call_id,omitempty"` // tool_invoke / tool_result
    ToolName   string              `json:"tool_name,omitempty"`
}

// log.delta — appends to an open block. Kind-tagged payload; exactly
// one of the payload fields is set, selected by `kind`.
type DeltaParams struct {
    Index uint64              `json:"index"`
    Block uint64              `json:"block"`
    Seq   uint64              `json:"seq"`
    Kind  message.ContentType `json:"kind"`

    Text        string          `json:"text,omitempty"`         // text, thinking
    PartialJSON string          `json:"partial_json,omitempty"` // tool_invoke args
    Chunk       string          `json:"chunk,omitempty"`        // tool_result exec output
    Signature   string          `json:"signature,omitempty"`    // signature
    Citation    json.RawMessage `json:"citation,omitempty"`     // citations (one appended)
}

// log.block_close — seals a block. Optional `final` carries the
// canonical finalized block (parsed tool args, full signature) so the
// client never has to parse accumulated fragments itself.
type BlockCloseParams struct {
    Index uint64           `json:"index"`
    Block uint64           `json:"block"`
    Final *message.Content `json:"final,omitempty"`
}

// log.seal — seals the tail message. After this, `index` is immutable
// and equals the log.entry a future replay would emit. Carries the
// terminal metadata that lives only on the IR Message.
type SealParams struct {
    Index      uint64             `json:"index"`
    StopReason message.StopReason `json:"stop_reason"`
    Model      string             `json:"model,omitempty"`
    Usage      *message.Usage     `json:"usage,omitempty"`
}

// --- control frames (NOT log entries) ---

// log.abort — retracts a provisional, never-sealed tail. The index is
// returned to the pool (the next message reuses it). Used on interrupt
// or provider error before a seal.
type AbortParams struct {
    Index  uint64 `json:"index"`
    Reason string `json:"reason"` // user_interrupt | fault | agent_exit
}

// log.done — the agent is idle (turn boundary). Pure control signal.
type DoneParams struct {
    Reason string `json:"reason"`
}
```

Method constants:

```go
const (
    MethodLogEntry      = "log.entry"
    MethodLogAnchor     = "log.anchor"
    MethodLogBlockOpen  = "log.block_open"
    MethodLogDelta      = "log.delta"
    MethodLogBlockClose = "log.block_close"
    MethodLogSeal       = "log.seal"
    MethodLogAbort      = "log.abort"
    MethodLogDone       = "log.done"
)
```

### 2.4 The lifecycle of one streamed message

A streaming assistant message at `index = N` with text then a tool call:

```
log.anchor      {index:N, message_id:"…", role:"assistant"}
log.block_open  {index:N, block:0, kind:"text"}
log.delta       {index:N, block:0, seq:0, kind:"text", text:"Let me "}
log.delta       {index:N, block:0, seq:1, kind:"text", text:"check."}
log.block_close {index:N, block:0}
log.block_open  {index:N, block:1, kind:"tool_invoke", tool_call_id:"tu_1", tool_name:"bash"}
log.delta       {index:N, block:1, seq:2, kind:"tool_invoke", partial_json:"{\"cmd\":\"l"}
log.delta       {index:N, block:1, seq:3, kind:"tool_invoke", partial_json:"s\"}"}
log.block_close {index:N, block:1, final:{type:"tool_invoke", tool_call_id:"tu_1", tool_name:"bash", arguments:{cmd:"ls"}}}
log.seal        {index:N, stop_reason:"tool_invoke", model:"…", usage:{…}}
```

A message that is *not* streamed (user tic, an interrupt sentinel, a tool_result that completed before any output) arrives as a single `log.entry{index, message}` — no anchor/seal dance.

### 2.5 Delta-compression & the durability rule (why resume is trivial)

**The canonical log is the sequence of sealed entries.** `anchor`/`block_*`/`delta` frames are a *transport optimization* for the live tail; they are **never required to be persisted**, because the sealed entry is the complete truth and is always reconstructable.

This yields the whole resume story for free:

- A subscriber joining at `from <= Tail` receives **full `log.entry` frames** up to `Tail` — no deltas to replay, no fragments to reassemble.
- For an in-flight tail it receives `anchor` + accumulated deltas (best-effort) and then live deltas.
- **The resume cursor is `index` alone.** `seq` orders deltas within a live connection but is not needed to resume: losing mid-tail deltas just means the server re-anchors and re-streams that one tail. A client that reconnects `from = N` (an open tail's index) discards its partial render of `N` and rebuilds it from the fresh anchor. Simple, and it makes the HTTP lift cheap (§4.2).

This is the "snapshot/anchor → N deltas folded in → seal" model the prompt describes, made concrete: anchor establishes `(index, message_id)`, deltas fold by `(index, block)`, seal marks final. Earlier indices are immutable. The tail is a single-writer, totally-ordered, monotonically-growing register keyed by `index` — **no CRDT, no merge.** Callers cannot fork history (the IR log has one writer, the drain loop), so there is no conflict resolution by construction.

---

## 3. Heterogeneous content, head-on

### 3.1 The decision: message-as-entry, blocks as sub-addresses

**Each *message* is a log entry; each block is addressed by `(index, block)` within it. Blocks are not log entries.**

Why message-as-entry:

- **`index` stays == `LT`.** No on-disk format change, no new offset space. The append-only log already *is* message-granular (`store.Log[message.Message]`).
- **The translator cache is per-message.** `Provider.Encode` is per-message and the per-message wire bytes are written exactly once (`ARCHITECTURE.md` "Cache prefix"; invariants #2, #4, #5). Block-as-entry would shatter this: the cache key, the byte-stable prefix, and Anthropic `cache_control` placement all assume message granularity. Breaking it is a direct hit to invariant #5 (cache-prefix byte-stability).
- **The provider already produces whole messages.** `drainSSE` accumulates a `nativeMessage` and `decodeNativeMessage` emits one `Message` with `[]Content`; tool dispatch reconciles against that assembled message (`turn.go:361`). Message-as-entry matches the existing decode/assemble boundary.
- **Block-granular *resume* is recoverable without block-granular *entries*.** `(index, block, seq)` on the wire gives the client per-block folding and per-block resume of a live tail, while the durable offset stays per-message.

Why **not** block-as-entry (the alternative, rejected):

- Pros: finer durable resume granularity; each block has its own offset so delta addressing is a flat `(offset, seq)`.
- Cons that kill it: fragments the `LT` space (a 4-block message becomes 4 offsets) → every `aria.read`/`figaro.context`/translator consumer must re-aggregate blocks into messages before sending to a provider; breaks per-message `Encode`/cache-prefix stability (#5); forces an on-disk migration (forward-only refactor rule); and makes "a message" a derived view rather than a stored unit. The only real win (durable per-block resume) isn't needed — §2.5 shows resume keys on `index` and re-streams the tail.

### 3.2 Kinds and how each delta accumulates

The discriminator is `kind` (a `message.ContentType`) on `log.block_open` and `log.delta`. The client dispatches on it at `block_open` (allocate the right UI) and folds `log.delta` by the same kind.

| Kind | block_open header | delta field | accumulation | block_close `final` |
|---|---|---|---|---|
| `text` | — | `text` | concatenate | (text in entry) |
| `thinking` | — | `text` | concatenate | (thinking in entry) |
| `tool_invoke` | `tool_call_id`, `tool_name` | `partial_json` | concatenate JSON fragments | `Content{tool_invoke, arguments}` (parsed once) |
| `tool_result` | `tool_call_id`, `tool_name` | `chunk` | concatenate exec output | `Content{tool_result, text, is_error}` |
| `signature` | — | `signature` | concatenate (usually one) | `Content{signature, …}` |
| `citations` | — | `citation` (one JSON obj) | **append to a list** | `Content{citations:[…]}` |
| `image` | — | (none; arrives whole) | n/a | single `log.entry`, no streaming |
| `interrupt` | — | (none) | n/a | single `log.entry` |

Notes:
- `text` / `thinking` / `tool_invoke` map 1:1 onto the existing Anthropic SSE deltas the provider already handles (`text_delta`, `thinking_delta`, `input_json_delta`). The provider's existing `Bus.PushDelta`/`PushToolInvokeDelta`/`PushToolReady` (`internal/provider/provider.go:37`) become `log.delta`/`log.block_close` emitters — small adapter, no new SSE parsing.
- `signature` / `citations` are **forward slots**: define them now so the wire is closed over the full union, but the hand-rolled Anthropic provider doesn't emit them yet (signature is an unused `nativeBlock` field; citations absent). Adding `ContentSignature`/`ContentCitations` to `message.ContentType` and the provider's `foldSSEEvent` is the follow-up that fills them. `citations` is the one kind whose delta is **list-append** rather than string-concatenate — the client pushes each `citation` object into an array.

### 3.3 The tool_use → tool_result → next-block flow

This is where unifying execution output onto the frame vocabulary pays off. Walk the round:

1. **Assistant entry `N`** streams as in §2.4 and `seal{N, stop_reason:"tool_invoke"}`. Its `tool_invoke` blocks close with parsed `arguments`.
2. **Tool execution = the tool_result tail at index `N+1`.** The agent opens `anchor{N+1, role:"tool_result"}`; for each tool it opens `block_open{N+1, block:k, kind:"tool_result", tool_call_id}`, streams the tool's stdout as `delta{N+1, block:k, kind:"tool_result", chunk:…}`, and closes with `block_close{N+1, block:k, final:{tool_result, text, is_error}}`. When all tools finish, `seal{N+1, stop_reason:"stop"}` — which *is* the existing `resultTic` Append (`turn.go:397`).
   - **Parallel tools interleave naturally:** speculative dispatch runs tools concurrently (`turn.go:455`); their `chunk` deltas interleave on the wire but are disambiguated by `block`. The client renders each tool's pane independently.
   - This folds today's `stream.tool_start`/`tool_output`/`tool_end` (`rpc/methods.go:15-17`) into `block_open`/`delta`/`block_close`. One vocabulary, not two.
3. **"The host then may open another block" = the next assistant turn = entry `N+2`**, a fresh `anchor`. Sequential by index, totally ordered.

So text generation, thinking, tool-arg authoring, and tool execution output all ride the same five frames, discriminated by `kind`. The client has one fold loop, dispatching on `kind`.

### 3.4 Edge cases (enumerated)

- **Block sealed mid-stream.** `block_close{index, block}` arrives while later blocks of the same message still stream. The client marks *that block* final; the *message* stays open until `seal`. (Exactly `content_block_stop` before `message_stop`.)
- **Multiple blocks per message.** Addressed by `block` ordinal, ordered ascending. A message with `[text, tool_invoke, tool_invoke]` is one entry, three blocks.
- **Ordering guarantees.** Within a block, deltas are ordered by `seq`. Within a message, blocks open/close in ascending `block`. Across messages, **seals are strictly index-ordered** (single-writer Append: `seal(N)` always precedes `seal(N+1)`). Live deltas of *different* in-flight tails (e.g. assistant `N` still finishing while tool output for `N+1` starts under speculative dispatch) **may interleave** — that is allowed and safe because every frame is self-addressing and seals still land in order. A client that wants strict per-index rendering can buffer `N+1` frames until `seal(N)`; one that wants maximum liveness renders both panes immediately.
- **Cancellation (interrupt).** The in-flight tail was never sealed (today: `Send` returns `ctx.Err()`, nothing is appended — `turn.go:349`). Emit `log.abort{index:N, reason:"user_interrupt"}`; the client drops its provisional render of `N`, and the index returns to the pool (the next message reuses it). If the *prior* assistant turn left dangling `tool_invoke`s, the existing interrupt sentinel (`message.NewInterruptSentinel`, `message.go:157`) appends as a normal `log.entry` — it is a real durable message, no special frame.
- **Error.** Provider error mid-tail → `log.abort{index:N, reason:"fault"}` then `log.done{reason:"error: …"}`. Error is a control frame, not a log entry (matching today's `stream.error`). *Option:* if persisting failures is wanted, add an `error` content kind and seal a durable error entry instead of aborting — flagged, not chosen here.
- **Empty / instant messages.** A tool_result that completes with no streamed output, a user tic, a system summary, an interrupt sentinel → single `log.entry`, no anchor/seal.
- **Resume of an open tail.** Reconnect `from = N` where `N` is mid-stream: server replays sealed `[from, Tail]` as entries, then re-anchors `N` and re-streams accumulated deltas. Client rebuilds `N` from scratch. No dupes (entries `< N` are sealed and sent once; `N` is rebuilt).
- **Abort/seal race on resume.** If `N` aborts between the client's disconnect and reconnect, the reconnect simply never sees `N` (it was retracted; the slot is reused by a later message). The client's stale partial `N`, if any, is discarded on `log.abort` replay or on seeing a different message seal at `N`.

---

## 4. Implementation thoughts & the HTTP constraint

### 4.1 Per-conversation sequence state

The agent gains a small **tail tracker** owned by the drain loop (no new goroutines touching state — invariant #1):

- `provisionalIndex uint64` — set when a tail opens, `= figLog.PeekTail().LT + 1`; cleared on seal/abort.
- `openBlocks map[uint64]blockState` — per `block`: kind, accumulated bytes/list, `nextSeq`.
- The translator/turn code already emits the lifecycle internally via `turnBus` (`turn.go:25`); the change is to **re-shape what `fanOut` sends**, not to add a new event source. `Bus.PushDelta` → `log.delta`; `PushToolInvokeStart` → `block_open`; `PushToolReady`/`content_block_stop` → `block_close{final}`; `PushMessageEnd` + the eventual Append → `log.seal`; `fanOutFigaro` (`turn.go:611`, today's full-message re-send) is **deleted** — the seal carries only terminal metadata, not the body.

The provider interface (`provider.go:37`) barely moves: the `Bus` already speaks in blocks (`PushToolInvokeStart/Delta/Ready`). Mostly the *figaro-side* `turnBus` and `fanOut` translate bus calls into the new frames; the Anthropic SSE parser is untouched for text/thinking/tool_invoke.

### 4.2 Catch-up joins live with no gap/dupe

The single load-bearing mechanism is §2.2 step 1: **register-subscriber-and-snapshot-tail under one `a.mu` acquisition**, buffer concurrent frames, replay `[from, tail]`, then drain the buffer discarding `index <= tail`. Today `Subscribe` (`agent.go:268`) and the log read are separate; the change is to fuse them so the snapshot and the subscription are atomic. The shared `LogCache` (`cache.go:28`) already lets the replay read the same `Log` instance the drain loop writes, lock-free — so cross-process `aria.read`-style replay and in-process subscription see one consistent log.

`aria.read` (`angelus/protocol.go:543`) stays as the stateless, paginated, cross-process catch-up read (good for a cold client that then opens a subscription). Its `from`/`next_from` are already `LT`-keyed, so it shares the cursor space with `figaro.subscribe` — a client can `aria.read` to `next_from`, then `figaro.subscribe{from: next_from}` and the boundary is clean.

### 4.3 Idempotency & resumption

- **Resume = re-subscribe with `from`.** Idempotent: replay is a pure function of `(from, log)`; sealed entries are immutable, so re-reading them yields identical bytes.
- **`index` is the only durable cursor a client must persist.** Track the highest sealed `index` fully rendered; reconnect `from = that + 1` (or `= openTailIndex` to rebuild a partial). `seq` is connection-local; never persisted.
- **Provisional-index reuse on abort** is safe precisely because aborted tails were never sealed — there is no durable record to contradict the reused index.
- *Alternative considered:* `Log.Reserve() uint64` to allocate the tail's slot up front instead of computing `PeekTail()+1`. Cleaner if speculative cross-index interleaving (§3.4) makes provisional computation feel brittle, but it introduces a "reserved-but-unsealed" gap in the otherwise-gapless log and touches the `Log` interface. **Recommendation:** ship with `PeekTail()+1` (no interface change, single-writer makes it exact); reach for `Reserve()` only if a second writer ever appears (it shouldn't — invariant #1).

### 4.4 What not to paint into a corner for the later HTTP/SSE lift

Design nothing for HTTP now, but keep these true so the lift is cheap:

1. **Resume keys on `index` alone.** SSE's `Last-Event-ID` maps to "highest sealed `index` I have" (optionally `index` of an open tail to rebuild). Because deltas are reconstructable from the sealed entry (§2.5), the SSE server needs no per-delta durable id — set `id:` to the `index` on `log.entry`/`log.seal` frames and you have correct `Last-Event-ID` resume. **Do not** make correctness depend on replaying individual deltas.
2. **Frames are self-addressing and transport-agnostic.** Every frame carries `(index[, block, seq])` and no connection-scoped state — the same frame serializes onto an SSE `data:` line unchanged. **Do not** let the unix client infer anything from arrival order (today's sin); always fold by address.
3. **Keep `seq` connection-local and disposable.** Don't promote it to a durable global frame counter now — that would over-constrain the HTTP id scheme. If a global cursor is ever wanted, it can be derived later as `(index, seq)`; nothing here precludes it.
4. **One subscription endpoint shape.** `figaro.subscribe{from} → ack + frame stream` lifts directly to `GET /arias/{id}/stream?from=N` (SSE) with `Last-Event-ID` overriding `from`. Keep the ack (`{tail, live}`) cheap and separable from the frame stream so HTTP can put it in headers.

---

## Appendix: file/method index (for the collaborator)

- Submit / turn: `figaro.qua` → `internal/figaro/server.go:43`, `agent.go:224`; `runTurn` `turn.go:118`; `driveOneRound` `turn.go:233`.
- Log / index: `store.Log` `internal/store/log.go:22`; `Entry` `log.go:5`; gapless LT append `figwal_log.go:148`, `file_log.go:120`, `mem_log.go:72`.
- IR: `Message` `internal/message/message.go:97`; `Content`/`ContentType` `message.go:47,64`; interrupt sentinel `message.go:157`.
- Live wire (today): methods `internal/rpc/methods.go:8`; params `rpc/rpc.go`; `fanOut` `agent.go:478`; `Subscribe` `agent.go:268`; auto-subscribe `protocol.go:52`; full-message re-send `turn.go:611`; CLI fold-by-arrival `internal/cli/stream_render.go:445`.
- Catch-up (today): `aria.read` `internal/angelus/protocol.go:543`, req/resp `rpc/methods.go:240`; `figaro.context` `server.go:51`; `LogCache` `internal/store/cache.go:28`.
- Provider: `Bus`/`SendInput`/`Provider` `internal/provider/provider.go:37,63,72`; SSE accumulate/decode in `internal/provider/anthropic/anthropic.go` (`drainSSE`, `decodeNativeMessage`).
- Tool exec lifecycle (to be unified): `specDispatcher.dispatch` `turn.go:455`; `runTools` `turn.go:580`.
