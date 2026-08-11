# livelog

The live-render layer for Figaro: a turn-shaped, paginated aria protocol plus
terminal renderers. The packages are isolated from transport: the same
`aria.Page` value is returned by a pull and delivered by a push.

## The one conversation shape: `aria.Page`

A conversation is addressed in `(turn, node)` space. Turn ids are dense within
an aria; node ids are positional ordinals within a turn. Figaro logical time
(LT) remains provenance and the storage/channel join key, but it is not a UI
address.

```go
type Page struct {
    Parts   []TurnPart `json:"parts,omitempty"`
    More    More       `json:"more"`
    Metrics *Metrics   `json:"metrics,omitempty"`
}
```

A page is a contiguous node window in reading order:

- `TurnPart.Turn` carries `turn`, `inquiry`, `at`, `lts`, `sealed`, `nodes`,
  and optionally `live`;
- `TurnPart.From` is the ordinal of `Nodes[0]`;
- `ClippedHead` / `ClippedTail` say this page omitted part of the turn; and
- `More.Before` / `More.After` say nodes exist outside the whole page.

`Inquiry` is text on the turn, not a node. Every part states it so a client
joining after the opening frame still knows the question. Nodes are `prose`,
`thinking`, `tool`, or `steering`; a tool node folds an invocation and its
later result into one UI element.

Current `livedoc.Node` snapshots may contain a legacy string `id`. It is source
or provider metadata, not identity. The only UI identity is `(turn, From+i)`;
live deltas therefore address nodes with an explicit positional `uint64` id.

## Pull and push

The same `Page` travels two ways:

- `figaro.read` pulls initial state, forward catch-up, or backward history;
- `figaro.aria` pushes subsequent changes on the aria socket.

An aria socket connection is automatically subscribed when accepted. There is
no subscribe RPC: connect, call `figaro.read` on that connection, then consume
notifications. The read may overlap live pushes; `Client.Apply` is idempotent.

The request still has legacy LT-era names:

- forward: `{"sinceLT": <turn>, "limit": <byte-budget>}`;
- backward: `{"before": <turn>, "before_node": <node>, "limit": <byte-budget>}`.

`limit` is measured in serialized node bytes. The server uses its configured
default for a non-positive value, clamps it to `wire.page_budget_max`, never
splits a node, and always returns at least one node when anything is available.
Callers use a turn beyond the tail for the initial backward page.

## The mutable suffix

Only the newest turn can move, and only a suffix of that turn can move.
`Live.From` is the boundary: ordinals below it are immutable; ordinals at or
above it may receive `NodeDelta`s. Each frame has a monotonically increasing
record version `Live.V`.

A delta has three independent operations:

- `set`: merge scalar/full-string fields, creating the node when `type` first
  appears;
- `unset`: remove fields; and
- `patch`: splice `markdown` or `output` with a rune-aligned byte delta.

A `Live` with no node deltas closes the currently streamed suffix. The client
accepts it only if its highest seen version matches; otherwise `OnDesync` asks
the transport owner to re-read from the highest fully sealed turn. Closing a
suffix is not sealing a turn: another model/tool round may reopen the same
turn. `Turn.Sealed` is the final immutability signal.

`turn.done` is separate control state, not conversation content. Its
`{reason,idle}` payload says the turn ended and whether queued work remains.

## Packages

- **`aria`**: `Page`, pagination, `Server`, range-backed `Client`, field-delta
  folding, desync detection, and history fetching. No socket or terminal I/O.
- **`render`**: the inline `Incipit` renderer. Closed slices freeze to native
  scrollback once; only the open suffix is repainted. The transcript renderer
  provides an alternate-screen, scrollable view over the same client/store.
- **`livedoc`** (sibling package): neutral nodes plus rune-safe string
  `Diff`/`Apply`; no ANSI, width, or theme decisions.

## In Figaro

`internal/figaro` owns an `aria.Server`, projects canonical `message.Message`
IR through `internal/uiir`, and fans every resulting page out as
`figaro.aria`. `internal/figaro/read.go` serves the same server state through
`figaro.read`, hydrating a dormant aria from canonical IR on its first read.

`internal/cli/livelog_bridge.go` owns an `aria.Client`; both rich `send` and
`listen` apply pages to it. `listen` performs no prompt call: it reads a recent
window and follows pushes indefinitely.

## External-client status

The protocol is local and functional but not yet a versioned public contract.
Wire types and the folding client are under `internal/`, and the CLI compares
exact build revisions because different builds may not agree on the wire.
Protocol versioning, capability negotiation, public client/schema packages,
and per-subscriber backpressure isolation are tracked in
[`docs/ui-stream.md`](../../docs/ui-stream.md#protocol-stability-todo).
