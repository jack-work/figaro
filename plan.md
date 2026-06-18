# Figaro stream redesign — respec (tail-log + open-message model)

**Status:** design proposal, ready for implementation planning. Supersedes the earlier
`duck/2026-06-13-resumable-message-log-stream.md` proposal where they conflict (see §0.2).
**Scope:** the Unix-socket JSON-RPC streaming + read surface. HTTP/SSE is out of scope;
§8 lists the constraints to honor so the lift stays cheap, but designs nothing for it.
**Authority:** `figaro-ir-and-protocol-spec.md` (branch `docs/stream-log-redesign`) and the
Go source define current behavior. This document defines the *target*. Figaro is
forward-only; this is a rewrite of the streaming/read surface, not a compatibility shim.

---

## 0. The idea in one paragraph

A conversation ("aria") is an append-only log of `message.Message` entries, each at a
gapless 1-based index (`LT`). Everything a client consumes — catch-up history *and* live
streaming — is one primitive: **a paginated tail-log read that may stay open.** You give it
a starting index; you get back a batch of messages and the index to resume from; the
*last* message in a live read may be **open** (not yet sealed) and may receive updates.
Closed messages are immutable and rendered once. The open message is mutable: the server
may patch it (delta-compressed) or replace it wholesale, and the client just rerenders that
one message. **What travels on the wire is the serialized Figaro IR** — the same
`message.Message` shape persisted to and loaded from disk — with a thin envelope marking
the open tail and (in delta mode) a small patch frame. There is no separate streaming
vocabulary to reassemble.

## 0.1 Why this shape

- **One abstraction, not two.** Today catch-up (`aria.read`, pull) and live (`stream.*`
  notifications, push) are disjoint paths with no shared cursor — a boundary race. Here
  they are the same call; live is just "the read didn't end."
- **The client's job shrinks to rendering the tail.** Sealed messages never change, so the
  client only ever rerenders the single open message. It does not run a block-lifecycle
  state machine; it holds a `Message` and applies ops to it, and if it ever loses the
  thread it re-reads that one message whole.
- **Wire == disk for everything sealed.** Closed frames are the bare IR `Message`. The
  only streaming-only concepts (open flag, version, patch ops) are confined to the
  unsealed tail.
- **The HTTP/SSE lift is trivial** because the SSE stream is literally the
  never-terminating version of the GET (§8).

## 0.2 What changes vs. the current spec and the prior proposal

| Decision | Prior `duck/…` proposal | **This respec** |
|---|---|---|
| Streaming primitive | `anchor/block_open/delta/block_close/seal` frame choreography | bare IR `Message` frames + an open-tail envelope + (delta mode) a patch frame |
| Catch-up vs live | `figaro.subscribe{from}` fuses them | one tail-read API in two flavors (turn-scoped, fan-out); §3 |
| Open-message index on abort | return to pool / reuse | **burn** (next message takes next index); §1.3 |
| Block addressing | every delta carries `(index, block, seq)` | patch ops are block-addressed within the open message; sealed messages carry no streaming metadata; §5 |
| Tool execution events | unify onto frames | tool execution is just an **open `tool_result` message** that streams then seals; no `stream.tool_*`; §6 |

---

## 1. Foundations (unchanged substrate, restated for the implementer)

### 1.1 The log and the index

The canonical store is the per-aria IR write-ahead log, `store.Log[message.Message]`
(figwal segment dir, `aria/`). Each record is an `Entry[T]` whose `LT` is the durable
index: **uint64, 1-based, gapless, monotonic, single-writer** (the drain goroutine),
assigned at `Append`. One `LT` per `message.Message`. This is the only durable cursor in
the whole protocol. (`figaro-ir-and-protocol-spec.md` §3.7, §4.2, §4.3.)

### 1.2 The wire payload is the IR

A **closed** message on the wire is exactly `message.Message` (IR §3.4) — the same bytes
shape persisted to and loaded from the figwal log, with `logical_time` stamped from the
envelope `LT`. No transformation, no streaming-specific fields. A reader that can parse an
on-disk aria can parse a closed wire frame.

### 1.3 Burn-on-abort (load-bearing rule)

The open message lives at the provisional index `tail` and is patched in place, reusing
that index for the duration of streaming. **It is not durable until sealed.** If the turn
is interrupted / faults / the provider errors before the seal, the provisional index is
**burned**, exactly as figwal truncates a torn tail on recovery: the partial is discarded
and the *next* appended message takes the next index. The client MUST NOT treat the open
message's index as durable until it receives the seal (§4.4).

Note this composes with the existing interrupt sentinel (IR §3.6): a dangling-tool-call
reconciliation is a *real appended durable message* (`role:"system.interrupt"`), so it
occupies its own sealed index — it is not the burned provisional. Burning concerns only
the unsealed open attempt.

---

## 2. Frame types (what travels on the socket)

All frames are JSON-RPC 2.0 over the existing jkrpc NDJSON transport (spec §2). Reads
return a **response** to the read request; live updates arrive as **notifications** on the
same connection, sharing its FIFO ordering stream (spec §2.3). Three frame bodies:

### 2.1 Closed message frame

The bare IR `Message`, carried with its index. Used for every sealed entry — assistant
turns, user tics, tool-result tics, set/patch-only tics, interrupt sentinels, system
summaries. Render once; never updated.

```
type LogEntry struct {
    Index   uint64          `json:"index"`   // the durable LT
    Message message.Message `json:"message"` // bare IR, identical to disk
}
```

### 2.2 Open message frame

The same IR `Message`, carried in a thin envelope marking it unsealed and versioned. This
is the *current full state* of the open tail — sent on first appearance, and re-sent in
full whenever the server chooses full mode or the client re-requests it.

```
type OpenEntry struct {
    Index   uint64          `json:"index"`   // provisional; not durable until sealed
    Version uint64          `json:"version"` // per-open-message counter, from 0; resets each new open message
    Open    bool            `json:"open"`    // always true here
    Message message.Message `json:"message"` // current full IR state of the tail
}
```

`Version` is **connection-local sugar for gap detection only.** It is never persisted by
client or server and never appears on a closed frame. A new open message restarts it at 0.

### 2.3 Patch frame (delta mode only)

A delta against the open message at `(Index, Version-1) → Version`. Block-addressed ops
(§5). Only emitted to subscribers that opted into delta mode (§3.4). A client that detects
a version gap (`have v=5`, patch says `from=6`) discards and re-requests the open message
whole (§4.4).

```
type PatchEntry struct {
    Index   uint64     `json:"index"`
    Version uint64     `json:"version"`      // the version this patch PRODUCES
    From    uint64     `json:"from"`         // the version it applies to; == Version-1
    Ops     []BlockOp  `json:"ops"`          // §5
}
```

### 2.4 Seal signal

Sealing the open message is conveyed by the **next closed frame at that index**: the
client receives `LogEntry{Index: N, Message: …}` for the same `N` it was holding open,
which both finalizes the body and tells it the index is now durable. No separate "seal"
method is required — the closed frame *is* the seal. (Implementations MAY additionally
flip `open:false`; the normative signal is the arrival of the closed `LogEntry` at `N`.)

Abort is conveyed by a small control notification so a client can drop its provisional
render without waiting:

```
type AbortEntry struct {
    Index  uint64 `json:"index"`
    Reason string `json:"reason"`  // user_interrupt | fault | agent_exit
}
```

After an abort, index `N` is burned; the client drops its open render of `N`.

---

## 3. The read/stream API

Underneath everything is one operation: **read from an index, get a batch + a resume
cursor, optionally keep receiving.** Two named entry points wrap it for two use cases.

### 3.1 The underlying read semantics

- Input: a start index `from` (inclusive; `0`/absent = from the beginning), plus options
  (§3.5).
- Output: a sequence of `LogEntry` frames for sealed messages `[from .. tail]`, possibly
  followed by an `OpenEntry` if the tail is mid-stream.
- **Batching is the server's call.** The server MAY pack multiple `LogEntry` frames into
  one response/notification payload, or emit them one per frame, based on connection
  constraints and payload-size limits. The client treats the result as a stream of
  messages regardless of how they were packed. (Concretely: a read response carries
  `entries: []LogEntry`; a live connection then emits further `entries` notifications.)
- **End-of-stream is both explicit and naturally discoverable.** Each batch reports the
  next index to resume from; a `live` flag indicates whether the tail is the current head.
  A pull client that reads past the end gets an empty batch with `next_from == from`,
  which also means "caught up."

Read response shape:

```
type ReadResponse struct {
    Entries  []LogEntry  `json:"entries"`            // sealed messages, ascending by index
    Open     *OpenEntry  `json:"open,omitempty"`     // present iff the tail is mid-stream and in range
    NextFrom uint64      `json:"next_from"`           // resume cursor; == tail+1 when fully caught up
    Tail     uint64      `json:"tail"`                // highest sealed index at read time
    Live     bool        `json:"live"`                // true iff a turn is currently streaming
}
```

### 3.2 Turn-scoped API (what the current CLI uses)

Send a prompt, watch exactly that turn print to the console, stop. This is `figaro.qua`
extended to return the index it appended, plus the existing per-connection live stream
scoped to one turn.

```
// figaro.qua  (request)
type QuaRequest struct {
    Text       string           `json:"text"`
    Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"`
    DeltaMode  bool             `json:"delta_mode,omitempty"` // §3.4
}
// figaro.qua  (response)
type QuaResponse struct {
    OK    bool   `json:"ok"`
    Index uint64 `json:"index"` // LT of the appended USER tic
}
```

Client flow:

1. `figaro.qua{text}` → `{index: U}` (the user tic's index).
2. Listen for frames with index `> U`: the assistant open message appears at some `N`
   (`N == U+1` in the simple case), streams as `OpenEntry`/`PatchEntry`, then arrives as a
   closed `LogEntry{N}` (seal). If the turn calls tools, further open/closed messages at
   `N+1, N+2, …` follow (§6).
3. Stop listening when the turn goes idle — signaled by a `turn.done` notification
   (replaces today's `stream.done`):

```
type DoneEntry struct {
    Reason string `json:"reason"`  // stop reason, or an error string
}
```

The turn-scoped consumer renders only the live tail to the console and exits on
`turn.done`. It does not catch up history; it starts from its own just-appended index.

### 3.3 Fan-out / catch-up API (new)

Read the log from any index, for any consumer (a TUI showing scrollback, a second client,
a monitor). Decoupled from sending. This is the generalization of today's `aria.read`,
served through the shared `LogCache` so it is consistent with the live agent's writes.

```
// figaro.read  (request)  — on the figaro socket; also mirrorable on angelus (§3.6)
type ReadRequest struct {
    From      uint64 `json:"from,omitempty"`   // inclusive; 0 = beginning
    Last      uint64 `json:"last,omitempty"`   // relative: the last N messages (overrides From if set)
    Limit     uint64 `json:"limit,omitempty"`  // max sealed messages this batch; 0 = server max (cap 1000)
    Follow    bool   `json:"follow,omitempty"` // keep the connection open and stream new entries live
    DeltaMode bool   `json:"delta_mode,omitempty"` // §3.4; only meaningful with Follow
}
```

- `Follow:false` → a single `ReadResponse` (pull/snapshot). The client pages by re-calling
  with `From: NextFrom`. Reading past the end returns empty + caught-up.
- `Follow:true` → an initial `ReadResponse` for the catch-up window, then `entries` /
  `open` / `patch` / `LogEntry`(seal) / `turn.done` notifications continue live on the
  connection. The catch-up→live cutover is atomic (§7.2): no gap, no dupe.
- `Last:3` → "show me the last three messages" = `From = max(1, tail-2)`. With `Follow`,
  this is "tail the last 3 then keep going."

### 3.4 Delta mode is a per-subscription opt-in

Whether the open message arrives as full `OpenEntry` re-sends or as `PatchEntry` deltas is
chosen by the **consumer**, on its `qua`/`read` call (`delta_mode`). Default is **full**
(simplest correct client: replace the open message wholesale every update; never parse a
patch). Delta mode is the bandwidth optimization for clients that implement op-folding
(§5) and version-gap recovery (§4.4). The server MAY at any time send a full `OpenEntry`
even to a delta-mode client (e.g. on a wholesale block replacement, or to resync) — a
full open frame is always a valid update in either mode.

### 3.5 Options summary

| Option | Meaning |
|---|---|
| `from` | inclusive start index; 0 = beginning |
| `last` | relative window: last N messages (sets `from = tail-N+1`) |
| `limit` | max sealed messages per batch; server caps at 1000 |
| `follow` | keep open and stream live after catch-up |
| `delta_mode` | open tail arrives as patches rather than full re-sends |

### 3.6 Socket placement

`figaro.read` lives on the **figaro socket** (per-aria). The existing `aria.read` on the
**angelus socket** stays as the stateless cross-process snapshot read (cold clients, the
`l` CLI listing). Both key on `LT`, so a client may `aria.read` to `next_from` then
`figaro.read{from: next_from, follow:true}` and the boundary is clean — same cursor space.

---

## 4. Client rendering model

### 4.1 The invariant the client relies on

Indices below the open message are sealed and immutable. The client renders each closed
message exactly once and never touches it again. At most one message — the tail — is open
and rerendered on update. Therefore the client holds: a list of rendered sealed messages,
plus at most one open `Message` + its `version`.

### 4.2 Applying updates (full mode)

On `OpenEntry{Index:N, Version:v, Message:M}`: if `N` is the current open index, replace
the held open message with `M`, set version `v`, rerender that one message. If `N` is new
(greater than any seen), begin a new open message. On closed `LogEntry{N}`: finalize —
move `N` into the sealed list, clear the open slot, rerender once as sealed.

### 4.3 Applying updates (delta mode)

On `PatchEntry{Index:N, Version:v, From:f, Ops}`: if `f == heldVersion`, apply ops (§5) to
the held open message, set version `v`, rerender. If `f != heldVersion` (gap), **discard
and recover** (§4.4). On a full `OpenEntry` in delta mode: replace wholesale (always valid;
resets the version baseline).

### 4.4 Recovery (the escape hatch)

Guaranteed-receipt, in-order delivery is the contract (jkrpc FIFO per direction, spec
§2.3). The client's only failure mode is a detected version gap or a reconnect. Recovery is
always the same: **re-request the open message whole** — `figaro.read{from:N, limit:1}`
(or reconnect with `follow`) — which returns the current full `OpenEntry{N}`, and resume.
Because the open message is fully reconstructable from the server's live state and, once
sealed, from the durable log, there is never anything to reassemble from lost deltas. The
durable cursor the client persists across reconnects is `LT` alone; `version` is discarded.

### 4.5 Server owns the markup; client owns the render

The open `Message` is fully server-defined: its `Content` blocks, their kinds, their order,
any finalized parsed fields. The client renders whatever the latest `Message` says into
whatever UI component it has (a terminal pane today; a DOM node later). The client does not
interpret semantics beyond dispatching on `Content.Type` to pick a renderer. The server may
restructure the open message arbitrarily between versions (append, replace a block, replace
the whole message) — the client just rerenders.

---

## 5. Patch-on-structured-message (delta mode ops)

A message is `[]Content` (IR §3.2), not a string, so a patch is a small list of
**block-addressed ops**. Block address is the ordinal position in `Message.Content`
(0-based), stable for the open message's lifetime.

```
type BlockOp struct {
    Op    string           `json:"op"`              // append | open | replace | close
    Block uint64           `json:"block"`           // index into Message.Content

    // op=append: extend the addressed block's body
    Text  string           `json:"text,omitempty"`         // for text/thinking/tool_result bodies
    JSON  string           `json:"partial_json,omitempty"` // for tool_invoke argument accumulation

    // op=open: a new block appeared at Block (header only; body fills via append)
    // op=replace: swap the whole block (e.g. finalized parsed tool args, or server restructure)
    Content *message.Content `json:"content,omitempty"`    // full block for open/replace

    // op=close: this block won't change again (message may still be open)
}
```

### 5.1 Op semantics

| Op | Effect | Common trigger |
|---|---|---|
| `open` | insert `Content` at position `Block` (a freshly-opened block, body may be empty) | model starts a new text/thinking/tool_invoke block |
| `append` | concatenate `text` (or `partial_json`) onto block `Block`'s body | the 99% path: text growing token-by-token; tool args streaming |
| `replace` | overwrite block `Block` with `Content` | finalized parsed `arguments` land; server restructures a block |
| `close` | mark block `Block` final (no more appends); message stays open | provider `content_block_stop` before `message_stop` |

`append` is the prefix-compression win: the server sends only the new suffix, not the whole
growing block. `replace` and a full `OpenEntry` are always available, which is what gives
the server total freedom to rewrite the open message. Last-write-wins on the whole open
message; ops are merely the compressed path to the same end state.

### 5.2 Mapping IR content kinds onto ops

| `Content.Type` | open carries | append field | finalized by |
|---|---|---|---|
| `text` | `{type:text}` | `text` (concatenate) | the seal (closed frame) |
| `thinking` | `{type:thinking}` | `text` (concatenate) | the seal |
| `tool_invoke` | `{type:tool_invoke, tool_call_id, tool_name}` | `partial_json` (concatenate) | `replace` with decoded `arguments`, or the seal |
| `tool_result` | `{type:tool_result, tool_call_id, tool_name}` | `text` (concatenate exec output) | the seal of the tool_result message |
| `image` | full block via `open`/`replace` | — (arrives whole) | n/a |
| `interrupt` | — (sentinels are sealed messages, never streamed) | — | n/a |

`signature` and `citations` from the original Anthropic union are **not IR content kinds
today** (IR §3.2 has no such types). Leave the op vocabulary closed over the *existing* IR
union; adding those kinds is a separate IR + provider change, and the op machinery
(`open`/`append`/`replace`/`close`) already covers them when they land (citations would
use `replace`/`open` per citation rather than string-append). **Do not invent wire kinds
the IR can't represent.**

---

## 6. Tool execution as an open message

This replaces today's parallel `stream.tool_start` / `stream.tool_output` / `stream.tool_end`
vocabulary. There is no separate tool-execution lifecycle on the wire.

Per IR §3.5, `tool_invoke` (assistant message) and `tool_result` (next tic) live in
**adjacent entries with consecutive indices**, never the same message. So a round is:

1. **Assistant message at `N`** streams as an open message (text/thinking/tool_invoke
   blocks), then seals as closed `LogEntry{N}` with `stop_reason:"tool_invoke"`. Its
   `tool_invoke` blocks finalize with decoded `arguments` (via `replace` or at seal).
2. **Tool execution = the open `tool_result` message at `N+1`.** The agent opens it as an
   `OpenEntry{N+1, role:"tool_result"}`; for each tool a `tool_result` block opens
   (`open`) and the tool's stdout streams in as `append{text: chunk}`; when all tools
   finish the message seals as closed `LogEntry{N+1}`. Parallel/speculative tools are
   distinct blocks in the same open message; their `append` ops interleave on the wire,
   disambiguated by `block`. (This preserves IR §3.5's "one `tool_result` block per call,
   matched by `tool_call_id`.")
3. **The next assistant turn is `N+2`**, a fresh open message. Sequential by index.

The client renders the live tool output by rerendering the open `tool_result` message,
same as any other open message. **Open question for the implementer to confirm with the
maintainer, defaulting to "stream live":** whether tool stdout streams as an open
`tool_result` message (live) or arrives only as the sealed `LogEntry{N+1}` (atomic). This
respec assumes live; if atomic is wanted, the `tool_result` tic simply arrives as a closed
frame with no preceding open frames.

---

## 7. Implementation notes

### 7.1 Per-aria streaming state (owned by the drain loop)

No new goroutines touch shared state (preserve the single-drain-loop invariant). The agent
gains a small tail tracker, live only while a message streams:

- `openIndex uint64` — the provisional index, `= figLog.PeekTail().LT + 1` when a tail
  opens; cleared on seal or abort.
- `openVersion uint64` — bumped on each update; reset to 0 when a new open message begins.
- `openBlocks` — per-block accumulation state (kind, accumulated bytes) so the server can
  emit `append` suffixes (delta mode) or assemble the full `OpenEntry` (full mode).

The existing provider `Bus` (spec §8) already speaks in blocks
(`PushDelta`/`PushToolInvokeStart`/`PushToolInvokeDelta`/`PushToolReady`/`PushMessageEnd`/
`PushFigaro`). The change is on the **figaro side**: translate those bus calls into
`OpenEntry`/`PatchEntry` frames (per subscriber's mode) instead of today's `stream.*`
notifications, and delete the full-message re-send path (`fanOutFigaro` /
today's `stream.message`) in favor of the seal being the closed `LogEntry`. The Anthropic
SSE parser is untouched. `PushFigaro` (append to `FigLog`) is exactly the seal point.

### 7.2 Catch-up joins live with no gap/dupe (the one load-bearing mechanism)

For a `follow` read: under a single `a.mu` acquisition, atomically (a) snapshot
`tail = PeekTail().LT` and any in-flight open state, and (b) register the connection as a
live subscriber. Buffer frames produced after that instant. Then: emit the catch-up batch
`[from .. tail]` as `LogEntry`s (+ the open tail if any), then flush the buffer
**discarding any frame whose index `< from`-adjusted cutoff or `<= tail` that the batch
already covered.** Because catch-up is by index and the cutover discards already-sent
indices, every entry is delivered exactly once with no boundary gap. This fuses today's
separate `Subscribe` (`agent.go`) and log read into one atomic step. The shared `LogCache`
(spec §4.7) already lets the replay read the same figwal instance the drain loop writes,
lock-free.

### 7.3 Idempotency & resumption

- A `follow` read is a pure function of `(from, log state)` for its sealed portion; sealed
  entries are immutable, so re-reading yields identical bytes. Resume = reconnect with
  `from = highest fully-rendered sealed index + 1` (or `from = openIndex` to rebuild a
  partial tail, which returns the current full `OpenEntry`).
- `version` is connection-local and never persisted (§4.4).
- Burn-on-abort is safe precisely because the burned index was never sealed; no durable
  record contradicts the next message taking that index. (If a client missed the
  `AbortEntry`, it discovers the burn on reconnect: it asks `from=N` and either gets a
  *different* message now sealed at `N`, or nothing — either way it drops its stale
  partial.)

### 7.4 Suggested build order (smallest independently-verifiable steps)

1. **Frame types + full-mode open message + seal-as-closed-frame.** On the existing
   per-connection live path: replace `stream.delta`/`stream.message` with
   `OpenEntry` (full mode) + closed `LogEntry`. Client folds by index, not arrival order.
   Fixes the unaddressed-delta and whole-message-resend problems. No delta mode yet.
2. **`figaro.qua` returns the appended index**; turn-scoped CLI consumes `from index`,
   stops on `turn.done`.
3. **`figaro.read{from,last,limit,follow}`** with the atomic catch-up→live cutover (§7.2).
   This is the trickiest locking change; do it once full-mode framing is proven.
4. **Delta mode**: `PatchEntry` + block ops (§5) + version-gap recovery. Purely additive;
   full mode remains the default and the fallback.
5. **Tool execution as open `tool_result` message** (§6): delete `stream.tool_*`. Most
   mechanical; do last.

---

## 8. Do-not-paint-into-a-corner list for the later HTTP/SSE lift

Design nothing for HTTP now; keep these true:

1. **Resume keys on `LT` alone.** SSE `Last-Event-ID` maps to "highest sealed index I
   have." Set the SSE `id:` to the index on closed `LogEntry` / seal frames. Do **not**
   make correctness depend on replaying individual patches — they are reconstructable from
   the open state and, once sealed, from the log.
2. **Frames are self-addressing and transport-agnostic.** Every frame carries its
   `index` (and patches their `version`); none depend on connection-scoped arrival order.
   The same frame serializes onto an SSE `data:` line unchanged. The client MUST fold by
   `index`, never by arrival order.
3. **`version` stays connection-local and disposable.** Don't promote it to a durable
   global counter; that would over-constrain an HTTP id scheme.
4. **One read endpoint shape.** `figaro.read{from,…} → ReadResponse + frame stream` lifts
   directly to `GET /arias/{id}/log?from=N&follow=1` (SSE), with `Last-Event-ID`
   overriding `from`. Keep the `{tail, live, next_from}` header cheap and separable from
   the entry stream so HTTP can put it in response headers.

---

## Appendix: open questions for the maintainer (confirm before/while planning)

1. **Tool stdout: live or atomic?** §6 assumes the `tool_result` message streams live as an
   open message. Confirm, or switch to atomic (sealed `LogEntry` only).
2. **`turn.done` vs. natural discovery.** This respec keeps an explicit `turn.done`
   notification for the turn-scoped CLI's "stop now" signal. A pure fan-out follower can
   instead rely on `live:false` + caught-up. Confirm both signals should coexist.
3. **Batch framing of `entries`.** §3.1 lets the server pack N messages per payload at its
   discretion. Confirm there's no client that needs one-message-per-frame (the turn-scoped
   CLI does not; it renders whatever arrives).
4. **`signature`/`citations`.** Confirmed out of scope: not in the IR today; the op
   vocabulary already accommodates them when the IR gains them. No wire kinds ahead of the
   IR.
