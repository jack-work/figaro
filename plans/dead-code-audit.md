# Dead Code Audit — figaro Go codebase

Goal: prune orphaned exported surface flagged in `internal/figaro/agent.go`, then run a general `deadcode -test ./...` pass and triage the rest.

## Tooling

```sh
go install golang.org/x/tools/cmd/deadcode@latest
# ensure $(go env GOPATH)/bin is on PATH
deadcode -test ./...
```

## Current build state (blocker)

The repo does **not** currently build. A partial refactor removed `Agent.Prompt(text string)` (now commented out at `internal/figaro/agent.go:295`) but left every caller and the `Figaro` interface contract behind. `deadcode` cannot produce useful output until the build is green, so step 1 is a forced cleanup, not a discretionary deletion.

Failing references (from `go build ./...` and `grep`):

- `internal/figaro/figaro.go:30` — `Figaro` interface still declares `Prompt(text string)`
- `internal/figaro/protocol.go:200` — `var _ Figaro = (*Agent)(nil)` compile-time check
- `internal/angelus/protocol.go:227,486,495` — passes `*figaro.Agent` as `figaro.Figaro`
- `internal/angelus/registry_test.go:25` — `mockFigaro.Prompt` method on test mock
- `internal/figaro/agent_test.go` — 14 call sites
- `internal/figaro/translator_integration_test.go` — 2 call sites
- Plus pre-existing breakage in `internal/figaro/inbox.go` / `inbox_test.go` (`NewInbox` undefined, unused import) and `agent.go:206` `NewInbox2` generic mismatch

## Plan

### Iteration 1 — restore build, drop `Agent.Prompt`

`Prompt` is referenced only by tests and the `Figaro` interface that exists to support tests/mocks. Production code calls `SubmitPrompt(rpc.PromptRequest)` (`agent.go:302`). Disposition: **delete**.

Steps:
1. Remove the commented stub at `agent.go:295-299`.
2. Remove `Prompt(text string)` from the `Figaro` interface (`figaro.go:28-30`).
3. Update test call sites to `a.SubmitPrompt(rpc.PromptRequest{Text: "..."})` in `agent_test.go` (14 sites) and `translator_integration_test.go` (2 sites).
4. Drop `mockFigaro.Prompt` in `internal/angelus/registry_test.go`.
5. Fix `inbox.go` unused `encoding/json` import and resolve `NewInbox` vs `NewInbox2` (separate concern surfaced by the same build break — flag for follow-up if not trivially fixable in this iteration).
6. `go test ./...` — must be green.
7. Smoke: `nix profile upgrade figaro && figaro rest && q "hello"`.
8. Commit: `chore(figaro): drop dead Prompt method and interface entry`.

### Iteration 2 — audit `Agent.SocketPath()`

Current callers (post-build):
- `internal/figaro/agent_test.go:163` (`TestAgent_SocketPath`)
- `internal/angelus/protocol.go:361` — `f.SocketPath()` on `Figaro` interface, used to populate `Address` in a registry response

Production use exists (angelus protocol), so **keep** the method. The agent comment at `agent.go:249` is stale — remove the comment. The `Figaro` interface entry stays.

Commit: `chore(figaro): drop stale SocketPath comment`.

### Iteration 3 — general `deadcode -test ./...` pass

Run from repo root after iteration 1 lands:

```
deadcode -test ./...
```

**Output: TBD — capture verbatim once the build is green.** Expected format is one `pkg.Func` per line.

Triage rubric for each entry:
- **delete** — no callers, no interface implementation, no reflective/RPC use, no future-staged plan referencing it.
- **move to `_test.go`** — only test code reaches it; convert to a test helper in the same package (e.g. `func newTestX(...)`).
- **keep** — exported API consumed by external clients, satisfies an interface, used via reflection/JSON-RPC dispatch, or named in an active plan. Justify in the commit message.

Each disposition becomes its own commit. Examples:
- `chore(figaro): drop dead helper foo`
- `refactor(store): scope NewMemStreamForTest to _test.go`
- `chore(angelus): keep ServeHTTP — used by net/http reflection (justify)`

## Iteration discipline (REQUIRED)

Every iteration:
1. Make exactly one logical change. **No bundling** — one removal, one commit.
2. Run `go test ./...` — all tests must pass before commit.
3. End-to-end smoke: `nix profile upgrade figaro && figaro rest && q "hello"` — must round-trip a response.
4. Commit with a clear scoped message (`chore(<pkg>): drop dead <thing>` or `refactor(<pkg>): scope <thing> to test helper`).
5. Only then move to the next entry.

If a removal turns out to break something not caught by tests, revert that single commit and re-triage — don't unwind the whole audit.

## Out of scope

- Linting unused parameters / locals (handled by `go vet` and `staticcheck`, separate concern).
- Reachability through `cmd/figaro` build tags or alternate build configurations — `deadcode -test ./...` covers the standard build.
- The `inbox.go` / `NewInbox2` generics mismatch is a real bug surfaced incidentally; track separately if it doesn't fall out of iteration 1.
