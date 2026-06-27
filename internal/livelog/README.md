# livelog

A pi-styled renderer for a **loosely append-only log**, live-updated from a
stream of events, with **catch-up on (re)connect** over a **paginated, delta-
compressed** API.

It is a self-contained Go module (`github.com/jack-work/figaro/internal/livelog`) with no
dependencies outside the standard library, so it builds and tests in isolation:

```
cd livelog && go test ./...
```

## Why

figaro's live painter flushes finalized rows into native scrollback and drives
the rest with relative cursor moves. That makes a mid-stream terminal **resize**
unrecoverable: the painter can't reflow what's already in scrollback, so a
non-focused/relaid-out tmux pane (a SIGWINCH while a tool is still running)
duplicated content and stranded spinners. `livelog` fixes the class by owning its
screen, pi-style: it holds the whole frame and, on a resize or any change to an
off-screen line, does a **clean full redraw** instead of a doomed in-place reflow.

## Layers

Three packages, each independently testable behind an interface:

| package  | role                                                              |
|----------|------------------------------------------------------------------|
| `doc`    | pure model: `Block`, `Event`, `Doc` (the fold), `Delta`/`Diff`/`Apply` (delta compression). No IO. |
| `stream` | catch-up + live follow: a `Feed` (paginated `Snapshot` + delta-`Tail` + `Subscribe`), a `Server` (journal + materialized state), a `Client` (catch-up, reconnect gap-recovery, Seq-idempotent apply). |
| `render` | pi-style differential renderer over a `Terminal` + `BlockRenderer` interface; ships a `FakeTerminal` (in-memory VT) as the shared test mock. |

`livelog.Viewer` wires all three: subscribe → catch up → render every update →
recover on reconnect.

## Isolation testing

Every boundary is an interface with a substitutable implementation:

- `render.Terminal` → `render.FakeTerminal` (deterministic in-memory VT; asserts
  on the exact transcript a real terminal would show — no tty).
- `render.BlockRenderer` → inject a trivial one to test the differ apart from
  content.
- `stream.Feed` → use `*stream.Server` directly or a mock to test the `Client`'s
  catch-up/reconnect logic apart from any transport.

## Catch-up model

- **Fresh connect** → page the `Snapshot` (current blocks, O(blocks)) then drain
  the `Tail` for anything published while paging. Cheap regardless of history
  depth.
- **Reconnect** → page only the `Tail` from the client's cursor, recovering the
  gap. Events carry `Delta`s, so the tail is delta-compressed.
- Application is idempotent by sequence number, so the catch-up/live overlap
  (and any Follow-before-Catchup ordering) converges exactly.

## Usage

```go
feed := stream.NewServer()                       // or any stream.Feed
term := render.NewANSITerminal(os.Stdout, w, h)  // SetSize on SIGWINCH
v := livelog.NewViewer(term, render.TextRenderer{}, 64)
disconnect := v.Connect(feed)                    // catch up + follow
// ... on SIGWINCH: term.SetSize(w,h); v.Tick()
// ... on a dropped connection: disconnect(); v.Reconnect(feed)
```

Provide a custom `render.BlockRenderer` to draw tools/thinking/prose as widgets.
