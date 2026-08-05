# Status bar and queue — research, and a proposed shape

Commission of 2026-08-05, second half: **research and a proposal, not an
implementation.** Written to be argued with.

The brief, in the owner's words:

> the status indicator needs a little work in general. content that overflows
> the status bar cannot be seen in full. I think we need a hotkey to show it,
> and it should cause the status bar height to increase. Furthermore, the
> queued items should be placed underneath the status bar rather than over it.
> They should cause the status bar to grow the number of lines necessary to see
> all queued items. Capital Q can be used to view the queued items in a list,
> truncated to a few rows at max. Q again should focus the queue and allow
> navigation keybindings to toggle through the queue. Enter on any of these
> items should expand it and let it be readable in the few rows of space
> beneath the status bar. It should vanish when the queue is consumed. This
> might require a protocol change to the queue so that updates to the queue are
> pushed via a similar semantics to other delta updates.

---

## 1. What is there today

### The status row sheds; it does not truncate

`sessionStatus.statusLine` (`internal/cli/session_status.go:168`) builds ranked
tokens and drops whole ones until the row fits:

| rank | token | sheds |
|---|---|---|
| 6 | notice (errors, red) | never — only the ellipsis shortens it |
| 5 | `? help`, `! status` | fifth |
| 4 | turn label (`completed ✓`, `disconnected ⠸`) | fourth |
| 3 | clock | third |
| 2 | `ctx …` | second |
| 1 | `cost …` | first |
| 0 | mantra (already cut to 32 runes) | **first to go** |

So on a narrow pane the mantra is gone before anything else, and a long notice
is ellipsised. **Nothing anywhere can show what was shed** — this is the
owner's first complaint, and the shed order is why it bites the mantra first.

### The queue is drawn ABOVE the bookend, capped at five

`livelogTurn.queuedRows` (`internal/cli/livelog_bridge.go:997`) emits
`↳ queued messages` plus up to `queuedRowsMax = 5` entries (or `h/3`, whichever
is smaller), each `firstLineTrim`'d to one clipped row, then `… and N more`.
They are handed to the pager as `t.tr.queuedRows` and painted with the footer
chrome — i.e. in the region the owner reads as "over the status bar".

### The queue is PULLED, never pushed

`figaro.queued` is a request; `figaro.queue.update` / `figaro.queue.delete`
mutate. There is no notification: nothing tells a client the queue changed. The
CLI re-reads it on its own occasions, so a queue that drains while you are
looking at it goes stale until something asks again. `turn.done` carries an
`idle` flag and that is the whole of the push-side queue vocabulary.

Everything else in the UI stream is already delta-shaped: `figaro.aria` pushes
`Live{From, V, Nodes []NodeDelta}` with `set`/`unset`/`patch`, a close marker,
and resync-by-read on a version mismatch (`reference/ui-stream.md`).

---

## 2. Proposed shape

### 2.1 The status bar becomes a status REGION

One concept, three heights, and the same footer code paints all of them:

```
─────────────────────────────────────────── aria 5fd16081 ───   ← rule (unchanged)
mantra… · completed ✓ · ctx 10.3k/1.0m 1.0% · cost 643 · 02:30   ← row 1 (unchanged)
```

- **`!` (already the status hint) becomes the expander.** It is advertised in
  the row already (`! status`), so no new affordance has to be taught. Pressing
  it grows the region and re-lays the SAME tokens over as many rows as they
  need — nothing is shed, nothing is ellipsised, full mantra, full notice.
  Pressing it again collapses. The shed ranking stays exactly as it is for the
  one-row form; expansion is the escape hatch, not a new layout.
- The expanded form is bounded by a third of the viewport, as the queue trailer
  already is, so it can never be the thing that pushes the reply off screen.

### 2.2 The queue moves BELOW the status row and owns its own rows

```
─────────────────────────────────────────── aria 5fd16081 ───
mantra… · active ⠸ · ctx 10.3k/1.0m 1.0% · cost 643 · 02:30
↳ queued 3                                                       ← summary row, always
  1. rebase onto main and push                                   ← Q: the list
  2. then run the smoke suite
  3. …and update the skill
```

- **default:** one summary row (`↳ queued N`), and only when N > 0. Cheaper
  than today (which spends a blank + a header + up to five rows unconditionally
  once anything is queued) and it never covers the status row.
- **`Q`** opens the list, truncated to a few rows (keep `queuedRowsMax = 5`,
  still floored by `h/3`).
- **`Q` again** focuses it: the existing pager motions (`j`/`k`, `Ctrl-N`/
  `Ctrl-P`) move a selection within the queue instead of the transcript, and
  `Esc` releases focus. This is the same two-step the transcript already uses
  for node selection, so the muscle memory transfers.
- **`Enter`** expands the selected entry into the rows beneath the status row —
  full text, wrapped, bounded by the same `h/3`. This is `nodeExpandable`'s
  gesture applied to a queue entry, and it should reuse the same word:
  expansion is per-item, and an item with nothing more to show is inert.
- **it vanishes when the queue drains**, which is the part that needs §2.3:
  today nothing tells the client.

**Why below and not above.** The status row is an anchor: it is in the same
place every frame, so the eye finds it without reading. Anything that can
change height must therefore grow *away* from it — downward — or the anchor
moves whenever work is queued. That is also why the summary row is one line
whose text changes rather than a block that appears and disappears.

### 2.3 The protocol: `figaro.queue` as a pushed, versioned list

The queue is a *list of small immutable items with a generation*, not a
document, so it does not want `livedoc`'s byte splices. It wants the same
**versioned-suffix discipline** with a coarser grain:

```json
{"jsonrpc":"2.0","method":"figaro.queue","params":{
  "epoch": "8f3c…",          // the inbox generation, as figaro.queued reports today
  "v": 7,                     // 0-indexed frame version, per epoch
  "len": 3,                   // authoritative length AFTER this frame
  "ops": [
    {"op":"add",    "id":12, "at":2, "text":"then run the smoke suite", "sender":"gluck"},
    {"op":"remove", "id":10},
    {"op":"update", "id":11, "text":"rebase onto main and push"},
    {"op":"drain",  "id":9,  "into":"turn", "turn":42}
  ]
}}
```

Four rules, each borrowed from the node stream because it already works:

1. **`epoch` is the reset boundary.** It is minted per agent construction and
   ids restart with it — that is already true of the queue's identity model.
   A frame whose epoch differs from the client's is a full resync: drop the
   local list and re-read `figaro.queued`.
2. **`v` is monotonic per epoch; a gap is a resync**, exactly as
   `Live.V` mismatch is today. Clients never guess.
3. **`len` is authoritative.** It is the one field the summary row needs, so a
   client that renders only `↳ queued N` can ignore `ops` entirely and still be
   correct after a dropped frame. (This is the cheap-client escape hatch the
   node stream lacks and wants.)
4. **`drain` is distinct from `remove`.** "Consumed by a turn" and "deleted by
   the user" look identical in a list and mean opposite things to a reader
   watching their own work get picked up. `into`/`turn` names where it went, so
   the UI can say "started" rather than blinking the row away.

**Mutations stay where they are.** `figaro.queue.update` / `.delete` keep their
request/response shape, including the closed reason set
(`committing`/`committed`/`merged`/`stale`/`unknown`/`closed`) — refusals are
results, and a notification cannot carry a per-request outcome. The push is
strictly an observation channel; every mutation still round-trips.

**Where it is emitted.** One place, for the same reason the steering
classification has one place: the code that owns the inbox. Every enqueue,
every mutation, and every drain already passes through it; a notification
emitted anywhere else can disagree with the queue's own state.

**Cost.** A queue frame is tens of bytes and fires on human-scale events
(a prompt queued, a turn draining). It needs no coalescing and should not get
the 90 ms emit throttle — the throttle exists for token-rate deltas.

### 2.4 What this does NOT need

- No new socket, no subscribe request: every accepted aria connection is
  already registered for notifications.
- No change to `livedoc.Node` — a queue entry is not a node and should not
  pretend to be one. It has no width-dependent render and no byte-splice
  history; giving it `patch` semantics would be cargo-culting.
- No persistence: the queue is already `(epoch, id)` and dies with the agent.

---

## 3. Staged plan

1. **Status expansion (`!`)** — pure client, no protocol. The shed ranking
   stays; expansion re-lays the same tokens across rows.
2. **Queue below the bar, one summary row by default** — pure client. Reuses
   `queuedRows`, moves where the footer composes it, and drops the
   unconditional blank + header.
3. **`Q` list → `Q` focus → `Enter` expand** — pure client, but it needs a
   selection model in the footer region. The transcript's selection is the
   pattern to copy, not to share: the footer has no coordinates.
4. **`figaro.queue` push** — the protocol change. Only after 1–3, because until
   the UI can show a queue well there is nothing to keep fresh, and because
   stages 1–3 will teach us whether `len` alone is enough (it may be: if the
   list is only open while `Q` is pressed, a pull on open plus `len` on every
   frame may cover the whole need without `ops` at all — the cheapest possible
   version of this, and worth measuring before building the rest).

Stage 4's own first question is therefore: **is `ops` needed, or is
`{epoch, v, len}` plus a pull-on-open sufficient?** I would build the frame
with `ops` optional and find out.
