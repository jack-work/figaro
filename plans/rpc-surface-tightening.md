# RPC Surface Tightening

Shrink the figaro JSON-RPC surface to its minimum viable shape: five
methods, one dispatch interface, no compatibility shim. Authoritative
source for the desired endpoint is the `agent:` comment block at the
top of `serveConn` in `internal/figaro/protocol.go`.

## Final surface

Five methods on the figaro socket, all dispatched through a single
`AgentServer.Handler`:

| New constant         | Wire name              | Replaces                                 |
| -------------------- | ---------------------- | ---------------------------------------- |
| `MethodQua`          | `figaro.qua`           | `MethodPrompt` (`figaro.prompt`)         |
| `MethodSet`          | `figaro.set`           | (kept) — also absorbs set_model/set_label |
| `MethodReloadConfig` | `figaro.reload_config` | `MethodRehydrate` (`figaro.rehydrate`)   |
| `MethodInterrupt`    | `figaro.interrupt`     | (kept)                                   |
| `MethodChalkboard`   | `figaro.chalkboard`    | `MethodChalkboardSnapshot`               |

`MethodContext` and `MethodFigaroInfo` are out of scope for this
plan — they're already orthogonal and stay as-is. (If they should be
folded into `Chalkboard`, that's a separate plan.)

## Removals

- **`MethodSetModel`** — clients write `system.model` via `figaro.set`.
  The provider validates the model string at next send; on rejection
  the figaro shuts down (per the `agent:` block).
- **`MethodSetLabel`** — clients write `system.label` via `figaro.set`.
  Empty value removes the key. `Agent.SetLabel`/`SetModel` Go methods
  go away with the RPC handlers.

This is a **breaking wire change**. No shim, no version negotiation —
old clients fail loudly against new servers.

## AgentServer interface

New file `internal/figaro/server.go`:

```
type AgentServer interface {
    Handler(ctx context.Context, method string, params json.RawMessage) (any, error)
}
```

`Agent` implements it. `Handler` owns the method-string switch
(currently the inline `handlers` map in `serveConn`). `serveConn`
becomes a thin adapter that wires `jsonrpc.HandlerFunc` entries to
`a.Handler` — or, simpler, builds the handler map from a fixed slice
of `(method, fn)` pairs the interface exposes. Either shape is fine
as long as the dispatch logic leaves `protocol.go` and the
per-method JSON unmarshal lives next to its handler.

## CLI migration

Call sites that need updating live in `cmd/figaro/main.go` (no
separate `internal/cli/` package — the CLI is one big file):

- `main.go:405` — `fcli.PromptWithChalkboard` → rename to `Qua` on the client
- `main.go:1536` — same
- `main.go:569` — `fcli.SetLabel(ctx, label)` → replace with `fcli.Set(ctx, ChalkboardPatch{...system.label...})`
- `main.go:677`, `main.go:749` — `fcli.ChalkboardSnapshot` → `fcli.Chalkboard`
- `main.go:1093` — `fcli.Rehydrate(ctx, dryRun)` → `fcli.ReloadConfig(ctx, dryRun)`
- `main.go:1855` — usage string `figaro rehydrate` becomes `figaro reload-config` (subcommand rename optional but consistent)
- `main.go:1777` — comment mentions `figaro.set_model` / `figaro.set_label`; update wording

`internal/figaro/client.go` mirrors all five renames and drops
`SetLabel`. `internal/rpc/rpc_test.go:128` constant assertion gets
updated alongside `methods.go`.

## Iteration discipline

Every commit on this plan **must**:

1. Have a clear, scoped commit message (one rename or one removal per
   commit; no bundles).
2. Pass `go test ./...` cleanly **before** the commit lands.
3. Smoke-test end-to-end: `nix profile upgrade figaro && figaro rest && q "hello"`.
   If `q` doesn't get a response, the commit is bad — fix or revert.

Non-negotiable. The five-step sequence below is sized so each step
keeps the build green; if a step grows beyond one rename or one
removal, split it.

## Sequence

1. **Rename `Prompt` → `Qua`.** `MethodPrompt`, `PromptRequest`/`Response`,
   `Client.PromptWithChalkboard` → `Client.Qua`, handler entry,
   `rpc_test.go`, the two CLI call sites. Wire string becomes
   `figaro.qua`. Commit.
2. **Rename `Rehydrate` → `ReloadConfig`.** `MethodRehydrate`,
   `RehydrateRequest`/`Response`, `Agent.Rehydrate`,
   `Client.Rehydrate`, CLI subcommand + usage string. Wire string
   becomes `figaro.reload_config`. Commit.
3. **Rename `ChalkboardSnapshot` → `Chalkboard`.** `MethodChalkboardSnapshot`,
   `ChalkboardSnapshotResponse`, `Client.ChalkboardSnapshot`, two CLI
   call sites. Wire string becomes `figaro.chalkboard`. Commit.
4. **Remove `MethodSetModel`.** Drop the constant, request/response
   types, handler entry, `Agent.SetModel`, any CLI references.
   Migration path for callers: write `system.model` via `figaro.set`.
   Commit.
5. **Remove `MethodSetLabel`.** Drop the constant, request/response
   types, handler entry, `Agent.SetLabel`, `Client.SetLabel`. Update
   the `figaro label` CLI command to construct a `ChalkboardPatch`
   and call `figaro.set` (set `system.label`, or remove it on empty
   string). Commit.
6. **Extract `AgentServer`.** Create `internal/figaro/server.go` with
   the `AgentServer` interface and the dispatch logic. Slim
   `protocol.go`'s `serveConn` to listener + per-conn glue only —
   the handler map is assembled from `AgentServer`. Commit.

After step 6, `protocol.go` should be ~60 lines (listener, accept
loop, subscriber wiring) and `server.go` owns the JSON-RPC method
table.
