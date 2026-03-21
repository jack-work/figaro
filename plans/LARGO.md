# Largo Integration Plan

> Streaming markdown rendering for figaro's CLI output.

## Overview

[Largo](https://github.com/jack-work/largo) is a lightweight streaming wrapper around [glamour](https://github.com/charmbracelet/glamour). It implements `io.Writer` — raw text is echoed immediately, then erased and replaced with styled markdown when a block boundary is detected.

Figaro's CLI is the integration point. The agent loop, RPC protocol, and figaro server are untouched.

## Integration Point

`cmd/figaro/main.go` → `mustPromptFigaro()` → `deliverEvent()`.

Currently:

```go
case rpc.MethodDelta:
    fmt.Print(p.Text)
```

## Changes

### 1. Create largo writer before the event loop

```go
sw, err := largo.NewWriter(os.Stdout, largo.Options{Margin: 4})
if err != nil {
    die("largo: %s", err)
}
```

### 2. Replace `fmt.Print` with `sw.Write` in `deliverEvent`

```go
case rpc.MethodDelta:
    sw.Write([]byte(p.Text))
```

### 3. Flush on boundaries

```go
case rpc.MethodToolStart:
    sw.Flush() // render pending markdown before tool output hits stderr
    // ... existing tool start handling ...

case rpc.MethodDone:
    sw.Flush()
    // ... existing done handling ...
```

Also flush on context cancellation / timeout in the `select` at the end of `mustPromptFigaro`:

```go
case <-ctx.Done():
    sw.Flush()
    fmt.Fprintln(os.Stderr, "\ninterrupted")
case <-time.After(120 * time.Second):
    sw.Flush()
    die("timeout waiting for response")
```

### 4. Add dependency

```bash
go get github.com/jack-work/largo
```

## What Stays the Same

- **Agent loop** (`internal/agent/`) — emits deltas, unchanged.
- **RPC protocol** — notification format unchanged.
- **Figaro server** — fan-out unchanged.
- **Tool output** — already goes to stderr, no interference with largo on stdout.
- **Content type routing** — thinking deltas already go to stderr via the existing `MethodThinking` case. Only `MethodDelta` text hits largo.
- **Other subcommands** (`list`, `kill`, `context`, `models`) — no rendering needed.
- **Multi-turn** — CLI process is ephemeral per prompt, so largo writer is naturally fresh each invocation.

## Scope

~10 lines of change in `mustPromptFigaro()`. No architectural changes.
