# Turn addressing

The canonical spec for turn ids, the turn-shaped UI IR, and the paginated read.
Phase 2/3 implement against this document.

The capital goal: **`fig send aria_id:LT` becomes `fig send aria_id:turn_id`**,
with turn id correlated across every xwal channel.

## Why

A conversation's natural unit is the **turn** — one user prompt plus everything
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
   (derived from the WAL frame index, never persisted in the payload — see
   `message.Message.LogicalTime`), the cross-channel foreign key, and the fork
   coordinate. Turn id rides above it as an attribute.

2. **`(turn, node)` are the UI coordinates.** LT appears in the UI IR only as
   downward-linking `lts` metadata. It is the *model's* coordinate — the LLM
   experiences the conversation in logical time steps — and belongs to a fig IR
   read path, not to UI code. That a tool node spans two LTs is the tell: a
   primitive that does not fit a coordinate means the coordinate is wrong for
   that layer.

3. **`atMainLT` is exclusive of the frozen prefix.** `disk.Log.Fork`:
   *"atIdx must be in (FirstIndex, LastIndex+1]; the prefix retains at least one
   entry."* So prefix = `[First, atMainLT)`, branch = `[atMainLT, Last]`.
   Fork at turn T uses **`atMainLT = min(LTs of T)`** — the prompt's LT — with
   **no off-by-one adjustment**. The shared prefix is everything strictly before
   the question; the branch replaces the question and all downstream.
   Related channels do not cut at the same index: each cuts at `boundaryFor` —
   the first own entry whose referenced `mainLT >= atMainLT`
   (figwal `xwal/fork.go:638`).

4. **A closed node never reopens.** Therefore **open nodes form a strict
   suffix** of the turn.

5. **`Turns()` is a pure function of the message list.** No parameter, field,
   or branch may depend on whether the tail is open. The streaming projection
   and the sealed projection are the same function over different inputs, so
   they cannot disagree — which is what prevents the screen visibly rewriting
   itself at turn end (see the seal-transition churn in
   `internal/compose/repro_test.go`).

6. **Node ids are minted on block appearance, even when empty.** Today
   `compose` skips empty prose/thinking (`TrimSpace(c.Text) == "" → continue`),
   so a block that arrives empty and fills later would be minted *after* nodes
   that follow it positionally. Mint on appearance and let the renderer skip
   empties at draw time. Then **id order ≡ display order, permanently.**

7. **A page that does not contain the open suffix is as immutable as a sealed
   page.** Follows from (4). Scrolling back through a live turn costs exactly
   what scrolling back through a sealed one costs.

8. **The live turn lives in memory** and is written to xwal exactly once, on
   seal. `_live` and `turn-wal` both persisted the live tail and were both
   retired for it (`b8c126f`); the replacement is drain + tail repair at open
   (`1d3a26b`). Do not re-litigate.

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
// One page. Also the push frame — pull and push share this type.
type Page struct {
    Parts []TurnPart `json:"parts"`
    More  More       `json:"more"`
}

type More struct {
    Before bool `json:"before"` // nodes exist below this window
    After  bool `json:"after"`  // nodes exist above this window
}

type TurnPart struct {
    Turn                        // embedded → JSON-inlined
    From        uint64 `json:"from"`                   // node id of Nodes[0]
    ClippedHead bool   `json:"clipped_head,omitempty"` // From > 0
    ClippedTail bool   `json:"clipped_tail,omitempty"` // ends before the turn's last node
}

type Turn struct {
    ID     uint64   `json:"turn"`
    LTs    []uint64 `json:"lts"`             // metadata only — never an address
    Sealed bool     `json:"sealed"`          // turn lifecycle
    Nodes  []Node   `json:"nodes,omitempty"` // contiguous from From
    Live   *Live    `json:"live,omitempty"`  // the open suffix; nil once sealed
}

// Live is the open suffix of the turn.
type Live struct {
    From  uint64      `json:"from"` // ← THE SUFFIX BOUNDARY
    V     int         `json:"v"`    // record version, ++ per frame
    Nodes []NodeDelta `json:"nodes"`
}

type NodeDelta struct {
    ID    uint64                   `json:"id"` // explicit: reference is non-positional
    Set   map[string]any           `json:"set,omitempty"`
    Unset []string                 `json:"unset,omitempty"`
    Patch map[string]livedoc.Delta `json:"patch,omitempty"`
}
```

**The suffix boundary is one `uint64`: `Live.From`.**

- `id < live.from` → committed, immutable, will never receive a delta
- `id >= live.from` → open, may receive deltas
- `live == nil` → the turn is sealed; every node is committed

No per-node state flag and no separate delta key: **the node's own id is the
delta key.** (`Node.ID` previously answered two questions — provenance and
identity — which is the mess this whole effort undoes. Do not reintroduce a
second identifier.)

**Node ids inside a part are positional.** `Nodes[i].ID == From + i`, because a
page window is contiguous. The per-node `id` is therefore **omitted from the
wire** for nodes in `Turn.Nodes`; it stays explicit only in `NodeDelta`.

### `Sealed` vs `Clipped*`

Two orthogonal words, deliberately not one:

- **`Sealed`** — the turn stopped moving. Lifecycle.
- **`ClippedHead` / `ClippedTail`** — this page did not show you all of it.
  Window.

A live turn can be unclipped; a sealed turn can be clipped. Both flags are set
explicitly on every part rather than derived from position in `Parts`, so the
envelope survives a future sparse fetch (search results, bookmarks) where
contiguity does not hold.

## Pagination

**A page is a contiguous run of nodes over a contiguous run of turns.** Only the
boundary turns can be clipped — the first at its head, the last at its tail.
Everything between is whole. With a single turn in the page, that one turn may
be clipped at both ends. This is a theorem of contiguity, not a rule to enforce.

**Bidirectional.** `dir: forward | backward` is first-class, so a scrolling
client can pull an earlier *or* a later page from any anchor. In particular
**`fig show -n N` means paginate backwards from the end** — the tail of the
newest turn(s) — and transacts in **turn ids, not LTs**.

**Budget in bytes, granularity in nodes.** Emit nodes in order until the
serialized size would exceed the budget; stop at a node boundary; **always emit
at least one node**, even if it alone exceeds the budget. Node count is a poor
budget: measured over 127 turns in 40 real arias at width 100, turns run median
221 rows, p90 3043, max 7988 — so a fixed node count could mean 40 bytes or
400 KB.

Node granularity is safe **only because tool output is already clamped** to
`composeBashCap = 200` lines (`internal/compose/compose.go:44`), with the full
text left in the canonical IR. Without that clamp we would need sub-node paging.

```toml
[wire]
page_budget     = 65536   # bytes per page, server default
page_budget_max = 524288  # ceiling on client requests
```

Server default applies when the client omits `budget`; a client request is
clamped to `page_budget_max`. Never let the client bound server memory.

### Request

```json
{"method":"figaro.read","params":{
  "turn":   7,          // null = newest
  "from":   4,          // node id anchor; null = start (forward) / end (backward)
  "dir":    "forward",  // or "backward"
  "budget": 65536       // optional; server default, clamped to max
}}
```

### Paginating function

```go
func Paginate(turns []Turn, from Cursor, dir Dir, budget int) Page
```

The server materializes whole turns in memory and slices on the way out. The
cost being optimized is **wire bytes and client memory**, not server memory.

A pure delta push is a `Page` with one part carrying `Live` and an empty
`Nodes` — so push and pull collapse into one type, which is what
`docs/ui-stream.md` already claims ("exactly one read shape, served two ways").

## Worked example

The real `quick test` exchange from aria `84de420c`.

**fig IR** — 4 messages, 7 content blocks:

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

**UI IR** — one turn, prompt and reply together:

```
turn 7   lts=[48…51]  sealed=true
  id 0  prose     lts=[48]                                     "quick test"
  id 1  thinking  lts=[49]
  id 2  tool      lts=[49,50]  tool_call_id=toolu_01LaNfPv…  status=ok
  id 3  tool      lts=[49,50]  tool_call_id=toolu_01DZofCa…  status=ok
  id 4  prose     lts=[51]                                     "**RESULT: SUCCESS.**…"
```

4 messages and 7 blocks become **1 turn and 5 nodes**. LT 50 has no node of its
own: its two `tool_result` blocks fold into the two tool nodes as
`status`/`output`. It remains addressable in the fig IR and forkable, but it is
not a UI coordinate.

**Steering** — a user message bearing a `tool_result` *and* text. The text
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
turn 9   lts=[60…63]
  id 0  prose     lts=[60]        role=user
  id 1  thinking  lts=[61]
  id 2  tool      lts=[61,62]     tool_call_id=toolu_AAA…
  id 3  steering  lts=[62]                        ← shares LT 62 with the tool_result
  id 4  prose     lts=[63]
```

**Tool before steering**, both touching LT 62. Steering renders like the user
part of the turn; it is the same primitive as the prompt, differing only in
position.

## Consequences

- `fig send aria:7` never mentions an LT. Internally it projects to
  `min(lts)` for `atMainLT`, but that is xwal's business. **The user coordinate
  and the storage coordinate are different things, on purpose.**
- A turn-addressed fork always lands on a user prompt, so history always
  terminates on a complete assistant message, so the dangling-tool synthesis
  path becomes unreachable for user-initiated forks.
- The UI IR earns a home on disk (a `ui` xwal channel, `mainLT` = the turn's
  first LT) because it finally has an immutable unit worth storing.
