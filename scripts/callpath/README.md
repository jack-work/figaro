# callpath: the disk-to-wire enumerator

A tool, not a list. **If it cannot be re-run after a refactor, it is a snapshot
of someone's reading and it will be wrong within a day.**

    go run ./scripts/callpath -tree -entry "<substring>" -pkgs ./internal/... -algo cha
    go run ./scripts/callpath -entry "<substring>" -sink "<substring>" -algo vta

Its own module, so it adds no dependency to figaro's `go.mod`. Builds offline
from the module cache (`golang.org/x/tools`).

## WHAT IT PRINTS AND WHY EACH LINE EXISTS

Every one of these was paid for by a failure on 2026-08-18:

- **THE ALGORITHM AND ITS PRECISION.** A VTA edge and a CHA edge are different
  claims. CHA admits every implementation of the interface method; VTA narrows
  by value flow. They are never printed identically.
- **THE CUT**, pattern, tests on/off, package list. *Anything outside the cut
  is invisible, not absent.*
- **CALL PATHS ARE NOT DATA PATHS.** An absent path may mean the bytes crossed
  **by value**, a `Log` returned by `Open` and invoked from another stack has
  no call path from `Open` and is not unrelated. An empty result means one of
  three things and only one of them is a finding.
- **STATIC vs DISPATCH[n]**, with the **candidate set as inline children**
  (`[CANDIDATE k/n]`) at the reader's indent, never in a footnote.
- **[CONDITIONAL] vs [UNCONDITIONAL]** per frame, derived from the SSA CFG. The
  read path's syscall tail is conditional: a segment consults its resident
  payloads first, and only a MISS reaches `ReadFrame -> ReadAt -> pread`. A tree
  that draws the syscall unconditionally **asserts a call most reads never
  make**.
- **[GENERIC INSTANTIATION]** where one runtime frame appears as two symbols,
  and type arguments on the symbol, because `decodeRecord[message.Message]`
  and `decodeRecord[[]json.RawMessage]` are the fig IR channel and the
  translator channel, and printing them identically merges two different byte
  movements into one line.
- **[CYCLE -> depth N]** and **DEPTH LIMIT: subtree not walked, NOT ABSENT**, so
  the tree's depth is never an artifact of the flag.
- **[OPAQUE: …with the reason]** for frames with no SSA body. figwal is INSIDE
  the walk; OPAQUE is reserved for vendored SDKs and the net stack, where a CHA
  candidate set is noise wearing the costume of an answer.
- **LOG statements inline.** One `slog.Warn` was a third of a benchmark that
  night, and another made a fixture unparseable.

## WHAT IT WILL NEVER DO

**It cannot tell you whether bytes are COPIED, RESHAPED, or PASSED BY
REFERENCE.** That is the column a restructuring actually needs, no callgraph
infers it, and it must be filled BY READING and marked READ.

## HOLES, PRINTED AT THE END OF EVERY RUN

Reflection; function values in struct fields called later; callbacks registered
at init; anything outside the cut. **An enumeration whose gaps are unnamed is
worse than a short one whose gaps are listed.**
