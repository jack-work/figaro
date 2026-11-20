# figchat stream: sdk/chat

`github.com/jack-work/figaro/sdk/chat` is how a program that is not a terminal
holds a conversation with figaro: one thread is one aria, a prompt opens a
turn, and the reply streams back as chat events. Herald's figchat plugin is
the first consumer. The package only speaks the public wire (`api/`, `sdk`);
it parses no CLI output.

## Connect

```go
c := chat.New(endpoint) // transport.Endpoint: unix socket, or wss with a bearer
defer c.Close()
```

A `Client` is safe for concurrent use and keeps one connection per thread it
has touched, plus one to the angelus.

## Calls

| call | does |
|---|---|
| `Open(ctx, root) (thread, error)` | forks `root` into a new aria and returns its id. The root needs `system.fanout.max_children` above 0. Store the id; it is the thread. |
| `Prompt(ctx, thread, text) (<-chan Event, error)` | sends `text`. If a turn is already running the text steers it and the channel is nil. Otherwise the channel carries the reply. |
| `Resume(ctx, thread, cursor) (<-chan Event, error)` | replays the turn named by `cursor` from the point the cursor counts, then follows it live. |
| `Cancel(ctx, thread) error` | interrupts the running turn. |
| `History(ctx, thread, before, limit) (History, error)` | up to `limit` (0 means 20) turns older than turn `before` (0 means newest), oldest first. `More` says older turns exist: pass `Turns[0].ID` as the next `before`. |

## Events

```go
type Event struct {
    Kind   Kind   // delta, tool.start, tool.end, end, error
    Text   string // delta: new text; end: the whole reply; error: the reason
    Tool   *Tool  // tool.start: ID, Name, Args; tool.end: ID, Name, Result, Error
    Cursor Cursor // the position after this event
}
```

- `delta` text is the reply's visible prose. Thinking, steering and the
  prompt itself are never sent. Prose of separate blocks in a turn is joined
  by a blank line (`"\n\n"`), which arrives inside the delta that starts the
  later block. Concatenating every delta of a `Prompt` channel gives exactly
  the `end` text; a `Resume` channel's deltas are the part after the cursor.
- `tool.start` is sent once the call's arguments are whole (or the tool has
  finished, or the model has moved past it). `Args` is the arguments as JSON.
- `tool.end` carries `Result` (the output) and `Error` (empty on success).
  Tool ids are `"<turn>.<node>"`, stable across resumes and in History.
  Args and Result are clipped to `MaxToolText` (4 KiB) with a trailing `…`.
- Tool events are released in the order the tools appear in the turn: a tool
  that finishes before an earlier one waits for it.
- `end` closes the channel; `Text` is the whole reply as this channel showed
  it. `error` closes it too; `Text` is the provider or connection failure.
- Text is never taken back. A daemon killed mid-reply can keep less of the
  turn than it streamed; the channel carries on from what it showed, `end`
  keeps it, and History holds what the daemon kept.

A reply can span turns: when the daemon finishes a turn with a prompt still
queued for the aria (a steer that arrived too late to join, or a resume after
a daemon restart), the channel continues into the next turn, starting with a
blank line, and ends when the aria goes idle.

## Cancellation and lifetime

Cancelling the `ctx` given to `Prompt` or `Resume` ends that subscription and
closes its channel without an `end`. It does not stop the turn; `Cancel`
does.

If the connection to the daemon drops while a channel is open, or a channel
is opened while it is down, the client redials for `Client.ReconnectFor`
(30 s by default), reads the turn back and carries on from where the channel
was, with nothing lost or repeated. A catch-up read that fails on a live
connection is retried for as long. If neither recovers, the channel gets
`error`.

## Cursors and reconnecting

```go
type Cursor struct {
    Turn    uint64 // the turn
    Text    int    // bytes of the turn's reply text already delivered
    Started int    // tool.start events delivered in the turn
    Ended   int    // tool.end events delivered in the turn
}
```

Keep the cursor of the last event you forwarded. To pick up after a dropped
browser, a process restart, or a cancelled subscription, call
`Resume(ctx, thread, cursor)`: the first event is the one after it. Cursors
are positions in the turn's content, not in a particular stream, so a cursor
from one `Client` resumes on another. `Text` counts bytes of the turn's own
text; the blank line a multi-turn reply puts before a later turn is not
counted in that turn's cursor.

A `Resume` on a turn that has already finished replays the remainder and ends
at once, unless the turn after it is still running; then the channel follows
that turn as one reply, the way the original channel did. A failed turn read back fails with its reason when it is the aria's newest
turn; an older failed turn read back ends with `end`, since the daemon keeps
only the latest reason.

## History

```go
type Turn struct {
    ID     uint64
    At     time.Time // zero when the daemon does not report it
    Prompt string
    Reply  string    // as the deltas would have built it
    Tools  []Tool    // in order, with Args, Result and Error
    Done   bool      // false for the turn still running
    Cursor Cursor    // the position after everything listed here
}
```

The turn still running appears in `History(ctx, thread, 0, n)` with what it
has so far; `Resume(ctx, thread, t.Cursor)` follows it from there.

## Latency

A delta is sent as the frame carrying it is read off the socket: no batching
and no timers. In the package's own test against a stub provider
(`TestTheFirstDeltaTrailsTheRawFrameByLittle`), the first delta reaches the
consumer within microseconds of the same frame reaching a raw
`sdk.DialAria` listener on a second connection.

## Under the hood

The daemon pushes `figaro.aria` frames: a page whose last part may carry a
live suffix of node deltas, versioned per suffix. A turn can stream in
several rounds (after a tool, a steer, a cut stream); each round ends with a
close marker and the next starts again at version 0. The client folds frames
by node position. A frame whose version it has already seen, or that belongs
to a round a read already covered, is skipped; a frame that skips a version,
or that it meets mid-round, makes it read the turn back with `figaro.read`
and hold later frames until the read lands, then replay them past the read.

Turn ends come from the aria's `runtime` form, read with every catch-up and
followed through its deltas: `turn.id` names the turn, a turn is over once a
later one is named or the state is no longer thinking or tooling, `turn.reason`
is its failure, and `inflight` says whether another turn follows. A dormant
aria has no runtime form; it reads as idle.
