# livelog

The live-render layer for figaro: an **aria read** protocol plus an **inline-seal**
terminal renderer. Standard-library only (no deps outside the figaro module), in
isolation-testable packages.

## The one API: a paginated read keyed by figaro LT

There is a single read API — a paginated read. The live stream is just the server
*pushing* that pagination as state changes, so a subscription is semantically
identical to repeatedly calling `read(sinceLT)`. The cursor is a figaro **LT**
(logical time): to catch up after a missed event or a reconnect, a client reads
from its last received LT.

An `AriaRead` (one page) has two optional sections — omitted when empty to save
bytes on noisy chats:

- **`committed`** — closed messages, each by LT. A *full* entry (role+nodes) is
  content for a client that lacks it (catch-up); a *close-patch* (`closed:true`,
  no nodes) just signals that a message the connection streamed live has now
  closed. Close-patches sort before newer full messages (lower LT first).
- **`live`** — the currently-open message: an ordered set of **blocks**, each with
  a stable **id** and a monotonic **version** (it binds to one UI element).

**Invariants** (per connection): an LT appears in `committed` at most once; a
message appears there once and never again; a message may spend time in `live`
before it closes. The close signal *is* the LT appearing in `committed`.

**Phase 1** (here): each block carries its full text on every update.
**Phase 2**: that text becomes a splice patch on the prior version — the same
item, compressed. `doc.Delta` (Diff/Apply) is the Phase-2 primitive.

## Packages

- **`aria`** — the protocol. `Server` (Open/Set/Close/Commit + Read/Subscribe;
  one `produce` serves both read and push) and `Client` (idempotent fold to a
  `View`; `Cursor()` is the resume point). No I/O — pure, fully unit-tested
  (catch-up shapes, live deltas, close-patch, the LT-once invariant, and
  reconnect-converges-from-cursor).
- **`render`** — `Inline`: renders inline (no alternate screen). Closed messages
  seal to native scrollback **once** and are never redrawn; only the open message
  is a live region, so a terminal resize repaints just that bounded part. The
  immutability boundary (a sealed message) is the resize boundary — which is what
  makes the resize/duplication corruption unrepresentable. `FakeTerminal` is a VT
  mock for deterministic rendering tests.
- **`doc`** — block/delta model utilities (Phase-2 delta compression).

## In figaro

`internal/cli/livelog_bridge.go` (opt-in via `FIGARO_LIVELOG`) translates figaro's
wire ops into aria blocks and drives `Inline`, reusing figaro's own node renderers
so on-screen content matches the default painter. The server-side `aria.Server` +
a real `figaro.read(sinceLT)` RPC (true catch-up / a persistent `listen`) is the
next phase.
