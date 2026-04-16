# Graceful Rest

> *"Figaro a riposo — ma con dignità."*

## The problem

`figaro rest` currently SIGTERMs the angelus immediately. This is unsafe
when:

- An agent is mid-turn (LLM streaming, tool executing) — the turn dies
  mid-flight, stores may not flush, `stream.done` is never emitted.
- A CLI is actively connected waiting on `doneCh` — it hangs forever
  because the agent socket closes without a terminating notification.
- The rest command itself was issued from *inside* a figaro's tool
  call: the CLI kills the angelus that hosts the figaro that is
  running the CLI's bash tool. Fratricide. Observed in practice:
  "figaro rested itself and hung."

## Design

### 1. Angelus: graceful shutdown on SIGTERM

Signal handler (already wired via `signal.NotifyContext` in
`runAngelus`) should trigger a shutdown sequence instead of just
cancelling the top-level context:

1. Mark registry as **draining** — refuse new `figaro.create` requests
   with a clear error (`angelus: shutting down`).
2. For each active figaro:
   - Call `Agent.Interrupt()` (already exists from the interrupt work).
   - Wait up to `shutdownGrace` (default 5s) for the figaro's inbox
     to report idle, or for its `done` channel to close.
3. For each figaro (idle or grace-expired):
   - Call `Agent.Kill()` — flushes MemStore → FileStore, closes log,
     closes socket.
4. Close the angelus socket listener.
5. Remove `angelus.pid` + `angelus.sock`.
6. Exit 0.

**Grace timeout is per-figaro**, not global — one slow figaro
shouldn't starve the others.

### 2. CLI: detect dead agent sockets

In `mustPromptFigaro`, the `jsonrpc.Client` read loop already hits
EOF when the agent socket closes. Today that kills the goroutine
silently; the main goroutine still waits on `doneCh`.

Fix: wire a `connClosed chan struct{}` into `jsonrpc.Client` (or
expose the existing read-loop termination). The select at the end
of `mustPromptFigaro` becomes:

```go
select {
case <-doneCh:
    // clean finish
case <-connClosed:
    die("agent disconnected before turn completed")
case <-ctx.Done():
    // interrupt path (already handled)
}
```

No more infinite hangs when the agent dies.

### 3. `figaro rest` policy

- `figaro rest` — default. Sends SIGTERM, waits up to ~6s for the
  socket file to disappear. Prints "angelus rested" or "angelus did
  not rest within 6s; try --force".
- `figaro rest --force` — sends SIGKILL immediately. Blunt but
  always works.

The CLI side also removes `angelus.sock` as a fallback (already does).

### 4. Self-defense (optional, later)

When the CLI sees `figaro rest` being invoked, it could check whether
`os.Getppid()` is bound to a figaro in the angelus we're about to
kill, and warn / refuse. Not critical once steps 1 and 2 are done —
graceful shutdown makes the self-kill merely inconvenient, not
catastrophic.

## Implementation order

1. **CLI connClosed detection** — smallest, immediate UX win. Even
   without graceful angelus shutdown, `figaro rest` from inside
   a tool call stops hanging; it just exits with a clear error.
2. **Angelus graceful shutdown** — the main event. Requires small
   changes in `internal/angelus/angelus.go` (signal handler),
   `internal/angelus/registry.go` (drain flag), and
   `internal/angelus/protocol.go` (refuse create when draining).
3. **`--force` flag** — trivial.

## Out of scope

- Persistent shutdown state across restarts — arias already handle
  conversation persistence; the angelus itself is stateless on disk.
- Client reconnection to survive angelus restart — a feature, not a
  fix. Someday maybe.
