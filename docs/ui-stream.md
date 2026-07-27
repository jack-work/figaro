# The UI stream

How a figaro conversation reaches your terminal: the **aria read** wire, the
default **inline-freeze** renderer (native scrollback), and the opt-in
**transcript** pager (a live, scrollable full-screen view).

The data model behind all of this is the UI IR (`livedoc.Node`); see
[ir-convergence.md](ir-convergence.md) for how it relates to the canonical fig
IR. This doc is about the *stream* — how nodes get to the screen.

## The shape: one paginated read, pushed and pulled

There is exactly one read shape, `aria.AriaRead`, served two ways:

- **pushed** live as `figaro.aria` notifications (`MethodAriaFrame`) — the server
  streaming its own pagination as the turn unfolds;
- **pulled** for catch-up via `figaro.read(sinceLT)` (`MethodRead`) — the same
  read, caught up from a figaro LT. Subscribing ≡ a `read(0)` plus following the
  push stream.

```go
type AriaRead struct {
    Committed []Committed `json:"committed,omitempty"` // messages that have closed
    Live      *Live       `json:"live,omitempty"`      // the one open message, as deltas
}
```

A **committed** entry is a finalized message — either a full snapshot
(`{lt, role, nodes}`, used on catch-up) or a close marker (`{lt, v}`, once a
connection already streamed it live). The **live** entry is the single open
message, carried as per-node deltas keyed by a stable id:

```go
type Live struct { LT, V int; Role string; Nodes []NodeDelta }
type NodeDelta struct {
    ID    string                   // stable node id
    Set   map[string]any           // merge fields (create on first set)
    Unset []string                 // remove fields
    Patch map[string]livedoc.Delta // splice a streamed string field (markdown/output)
}
```

`V` is a record version (0-indexed, ++ per frame). A client folds deltas into
materialized nodes and promotes the open message to committed only when its seen
version matches the close marker's `V`; a mismatch triggers a re-read from the
last committed LT. `turn.done` is the one control signal — it reports the turn
ended and whether the agent is now idle.

> ### Going turn-shaped
>
> The read is being reshaped from *message* granularity to **turn** granularity:
> one entry per turn, the user prompt and the assistant's nodes together, with
> `Committed`/`Live` moving inside the turn to separate its frozen nodes from its
> **open suffix**. `Live.From` — a single `uint64` node id — becomes the whole
> boundary: `id < From` is committed and will never receive a delta.
>
> The read also becomes genuinely **paginated and bidirectional** (budget in
> bytes, granularity in nodes), because turns are far taller than messages:
> measured over 127 turns in 40 real arias at width 100, median **221 rows**,
> p90 **3043**, max **7988**. A turn-atomic read would regress the common case.
>
> Full types, invariants and worked examples:
> [turn-addressing.md](turn-addressing.md).
>
> **Vocabulary note.** The renderer's ink-to-scrollback step is now
> **freeze** (`Incipit.Freeze`). The word **seal** is reserved for exactly one
> meaning: *a turn became immutable* — the moment its nodes stop moving and it
> is written to the `ui` channel.

Node types: `prose` (assistant/user markdown), `thinking` (extended-thinking),
`tool` (an invocation folded with its streamed result), `steering` (a user
message injected mid-turn — see below).

## Default view: inline-freeze, in native scrollback

The default renderer (`internal/livelog/render`, `Inline`) draws **inline** — no
alternate screen. The consequence is the headline feature:

> **Your terminal's own scrollback owns the conversation.** Closed messages are
> printed once and never touched again, so scrollback, search (your terminal's
> `/`), mouse selection, and copy all work on the real transcript — figaro
> doesn't capture the screen or hold it hostage in a pager.

The mechanism that makes this safe: the **immutability boundary is the resize
boundary.** A message that has closed is frozen to scrollback exactly once; only
the *open* message is a live, redrawable region. So a terminal resize repaints
just that bounded open part — committed history is never reflowed or duplicated.

Each turn opens with one dim full-width rule (a boundary between your shell
prompt and the response), every message is prefaced with a blank line, and a
message closes with a trailing rule: the id·time **bookend** after the assistant
reply (gated on the `status_line` config), a plain wide rule after your prompt.

Inline keybindings while a turn streams:

| Key | Action |
| --- | --- |
| `Ctrl-O` | toggle verbosity (expand tool args / full output) |
| `Ctrl-T` | open the transcript pager (below) |
| `Ctrl-D` | end the turn |

### The inherent inline limit

Inline rendering is clean at normal pane sizes. The one case it cannot fix:
shrinking the pane **shorter than the live message** makes the *terminal itself*
scroll content into native scrollback before figaro's code runs — unreachable
for in-place repaint. This is a property of inline drawing, not a bug; the
alternate-screen transcript (no scrollback to lose) is the escape hatch when you
want a guaranteed-stable, scrollable view.

## Opt-in: the live transcript pager

Press **`Ctrl-T`** to open the transcript — a full-screen, alternate-screen
pager over the *whole* conversation that **keeps streaming live** while you
scroll. It shares the same `aria.Client` model as the inline view (it catches up
with `read(0)` on entry), so both render identical content; only the active view
paints.

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
