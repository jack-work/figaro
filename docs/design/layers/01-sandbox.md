# Layer 1: tools + sandbox

## For Gluck (phone page)

**What it is.** The code that runs a tool call: `internal/tool`, `internal/toolout`, `internal/sandbox/local`, `internal/sandbox/box`. 3,912 lines. It already talks to the rest of figaro only through `api/sandbox` (its own go.mod), with two exceptions.

**The pinch.** A rule that nothing outside the host imports `internal/sandbox/*`, and that tools reach a sandbox only through `api/sandbox`. Two edges to cut:

1. `internal/tool` aliases `sandbox/local.Host` as its session registry (registry.go:131-135). Fix: the tool takes `api/sandbox.Sessions`, and the host passes in the local one.
2. `internal/cli/box.go` runs the box server (`figaro box serve`), so the client imports `sandbox/box`.

**Cost.** Two small commits plus the rule. No runtime change, nothing on disk.

**Hidden variables I expect.** Process sessions may lean on local-only methods beyond the `Sessions` interface. The box server shares code with the local sandbox, so a figbox binary would duplicate or move it.

**Decisions (one line each):**

- **A.** `figaro box serve`: keep it in the figaro binary (one allowed edge, written down), or move it to a separate `figbox` binary (a new nix output and install step)? I suggest keeping it for now.
- **B.** RunAs for tool calls (#38): leave it out of this pinch, so the pinch stays pure motion? I suggest yes, leave it out.
- **C.** Go ahead with this layer first?

## Ledger

- Status: page drafted 2026-11-10, waiting on Gluck.
- Rulings: none yet.
- Open questions: A, B, C above.
- Hidden variables surfaced: none yet. Already done before this lane: the bwrap provider was deleted (c8c030c3).
- Rollbacks: none.
