# Turn addressing

The canonical spec for turn ids, the turn-shaped UI IR, and the paginated read.
The turn-shaped `aria.Page` wire and bidirectional node pagination are shipped;
this document describes the current model and calls out legacy wire names where
the implementation has not yet been renamed.

The central result: user-facing commands address **`aria_id:turn_id`**, while
LT remains the storage and cross-channel coordinate underneath.

## Why

A conversation's natural unit is the **turn**: one user prompt plus everything
the agent thought, ran, and said in response. Nothing in figaro names it. The
fig IR has messages; the renderer invents "units" at draw time and discards
them; the fork API takes an LT. Every coordinate we expose is a proxy for the
one we lack.

The cost is concrete. Most LTs you could name are mid-tool positions, and
forking there leaves `tool_invoke` blocks without their `tool_result`. We built
`repairTurnTail` and tail-repair-at-open to synthesize
`interrupted: tool execution did not complete` blocks and make such histories
legal again. A turn boundary is always a user prompt, so **a turn-addressed
fork can never strand a tool call.**

## Invariants

These are load-bearing. Implementations may rely on them; changing one is a
spec change.

1. **LT joins; turn id addresses.** LT stays the xwal substrate: positional
   (derived from the WAL frame index, never persisted in the payload: see
   `message.Message.LogicalTime`), the cross-channel foreign key, and the fork
   coordinate. Turn id rides above it as an attribute.

2. **`(turn, node)` are the UI coordinates.** LT appears in the UI IR only as
   downward-linking `lts` metadata. It is the *model's* coordinate: the LLM
   experiences the conversation in logical time steps, and belongs to a fig IR
   read path, not to UI code. That a tool node spans two LTs is the tell: a
   primitive that does not fit a coordinate means the coordinate is wrong for
   that layer.

3. **`atMainLT` is INCLUSIVE of the frozen prefix.** figwal `disk/fork.go:194`
   reads *"atIdx must be in (FirstIndex, LastIndex+1]; the prefix retains at
   least one entry"*, which sounds exclusive. **It is not**: measured, not
   inferred: forking a real aria at `atMainLT = 5` produced a branch that still
   contained LT 5. So prefix = `[First, atMainLT]`, branch = `(atMainLT, Last]`.
   Fork at turn T therefore uses **`atMainLT = min(LTs of T) - 1`**: the LT just
   before the prompt. The branch retains everything through the end of turn T−1
   and your new prompt becomes the new turn T.
   The −1 is fork *policy* and lives in exactly one place, `cli.resolveTurn`;
   `compose.TurnSpan` reports the honest span and applies no adjustment.
   Related channels do not cut at the same index: each cuts at `boundaryFor` -
   the first own entry whose referenced `mainLT >= atMainLT`
   (figwal `xwal/fork.go:638`).

4. **A closed node never reopens.** Therefore **open nodes form a strict
   suffix** of the turn.

5. **`Turns()` is a pure function of the message list.** No parameter, field,
   or branch may depend on whether the tail is open. The streaming projection
   and the sealed projection are the same function over different inputs, so
   they cannot disagree: which is what prevents the screen visibly rewriting
   itself at turn end (see the seal-transition churn in
   `internal/compose/repro_test.go`).

6. **Node ordinals are reserved on block appearance, even when empty.**
   `compose` emits empty prose/thinking nodes and the renderer hides them. A
   block that fills later therefore already owns its position and cannot shift
   nodes that followed it. **Ordinal order ≡ display order, permanently.**
   `livedoc.Node.ID` is a legacy source/provider receipt still serialized in
   snapshots; it is not this ordinal and clients must not address by it.

7. **A page that does not contain the open suffix is as immutable as a sealed
   page.** Follows from (4). Scrolling back through a live turn costs exactly
   what scrolling back through a sealed one costs.

8. **The live UI turn lives in memory.** `_live` and `turn-wal` both persisted
   the live tail and were retired (`b8c126f`); the replacement is canonical-IR
   append plus drain/tail repair at open (`1d3a26b`). The UI projection is
   currently recomputed from that IR. If a derived `ui` xwal channel lands, it
   may write a turn only after seal: never the mutable suffix.

9. **At most one part per page carries `Live`, and it is the last.** Only the
   newest turn can be open, and a page is a contiguous window.

## Vocabulary

`seal` is now reserved for **exactly one meaning: a turn became immutable.**
Phase 0 freed it.

| was | is | meaning |
|---|---|---|
| `Incipit.Seal` / `i.seal()` | `Incipit.Freeze` / `i.closer()` | ink → scrollback (rendering) |
| `sealTurn()` / `turn_seal.go` | `repairTurnTail()` / `turn_repair.go` | the abort handler |
| `sealEntry`, `sealedInline` | `appendedEntry`, `appendedInline` | a durable append |

## The wire types

```go
// One page. Also the push frame: pull and push share this type.
type Page struct {
    Parts   []TurnPart `json:"parts,omitempty"`
    More    More       `json:"more"`
    Metrics *Metrics   `json:"metrics,omitempty"`
}

type More struct {
    Before bool `json:"before,omitempty"` // nodes exist below this window
    After  bool `json:"after,omitempty"`  // nodes exist above this window
}

type TurnPart struct {
    Turn                        // embedded → JSON-inlined
    From        uint64 `json:"from"`                   // node ordinal of Nodes[0]
    ClippedHead bool   `json:"clipped_head,omitempty"` // From > 0
    ClippedTail bool   `json:"clipped_tail,omitempty"` // ends before the turn's last node
}

type Turn struct {
    ID      uint64  `json:"turn"`
    Inquiry string  `json:"inquiry,omitempty"` // the opening question: TEXT, not a node
    At      int64   `json:"at,omitempty"`      // inquiry time, Unix milliseconds
    LTs    []uint64 `json:"lts,omitempty"`     // metadata only: never an address
    Sealed bool     `json:"sealed"`            // turn lifecycle
    Nodes  []Node   `json:"nodes,omitempty"`   // contiguous from From
    Live   *Live    `json:"live,omitempty"`    // mutable suffix, when one is active
}

// Live is the open suffix of the turn.
type Live struct {
    From  uint64      `json:"from"` // ← THE SUFFIX BOUNDARY
    V     int         `json:"v"`    // record version, ++ per frame
    Nodes []NodeDelta `json:"nodes,omitempty"`
}

type NodeDelta struct {
    ID    uint64                   `json:"id"` // explicit positional node ordinal
    Set   map[string]any           `json:"set,omitempty"`
    Unset []string                 `json:"unset,omitempty"`
    Patch map[string]livedoc.Delta `json:"patch,omitempty"`
}
```

**The suffix boundary is one `uint64`: `Live.From`.**

- `id < live.from` → committed, immutable, will never receive a delta
- `id >= live.from` → open, may receive deltas
- `live == nil` → no suffix is moving now; consult `sealed` for turn finality
- `sealed == true` → the whole turn is immutable and `live` is nil

No per-node state flag is needed. The delta key is the node's **positional
ordinal**, explicit as `NodeDelta.ID` because deltas may arrive sparsely.

**Node addresses inside a part are positional.** `Nodes[i]` is addressed as
`From+i`, because a page window is contiguous. Current snapshot nodes still
serialize `Node.ID` as a string (`"64.0"` for prose provenance or a provider
tool-call id for tools). That field is legacy metadata, not the positional
address; consumers must not compare, order, deduplicate, or route deltas by it.

### `Sealed` vs `Clipped*`

Two orthogonal words, deliberately not one:

- **`Sealed`**: the turn stopped moving. Lifecycle.
- **`ClippedHead` / `ClippedTail`**: this page did not show you all of it.
  Window.

A live turn can be unclipped; a sealed turn can be clipped. Both flags are set
explicitly on every part rather than derived from position in `Parts`, so the
envelope survives a future sparse fetch (search results, bookmarks) where
contiguity does not hold.

## Pagination

**A page is a contiguous run of nodes over a contiguous run of turns.** Only the
boundary turns can be clipped: the first at its head, the last at its tail.
Everything between is whole. With a single turn in the page, that one turn may
be clipped at both ends. This is a theorem of contiguity, not a rule to enforce.

**Bidirectional.** `aria.Paginate` takes `Forward` or `Backward`, so a
scrolling client can pull an earlier or a later page from any `(turn,node)`
anchor. The transcript pager enters with a backward read from a beyond-the-end
turn and then walks by anchors. The public coordinates are **turn ids and node
ordinals, not LTs**.

**Budget in bytes, granularity in nodes.** Emit nodes in order until the
serialized size would exceed the budget; stop at a node boundary; **always emit
at least one node**, even if it alone exceeds the budget. Node count is a poor
budget: measured over 127 turns in 40 real arias at width 100, turns run median
221 rows, p90 3043, max 7988: so a fixed node count could mean 40 bytes or
400 KB.

Node granularity is safe **only because tool output is already clamped** to
`composeBashCap = 200` lines (`internal/compose/compose.go:44`), with the full
text left in the canonical IR. Without that clamp we would need sub-node paging.

```toml
[wire]
page_budget     = 65536   # bytes per page, server default
page_budget_max = 524288  # ceiling on client requests
```

Server default applies when the client omits `limit` or sends a non-positive
value; a client request is clamped to `page_budget_max`. Never let the client
bound server memory.

### Current requests

The request type predates turn addressing. Its JSON names are retained for wire
compatibility even though their values now address turns:

```json
{"jsonrpc":"2.0","id":1,"method":"figaro.read","params":{
  "sinceLT": 7,
  "limit": 65536
}}
```

That is a forward read from turn 7 (at node 0). Application is idempotent, so a
recovery read may include the last sealed turn the client already holds.
Backward paging names the exact oldest node already held and excludes it:

```json
{"jsonrpc":"2.0","id":2,"method":"figaro.read","params":{
  "before": 7,
  "before_node": 4,
  "limit": 65536
}}
```

A caller asks for the tail by using a turn cursor beyond the known end. On the
Go side the typed client exposes `Read(sinceTurn)` and
`ReadBefore(aria.Anchor{Turn, Node}, budget)`, hiding most of the legacy names.

### Paginating function

```go
func Paginate(turns []Turn, at Anchor, dir Direction, budget int) Page
```

The server materializes whole turns in memory and slices on the way out. The
cost being optimized is **wire bytes and client memory**, not server memory.

A pure delta push is a `Page` with one part carrying `Live` and no snapshot
`Nodes`; every pushed part also repeats its turn's `Inquiry`. Push and pull
therefore use one type. A `Live` with no deltas closes the current streaming
suffix; only `Sealed:true` closes the entire turn.

## Worked example

The real `quick test` exchange from aria `84de420c`.

**fig IR**: 4 messages, 7 content blocks:

```
LT 48  user       turn=7  [0] prose        "quick test"
LT 49  assistant  turn=7  [0] thinking     "The user probably wants a quick verifi…"
                          [1] tool_invoke  bash  toolu_01LaNfPv…
                          [2] tool_invoke  bash  toolu_01DZofCa…
LT 50  user       turn=7  [0] tool_result        toolu_01LaNfPv…
                          [1] tool_result        toolu_01DZofCa…
LT 51  assistant  turn=7  [0] prose        "**RESULT: SUCCESS.**…"
LT 52  user       turn=8  [0] prose        "cool, is all this available…"
```

**UI IR**: one turn, question and reply together. The question is the turn's
`inquiry`: TEXT on the turn, not a node, so a renderer tells it from the reply
without inspecting a role, and node ids number the reply from 0:

```
turn 7   lts=[48…51]  sealed=true  inquiry="quick test"
  id 0  thinking  lts=[49]
  id 1  tool      lts=[49,50]  tool_call_id=toolu_01LaNfPv…  status=ok
  id 2  tool      lts=[49,50]  tool_call_id=toolu_01DZofCa…  status=ok
  id 3  prose     lts=[51]                                     "**RESULT: SUCCESS.**…"
```

4 messages and 7 blocks become **1 turn and 4 nodes**. LT 50 has no node of its
own: its two `tool_result` blocks fold into the two tool nodes as
`status`/`output`. It remains addressable in the fig IR and forkable, but it is
not a UI coordinate.

**Steering**, a user message bearing a `tool_result` *and* text. The text
becomes a `steering` node positioned after the tool nodes
(`internal/compose/compose.go:80-88`), sharing the result's LT:

```
LT 60  user       turn=9  [0] prose        "look at the flake too"       ← prompt
LT 61  assistant  turn=9  [0] thinking
                          [1] tool_invoke  bash  toolu_AAA…
LT 62  user       turn=9  [0] tool_result        toolu_AAA…
                          [1] prose        "actually check origin/main"  ← steering
LT 63  assistant  turn=9  [0] prose        "…"
```

```
turn 9   lts=[60…63]  inquiry="look at the flake too"
  id 0  thinking  lts=[61]
  id 1  tool      lts=[61,62]     tool_call_id=toolu_AAA…
  id 2  steering  lts=[62]                        ← shares LT 62 with the tool_result
  id 3  prose     lts=[63]
```

**Tool before steering**, both touching LT 62. Steering is the ONE node that
speaks in the user's voice: it rides inside a turn, so it stays a node, while
the question that OPENS a turn is the turn's own text.

## Consequences

- `fig send aria:7` never mentions an LT. Internally it projects to
  `min(lts)` for `atMainLT`, but that is xwal's business. **The user coordinate
  and the storage coordinate are different things, on purpose.**
- A turn-addressed fork always lands on a user prompt, so history always
  terminates on a complete assistant message, so the dangling-tool synthesis
  path becomes unreachable for user-initiated forks.
- The UI IR earns a home on disk (a `ui` xwal channel, `mainLT` = the turn's
  first LT) because it finally has an immutable unit worth storing.
