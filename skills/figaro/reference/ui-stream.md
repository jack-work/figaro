# The UI stream

How a figaro conversation reaches your terminal: the **aria read** wire, the
default **inline-freeze** renderer (native scrollback), and the opt-in
**transcript** pager (a live, scrollable full-screen view).

The data model behind all of this is the UI IR (`livedoc.Node`); see
[ir-convergence.md](ir-convergence.md) for how it relates to the canonical fig
IR. This doc is about the *stream* — how nodes get to the screen.

## The shape: one turn-shaped page, pushed and pulled

There is exactly one conversation-read shape, `aria.Page`, served two ways:

- **pushed** as `figaro.aria` JSON-RPC notifications while a turn changes;
- **pulled** with `figaro.read` for initial state, paging, and desync recovery.

Every accepted connection to an aria socket is automatically registered for
future notifications. There is no subscribe request today: connect, issue a
read on that same NDJSON JSON-RPC connection, then continue consuming pushed
frames. Read/push overlap is allowed and application is idempotent.

```go
type Page struct {
    Parts   []TurnPart `json:"parts,omitempty"`
    More    More       `json:"more"`
    Metrics *Metrics   `json:"metrics,omitempty"`
}

type TurnPart struct {
    Turn
    From        uint64 `json:"from"`
    ClippedHead bool   `json:"clipped_head,omitempty"`
    ClippedTail bool   `json:"clipped_tail,omitempty"`
}

type Turn struct {
    ID      uint64         `json:"turn"`
    Inquiry string         `json:"inquiry,omitempty"`
    At      int64          `json:"at,omitempty"`
    LTs     []uint64       `json:"lts,omitempty"`
    Sealed  bool           `json:"sealed"`
    Nodes   []livedoc.Node `json:"nodes,omitempty"`
    Live    *Live          `json:"live,omitempty"`
}
```

A `Turn` is one exchange. `Inquiry` is the question that opened it — text on
the turn, not a node — and every `TurnPart` states that question so a client
joining mid-stream does not depend on having seen one special opening frame.
`Nodes` contains agent output plus steering interjections. `LTs` and each
node's source LTs are provenance only; the UI address is `(turn, node)`.

A page is a contiguous window over those coordinates. In a `TurnPart`,
`Nodes[i]` is at node ordinal `From+i`; `ClippedHead` and `ClippedTail` say the
window omitted part of that turn. Current `livedoc.Node` values may also carry
a legacy string `id` used as source/tool metadata. It is emitted on snapshots
but is **not** the UI address and must not be used for ordering, deduplication,
or delta routing.

The newest turn may carry one mutable suffix:

```go
type Live struct {
    From  uint64      `json:"from"`
    V     int         `json:"v"`
    Nodes []NodeDelta `json:"nodes,omitempty"`
}

type NodeDelta struct {
    ID    uint64                   `json:"id"`
    Set   map[string]any           `json:"set,omitempty"`
    Unset []string                 `json:"unset,omitempty"`
    Patch map[string]livedoc.Delta `json:"patch,omitempty"`
}
```

`Live.From` is the immutability boundary: node ordinals below it are closed and
will never change; ordinals at or above it may still receive deltas. `V` is the
0-indexed frame version. `set` merges fields (and creates a node when `type`
first appears), `unset` removes fields, and `patch` splices a previous
**streamed string** using byte offsets. Three fields are streamed:
`markdown`, `output`, and `input`.

`input` is a tool's arguments **as they arrive** — the raw, still-truncated
JSON prefix — beside `args`, which is the same thing decoded and therefore
only exists once the whole object parses. It is what there is to show while
the model is still writing a large argument, and it is deliberately not
append-only: a bounded tail drops leading bytes as it slides, and the field is
cleared (an `unset`) the moment `args` lands. A `Delta` carries `del` as well
as `ins`, so a shrink is one ordinary splice and needs nothing new on the
wire. Whether the fragments arrive smoothly at all is a **provider** question
— see `system.eager_tool_streaming` in
[architecture.md](architecture.md).

### How a tool draws

One table in the CLI (`internal/cli/toolbox.go`, `toolStyles`) says how every
tool is drawn, keyed by the tool NAME the wire already carries. It is
presentation policy — the server has no business knowing that a shell wants a
`$` — and it is the only place any of it is written down. A second frontend
lifts the table rather than the renderer.

| field | means |
|---|---|
| `Label` | replaces the tool's name on the minimized header (`$` for shells) |
| `Headline` | the argument that speaks for the call: the command, the path |
| `Body` | an argument to draw in place of the tool's own output |

```go
"bash":  {Label: "$", Headline: "command"},
"write": {Headline: "path", Body: "content"},
"read":  {Headline: "path"},
```

An unknown tool keeps its name and takes its first argument as the headline —
the same shape as the known ones, not a special case.

```
minimized                                    expanded (Enter, or Ctrl-O)
✓ $ grep -n baritone opera.md [1.4s]         ✓ bash [1.4s]
  │ … last 10 of 32 lines                      │ grep -n baritone opera.md
  │ 15:13. Figaro is a baritone.                │ timeout 240
                                               │ started 2026-08-06 01:33:48.347 EDT
✓ write /var/tmp/x/opera.md [17.2s]            │ finished 2026-08-06 01:33:48.365 EDT
  │ … last 10 of 32 lines                      │
  │ 33. Rossini's Almaviva is a tenor.          │ 15:13. Figaro is a baritone.
```

Minimized, the header carries the CALL — what a reader scanning a transcript is
looking for — and the body carries the result, clamped, each row **cut** rather
than wrapped: a preview that reflows is harder to scan than one that stops.
Expanded, the header steps back to the tool's name because the call is about to
be shown in full: the headline argument first (in the call colour, unlabelled),
then the other arguments, then `started`/`finished`, then one blank row, then
the whole output, wrapped.

`write` sets `Body: "content"`, so the file body streams in exactly the way a
command's output does, and its receipt ("Wrote N bytes") is never shown — the
content is the interesting half and the reader can see it.

The duration is **one number**: opened to finished, the model writing the call
plus the tool running it. The split is in the expanded view, where `started`
and `finished` bracket the execution and everything before `started` was
generation.

**Expansion is per node and it persists.** `Esc` clears the *selection* and
leaves the expansion alone; the way back is to select the node again and press
`Enter`, or to leave the pager. (Expansion state lives in `transcript.expanded`,
keyed by `(turn, node)`. `pruneCaches` must keep the OPEN turn — it is the live
suffix and is not in the store's window, so a walk of the window alone prunes
it as though it had scrolled out of history. That dropped a live expansion on
`Esc` and on every frame while following the tail.)

`Ctrl-O` shows the metadata and the coordinate row and **nothing else** — it no
longer opens content. Verbosity and "open this one thing" are different
questions, and one key answering both meant neither could be asked alone.

A folded multi-line value names its fold on its **label** — `content (…last 5
of 41 lines)`, and `(…first 2 of 41 lines)` once settled, because a reader
cannot otherwise tell which end they are looking at. Expanded, the note is gone
and the value wraps instead: there is nothing to count when everything is
shown.

Colour carries what the layout does not: Kanagawa springBlue for the tool name
and every argument value, fujiGray for labels, and the same dim for every rule
in the box.

`y` follows the eye: a folded tool yanks its **output**, an expanded one yanks
the **call and the result**, both in full.

Two rules that are easy to get wrong:

- **A settled tool folds its arguments to the head.** Nothing is moving any
  more, so the useful part is what the call *is*, not where it stopped.
- **Only the pager has an expansion gesture**, so every other surface draws the
  minimized form. That reverses an older decision for the incipit, which used
  to draw every row of a tool's output because inline rows freeze to scrollback
  and a collapse there can never be undone. True — but written when a collapse
  was SILENT. The `… last N of M lines` banner now says what was elided,
  `figaro show` has the rest, and a 60-line file written inline buried the
  conversation it belonged to. See `ariaView.gesture`.

The argument fold note lives on the label rather than in a row of its own,
because rows are what is being rationed.

A `Live` frame with deltas updates the suffix. A `Live` frame with no deltas is
a close marker for that streaming suffix; it does **not** necessarily finish
the whole turn, because another model/tool round may follow. A client promotes
what it materialized only when the close marker's `V` matches its highest seen
version. On mismatch it re-reads from its highest fully sealed turn. A part with
`sealed:true` is the distinct, final signal that the entire turn is immutable.

### The queue: interrupt disposition, identity, and refusals

Prompts accepted while a turn is running wait in the aria's queue.
`figaro.queued` reads it (`{"include_carriers":true}` adds the empty-text
chalkboard carriers, which the default omits, as it always has);
`figaro.queue.update` and `figaro.queue.delete` mutate it. There is no create
method: `figaro.qua` IS the create.

`figaro.interrupt` takes an explicit disposition for the queue, defaulting to
`keep` so a client that predates the field is unchanged:

```json
{"queue": "keep"}     // stop the turn; the queue is answered next
{"queue": "clear"}    // stop the turn and drop the queue
→ {"ok": true, "cleared": false, "epoch": "…", "queue": [ … ]}
```

`queue` in the response is the queue **as of the hangup** — one field, one
meaning — and `cleared` says whether those messages were removed. On the clear
path the drain happens BEFORE any fold, so what comes back is verbatim — one
entry per message, each with its own id — which is what makes it worth
persisting.

**Coalescing is a property of DRAINING, not of interrupting.** All three drain
sites — the idle drain that opens a turn, the two mid-turn drains that steer
one, and the interrupt — fold the contiguous run of queued prompts into one
message: texts joined by a BLANK LINE (a lone newline is a soft break in
markdown, so the screen would rejoin them), chalkboard input merged in queue
order so a later value wins. **A queued `set` or `fork` is a barrier** that is
never crossed. A lone prompt is passed through unchanged.

An interrupted turn **never drains**: a round that opened with a cancelled
context cannot answer what it lifts, and prompts lifted there were committed to
the log and then abandoned unanswered. They stay queued, and the next turn asks
them.

**Identity.** A queued message is `(epoch, id)`. `id` is a small dense counter;
`epoch` names the INBOX GENERATION, minted afresh every time an agent is
constructed. Ids restart with it, so a mutation must present the epoch its ids
were read against; a mismatch is refused as `stale` and nothing is mutated. The
epoch is compared only for equality, and is a string on the wire because a
64-bit nonce would lose precision in a JSON number.

**Refusals are results.** Both mutators succeed at the RPC level and report one
outcome per requested id (`deleted`/`updated`/`rejected`), in request order.
Neither response has a summary `ok` field: reading the outcome is the only way
to learn anything. The reason set is closed — `committing`, `committed`,
`merged` (with `into`, the surviving id), `stale`, `unknown`, `closed` — and
the JSON-RPC error channel is reserved for transport and malformed requests.

`turn.done` is the only control notification. Its params are
`{"reason":"...","idle":true|false}`: the turn ended, and `idle` says whether
queued work remains. It is not transcript content and does not replace the
sealed `figaro.aria` frame.

The current `figaro.read` request predates turn addressing, so two field names
retain LT-era spelling. Forward catch-up uses `sinceLT` as a **turn cursor**;
backward paging uses `before` plus `before_node`; `limit` is a byte budget. See
[turn-addressing.md](turns.md) for the exact current requests,
pagination rules, types, and worked examples.

### Connection and endpoint discovery

The Angelus supervisor and each live aria listen on local Unix-domain sockets;
the transport payload is JSON-RPC 2.0 with one JSON object per line. The runtime
root is `$FIGARO_RUNTIME_DIR` when set, otherwise `$XDG_RUNTIME_DIR/figaro`,
otherwise the platform temporary directory plus `figaro`. On Windows the last
case is normally `%TEMP%\figaro`. The supervisor is `angelus.sock` and aria
sockets live under `figaros/<id>.sock`.

Do not construct an aria path as the primary discovery mechanism. Connect to
Angelus and attach by id so a dormant aria is restored and its actual endpoint
is returned:

```json
{"jsonrpc":"2.0","id":1,"method":"figaro.attach","params":{"figaro_id":"85ac180e"}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"figaro_id":"85ac180e","endpoint":{"scheme":"unix","address":".../figaros/85ac180e.sock"}}}
```

Connect to that endpoint, immediately issue an idempotent catch-up read, and
keep one reader consuming interleaved responses and notifications:

```json
{"jsonrpc":"2.0","id":2,"method":"figaro.read","params":{"sinceLT":0,"limit":65536}}
```

A full frontend can also call `figaro.qua`, `figaro.interrupt`,
`figaro.context`, `figaro.chalkboard`, `figaro.set`, `figaro.loadout`,
`figaro.queued`, `figaro.queue.update` and `figaro.queue.delete` on the aria
socket; creation, listing, forking, promotion, and
lifecycle operations remain on Angelus. Request ids are integers in the current
`jkrpc` framing.

Node types: `prose` (assistant markdown), `thinking` (extended-thinking),
`tool` (an invocation folded with its streamed input and result), and `steering` (a user
message injected mid-turn — see below).

### Protocol stability TODO

The local socket protocol is usable by alternate frontends, but it is not yet
a versioned public contract. The CLI currently protects itself by comparing
its exact build revision with the angelus because wire shapes may change
between builds. Before independent frontends are treated as supported clients:

1. add a protocol version and capability list to the angelus/aria handshake;
2. define compatibility and required/optional-field rules;
3. publish the wire structs and delta-folding client outside `internal/` (plus
   a language-neutral schema for non-Go clients);
4. bound each subscriber independently so a slow reader cannot block aria
   fan-out; and
5. provide a portable bridge for clients, such as browsers, that cannot dial a
   local Unix-domain socket directly.

Until then, external clients should be revision-pinned and treat the sockets as
a trusted, same-user interface: the aria connection also exposes mutating
methods such as `figaro.qua`, `figaro.interrupt`, and `figaro.set`.

## Default view: inline-freeze, in native scrollback

The default `Incipit` renderer (`internal/livelog/render`) draws **inline** —
no alternate screen. The consequence is the headline feature:

> **Your terminal's own scrollback owns the conversation.** Closed turn slices
> are printed once and never touched again, so scrollback, search (your
> terminal's `/`), mouse selection, and copy all work on the real transcript —
> Figaro does not capture the screen or hold it hostage in a pager.

The mechanism that makes this safe: the **immutability boundary is the resize
boundary.** Closed nodes freeze to scrollback exactly once; only the open turn
suffix is a live, redrawable region. A terminal resize therefore repaints just
that bounded suffix — committed history is never reflowed or duplicated.

Each turn opens with one dim full-width rule and renders its inquiry above the
agent nodes. The assistant reply ends with the id·time **bookend** (gated on the
`status_line` config).

Inline keybindings while a turn streams:

| Key | Action |
| --- | --- |
| `Ctrl-O` | toggle verbosity (expand tool arguments and full output) |
| `Ctrl-T` | open the transcript pager (below) |
| `Ctrl-D` | end the turn |

### The inherent inline limit

Inline rendering is clean at normal pane sizes. The one case it cannot fix:
shrinking the pane **shorter than the live suffix** makes the *terminal itself*
scroll content into native scrollback before figaro's code runs — unreachable
for in-place repaint. This is a property of inline drawing, not a bug; the
alternate-screen transcript (no scrollback to lose) is the escape hatch when you
want a guaranteed-stable, scrollable view.

## Opt-in: the live transcript pager

Press **`Ctrl-T`** to open the transcript — a full-screen, alternate-screen
pager over the *whole* conversation that **keeps streaming live** while you
scroll. It shares the same `aria.Client`/range store as the inline view. On
entry it pulls a recent backward page, fetches older ranges on demand, and
continues folding live pushes, so both views render the same content; only the
active view paints.

Alternate screen is the right tool *here specifically* because it's a deliberate,
toggled view: it gives a guaranteed-stable, scrollable surface without occluding
your shell history permanently — on exit, the terminal restores your normal
screen and figaro reconstructs the inline scrollback so it reads as though you'd
run `figaro show` (full content above, cursor below).

| Key | Action |
| --- | --- |
| `j` / `k` | line down / up |
| `u` / `d` | half-page up / down |
| `gg` / `G` | top of the retained buffer / bottom |
| `↓` / `↑` | line down / up |
| `PgDn` / `PgUp` | half-page down / up |
| `Home` / `End` | top / bottom |
| `/` | literal string search |
| `:` | jump to a coordinate — `:12`, `:12.3`, `:0` |
| `q` / `Esc` / `Ctrl-T` | exit the pager |

### Coordinates and the `:` jump

**`Ctrl-O` in the pager also draws every node's address**, one dim row above
it — `12.3 · 01:23:45`: turn id, node id, and when the node was written. The
turn's opening question gets the same row at its virtual node id, `-1`, because
the question selects, copies and highlights exactly as a node does and is
addressable the same way.

**`:` opens a command line that accepts one of those addresses.**

| Typed | Goes to |
| --- | --- |
| `:12` | the head of turn 12 |
| `:12.3` | node 3 of turn 12 |
| `:12.-1` | turn 12's question |
| `:0` | the beginning of the aria |

The landing snaps: the target's first row goes to the top and the selection is
placed on it, so you can see what you were sent to.

`:0` is a **sentinel, not an address**. Turn ids are dense and 1-based within an
aria, but a forked aria continues its parent's numbering, so a fork's first turn
is not turn 1 — and `Anchor{Turn: 0}` means *unset* on the wire, so asking for
it on a backward read returns the tail. `:0` therefore means "the lowest turn
that actually exists", found by walking back until the store says there is
nothing older.

`gg` is unchanged and still means **the top of what is loaded** — the cheap
gesture, no paging. Once a conversation is long enough to page these are
different questions, so they are different keys.

A target that is not loaded is walked toward through the ordinary backward
paging path, **bounded**; if it cannot be reached, the footer says so rather
than hanging or landing somewhere else. `:` and `/` are each literal text inside
the other's box.

**A hole is not a turn.** The window can contain gaps — a `N turns not loaded`
rule where history was evicted or arrived non-contiguously — and a gap entry
carries turn `0`, an id no aria issues. A coordinate that lands inside one is
*not yet*, never *absent*: the walk stays up, asks for that hole to be filled
(even one the viewport cannot see), and **snaps to the real turn once the entry
is ungapped**. `:0` likewise waits rather than landing on the sentinel standing
where the beginning will be. What cannot exist — past the live tail, or below a
proven floor with no hole — is still answered honestly and at once.

The arrow cluster is an alias for the letter motions, and — like them — pressing
one while a turn streams inline opens the pager first, so the gesture acts on
arrival instead of looking like a dead keyboard.

These tables are a summary. The canonical list is the **keymap** in
`internal/cli/keymap.go`: one declarative row per binding — the chord, the modes
it is live in, whether it opens the pager from incipit, its action, and its help
text. The `?` panel is generated from it, so what the pager tells you about
itself cannot drift from what it does.

At the bottom the view **follows** new output live (the status bar shows
`(live)`); scroll up and it holds position while the conversation grows beneath
you. Messages that close while you're paging flush to the inline scrollback when
you exit, so nothing is lost. If the turn finishes while you're reading, the
command stays open until you close the pager.

### The pager can open without being asked

Three doors reach it, and **every one of them reads the aria's tail first**:

| door | when |
|---|---|
| `Ctrl-T` / `Ctrl-L` / `figaro listen` | you asked |
| an open turn taller than the viewport | it cannot be painted inline |
| a resize that shrinks the viewport under the live region | same, from the other side |

The last two are **automatic promotions**, and they used to open on nothing but
the turn being streamed: no history above it, `More.Before` never set by any
wire answer, and so the pager concluded that the question you had just typed was
the beginning of the aria — scrolling up found nothing, forever. The read is now
owed by the promotion itself (`livelogTurn.enterPager` → `catchUp`) and runs
**off the render lock**, since one of its callers is the frame path. A failed
read is not a floor: the claim is released so a later gesture retries.

## Steering: messages mid-turn

A message sent to a **busy** aria does not wait for a new turn — it folds into
the *current* turn as a **steering** node, which the model reads on its next
round. In the stream it appears as a `steering` node (rendered under a
`↳ input` gutter) positioned where it arrived, inside the agent's turn. The
client tells "my turn is done" from "a turn ended with my steer still queued"
via `turn.done`'s idle flag, so a steering send waits for *its own* completion.

**Timing is the whole rule, and there is no flag.** One command, identical
whether or not the aria is busy: arrive while a turn is running and you steer
it; arrive when nothing is running and you open a turn. A steer is not a turn,
so it does not get one.

The classification is made in exactly one place — **the code that drains the
queue into a turn** — because that is the only point that knows the turn
boundary as the agent itself sees it, rather than as a client call returning.
Nothing upstream declares it and nothing downstream may override it. The
`steering` bit is persisted so a replayed log classifies the same way it did
live.

> Steering is a server-side feature (the mid-turn drain). It requires a daemon
> built with it; an older long-lived daemon will queue the message as a separate
> turn instead. `figaro stop` cuts the daemon over to a fresh binary.
