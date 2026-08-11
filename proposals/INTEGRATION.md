# INTEGRATION: `rosina/integration`

Two branches that are **complements**, merged so the pair can be judged as one
thing. Neither is useful alone:

- **`feat/mouse-nodes`** (ROSINA): click a node to select it, click again to
  toggle its expansion. See **PROPOSAL-mouse.md**.
- **`feat/table-wrap`** (SUSANNA): markdown tables no longer lose text; prose
  whose collapsed render drops rows becomes *expandable*. See
  **PROPOSAL-table-wrap.md**.

SUSANNA's clamp introduces a collapsed table; the click is the only gesture that
opens one. In her words, recorded in her own document: *"a clamp with no key is
not on offer."* **Take both or neither.**

## What the merge actually required

One conflict git reported (`PROPOSAL.md`, add/add: both branches wrote one; they
are preserved side by side above) and **one it did not**:

`internal/cli/nodes.go` reported `Auto-merging` and produced a file with **two
definitions of `nodeExpandable`**: mine (the placeholder: tools only, exactly
what `toggleSelectedTools` tested inline) and hers (the real predicate, which
also answers for clipped prose). No textual conflict, no compile. Resolved by
deleting the placeholder and leaving a comment where it stood, because
*"auto-merging" is not a semantic claim* and the next reader deserves to know
this file was checked rather than trusted.

Everything else merged untouched: `livelog_bridge.go` (she edited 8 lines of
`ariaView`, I added two bridge methods), `transcript*.go` (mine alone),
`internal/render/*` (hers alone). The seam held: it was agreed by name and
signature *before* either of us built, which is why a two-agent change to the
same predicate cost one deletion.

## One behaviour change rides in on the merge

Her `nodeExpandable` tests `strings.TrimSpace(n.Output) != ""` where mine (and
the original inline test) had `n.Output != ""`. A tool whose entire output is
whitespace was expandable before and is not now. That is **her D7**, flagged in
her document as a decision for the owner, and it is noted here because it is the
only place the merge changes tool behaviour at all.

## State

`go build ./... && go vet ./... && go test ./...` green on the merge.
Combined-gesture pty verification: see the run recorded below by whoever ran it
last; the individual branches each carry their own captures.
