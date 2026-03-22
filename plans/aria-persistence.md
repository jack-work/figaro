# The Aria Persistence Plan

*"An aria that is not written down dies with the singer."*

## Concept

The **MemStore** is the hot, in-memory conversation. A new **FileStore** sits behind it as a WAL — not append-only, but a **full-state overwrite** on each flush. The figaro agent flushes to the FileStore at the end of every turn (when the LLM yields to the user). On restart, arias are discovered by scanning the WAL directory — any file there is an aria, whether or not a figaro is currently singing it.

The naming shift: what the user sees and manages are **arias** (persistent conversation states), not figaros (ephemeral processes).

---

## Part 1 — FileStore (new file)

**New file:** `internal/store/file.go`

A `Store` implementation that serializes a full `message.Block` as JSON to a single file, overwriting on each write. It needs:

- `NewFileStore(path string) *FileStore` — creates the store, loads from file if it exists
- `Context() *message.Block` — reads from the in-memory cache (loaded at construction or after last write)
- `Append(msg) (uint64, error)` — appends to the in-memory slice, then overwrites the file with the full slice
- `Branch`, `LeafTime`, `Close` — same semantics as MemStore
- The file format: a single JSON object with `{"next_lt": N, "messages": [...]}` — one atomic write, not JSONL. This avoids partial-line corruption.
- Write strategy: write to `<path>.tmp`, then `os.Rename` for atomicity.

The file path will be: `~/.local/state/figaro/arias/<aria-id>.json`

## Part 2 — MemStore wraps FileStore (decorator)

**Modify:** `internal/store/mem.go` (lines 1–65)

Currently MemStore is standalone. We change it to accept an optional downstream `Store`:

```go
type MemStore struct {
    mu       sync.Mutex
    messages []message.Message
    nextLT   uint64
    downstream Store  // nil = no persistence (test/crash mode)
}

func NewMemStore() *MemStore { ... }                          // existing, no downstream
func NewMemStoreWith(downstream Store) *MemStore { ... }      // new: seeds from downstream.Context()
```

- `NewMemStoreWith` calls `downstream.Context()` at construction to seed in-memory state, recovering the `nextLT` from the last message's LogicalTime + 1.
- `Append` writes to memory only (fast path). No downstream call here.
- New method: `Flush() error` — overwrites downstream with full in-memory state. This is called externally by the figaro agent, not internally.
- `Close()` calls `Flush()` then `downstream.Close()`.

The MemStore does **not** flush on every Append — that would be per-tic (including tool results, tool calls, etc.). The flush boundary is the **turn**, controlled by the caller.

## Part 3 — MemStore gains `Flush()` + `Clear()`

**Modify:** `internal/store/mem.go`

Add:

- `Flush() error` — if downstream is non-nil, push full state. The downstream FileStore overwrites its file.
- `Clear() error` — clears in-memory messages, resets nextLT, and if downstream is non-nil, removes the file. This is the cascade delete for `figaro fin`.

## Part 4 — Figaro Agent: embed aria ID, flush on turn end

**Modify:** `internal/figaro/agent.go`

**Config** (line ~37): add `AriaID string` and `StateDir string` (the `~/.local/state/figaro/arias/` directory).

**NewAgent** (line ~79): construct the FileStore with path `<StateDir>/<AriaID>.json`, then wrap it with `NewMemStoreWith(fileStore)`. The current `store.NewMemStore()` call on line ~93 becomes this two-layer construction.

**processPrompt** (line ~231): after the `ag.Prompt(ctx, text)` call returns (line ~259) and after token counting (lines ~270–278), call `a.memStore.Flush()`. This is the "turn boundary" — the LLM has yielded, all tool calls in this turn are done, the assistant's final response is in the store. One flush per turn.

**Kill** (line ~148): `a.memStore.Close()` is already called implicitly since we close resources — but we should ensure it. Add explicit `a.memStore.Close()` before closing the log file.

**New method on Agent:** `AriaID() string` — returns the aria ID. Also expose it in `FigaroInfo`.

**Crash recovery** (`runWithRecovery`, line ~175): when we reset the MemStore after a panic, we should create a fresh `NewMemStoreWith(fileStore)` rather than `NewMemStore()`, so the downstream file persists. Or, for simplicity in this iteration: reset with a bare `NewMemStore()` (losing the downstream link) and accept that a crash loses the aria. We can refine later.

## Part 5 — New method on Agent: `Fin()`

**Modify:** `internal/figaro/agent.go`

Add `Fin()` — calls `a.memStore.Clear()`, which cascades to deleting the file. Then calls `Kill()`. The aria is gone from disk and memory.

## Part 6 — FigaroInfo carries AriaID

**Modify:** `internal/figaro/figaro.go` (line ~55, `FigaroInfo` struct)

Add `AriaID string json:"aria_id"` field. The `Info()` method on Agent (line ~131) populates it.

## Part 7 — Aria discovery (list arias from disk)

**New file:** `internal/store/ariastore.go`

A utility that scans `~/.local/state/figaro/arias/` and returns metadata for each `.json` file:

```go
type AriaInfo struct {
    ID           string
    MessageCount int
    LastModified time.Time
}

func ListArias(dir string) ([]AriaInfo, error)
```

It reads each file just enough to count messages and extract the last timestamp. This is the **source of truth** for what arias exist — the registry only knows about live figaros.

## Part 8 — Angelus protocol: new RPC methods

**Modify:** `internal/rpc/methods.go`

Add constants (after line ~30):

```go
MethodAriaList   = "aria.list"
MethodAriaFin    = "aria.fin"
```

Add types:

```go
type AriaListResponse struct {
    Arias []AriaInfoResponse `json:"arias"`
}

type AriaInfoResponse struct {
    ID           string `json:"id"`
    MessageCount int    `json:"message_count"`
    LastModified int64  `json:"last_modified"`  // unix millis
    Sung         bool   `json:"sung"`           // true if a figaro is currently singing this aria
    FigaroID     string `json:"figaro_id,omitempty"`
}

type AriaFinRequest struct {
    AriaID string `json:"aria_id"`
}

type AriaFinResponse struct {
    OK bool `json:"ok"`
}
```

## Part 9 — Angelus handlers: aria.list, aria.fin

**Modify:** `internal/angelus/protocol.go`

In `NewHandlerMap` (line ~30), add:

```go
rpc.MethodAriaList: handler.New(h.ariaList),
rpc.MethodAriaFin:  handler.New(h.ariaFin),
```

**`ariaList`**: scans the arias directory via `store.ListArias()`, then cross-references with the registry to set the `Sung` flag and `FigaroID`. The registry needs a reverse lookup: aria ID → figaro ID. This requires either:
- A new method on Registry: `FindByAriaID(ariaID string) figaro.Figaro`
- Or we iterate `Registry.List()` and check each `FigaroInfo.AriaID`

The latter is simple and sufficient.

**`ariaFin`**: looks up whether a figaro is singing this aria. If so, calls `registry.Kill(figaroID)` (which calls `Kill()` on the agent). Then deletes the file from disk. If no figaro is singing it, just deletes the file.

The state dir path needs to be known by the handlers. Add it to `ServerConfig` (line ~23) or derive from config.

## Part 10 — Angelus client: Aria methods

**Modify:** `internal/angelus/client.go`

Add:

```go
func (c *Client) AriaList(ctx) (*rpc.AriaListResponse, error)
func (c *Client) AriaFin(ctx, ariaID string) error
```

## Part 11 — CLI: `figaro aria` (alias for list), `figaro fin`

**Modify:** `cmd/figaro/main.go`

In the dispatch switch (around line ~70), add:

```go
case "aria":
    runAriaList(loaded)
    return
case "fin":
    runFin(loaded)
    return
```

**`runAriaList`**: calls `acli.AriaList()`, renders a table:

```
ARIA       MESSAGES  LAST ACTIVE        SUNG  FIGARO
a3f9c2e1   24        2026-03-21 18:42   ♪     b7d4e8
c0ffee42   8         2026-03-20 14:15   -     -
```

The `SUNG` column shows `♪` if a figaro is actively bound to it, `-` otherwise.

The existing `figaro list` command (line ~194, `runList`) should be redirected to `runAriaList` — `figaro list` becomes an alias for `figaro aria`. This is the user-facing command.

**`runFin`**: calls `acli.AriaFin(ctx, ariaID)`. Usage: `figaro fin <aria-id>`.

## Part 12 — Create flow: aria ID = figaro ID (for now)

**Modify:** `internal/angelus/protocol.go` (line ~49, `create` handler)

When creating a figaro, the aria ID can simply be the figaro ID (the existing `uuid[:8]`). Pass it into `figaro.Config` as `AriaID`, and set `StateDir` to the arias directory.

In the future (Step 12 of the angelus plan), arias decouple from figaros — an aria can be reassigned to a different figaro from a pool. But for now, 1:1 is fine.

---

## File Map Summary

| # | File | Action | Key Lines |
|---|------|--------|-----------|
| 1 | `internal/store/file.go` | **CREATE** | New FileStore implementation |
| 2 | `internal/store/mem.go` | **MODIFY** | Lines 10–15 (struct), 19–22 (constructor). Add `NewMemStoreWith`, `Flush`, `Clear` |
| 3 | `internal/store/ariastore.go` | **CREATE** | `ListArias()` utility |
| 4 | `internal/figaro/figaro.go` | **MODIFY** | Line 55 (FigaroInfo): add `AriaID` |
| 5 | `internal/figaro/agent.go` | **MODIFY** | Line 37 (Config): add `AriaID`, `StateDir`. Line 93: two-layer store. Line ~259: flush. Line 148 (Kill): close store. New `Fin()` method |
| 6 | `internal/rpc/methods.go` | **MODIFY** | Lines 30+: add `MethodAriaList`, `MethodAriaFin`, request/response types |
| 7 | `internal/angelus/protocol.go` | **MODIFY** | Line 30 (handler map): add aria handlers. New `ariaList`, `ariaFin` methods |
| 8 | `internal/angelus/client.go` | **MODIFY** | Add `AriaList()`, `AriaFin()` |
| 9 | `cmd/figaro/main.go` | **MODIFY** | Line 70 (dispatch): add `aria`, `fin`. Line 194 (`runList`): redirect to aria list. New `runAriaList`, `runFin` functions |

---

## Execution Order

Three passes:

1. **Pass 1 — The store layer** (files 1, 2, 3): FileStore, MemStore decorator, aria scanner. Unit-testable in isolation with no daemon needed.

2. **Pass 2 — The agent wiring** (files 4, 5): Config plumbing, flush-on-turn, `Fin()`. Test with the existing agent_test.go mock setup.

3. **Pass 3 — The RPC + CLI surface** (files 6, 7, 8, 9): Wire it all through the protocol, client, and CLI. End-to-end testable.
