# The UI stream

How a figaro conversation reaches your terminal: the **aria read** wire, the
default **inline-seal** renderer (native scrollback), and the opt-in
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

Node types: `prose` (assistant/user markdown), `thinking` (extended-thinking),
`tool` (an invocation folded with its streamed result), `steering` (a user
message injected mid-turn — see below).

## Default view: inline-seal, in native scrollback

The default renderer (`internal/livelog/render`, `Inline`) draws **inline** — no
alternate screen. The consequence is the headline feature:

> **Your terminal's own scrollback owns the conversation.** Closed messages are
> printed once and never touched again, so scrollback, search (your terminal's
> `/`), mouse selection, and copy all work on the real transcript — figaro
> doesn't capture the screen or hold it hostage in a pager.

The mechanism that makes this safe: the **immutability boundary is the resize
boundary.** A message that has closed is sealed to scrollback exactly once; only
the *open* message is a live, redrawable region. So a terminal resize repaints
just that bounded open part — committed history is never reflowed or duplicated.

Each turn opens with one dim full-width rule (a boundary between your shell
prompt and the response), every message is prefaced with a blank line, and a
message seals with a trailing rule: the id·time **bookend** after the assistant
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
| `gg` / `G` | top / bottom |
| `/` | literal string search |
| `q` / `Esc` / `Ctrl-T` | exit the pager |

At the bottom the view **follows** new output live (the status bar shows
`(live)`); scroll up and it holds position while the conversation grows beneath
you. Messages that close while you're paging flush to the inline scrollback when
you exit, so nothing is lost. If the turn finishes while you're reading, the
command stays open until you close the pager.

## Steering: messages mid-turn

A message sent while a turn is running (e.g. `fig send` to a busy aria) doesn't
wait for a new turn — it folds into the *current* turn as a **steering** node,
which the model reads on its next round. In the stream it appears as a
`steering` node (rendered under a `↳ you` gutter) positioned where it arrived,
inside the assistant's turn. The client tells "my turn is done" from "a turn
ended with my steer still queued" via `turn.done`'s idle flag, so a steering
send waits for *its own* completion.

> Steering is a server-side feature (the mid-turn drain). It requires a daemon
> built with it; an older long-lived daemon will queue the message as a separate
> turn instead. `figaro stop` cuts the daemon over to a fresh binary.
