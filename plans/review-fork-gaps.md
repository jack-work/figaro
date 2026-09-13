# Fork-gap review

Reviewer: `27068b2c`. Worktree: `/home/gluck/dev/figaro-qua/review2`, branch `review/fork-gaps`.

## Method

The pane matrix uses a real Nix-built figaro and daemon, an isolated store, a private 110x34 tmux pane, and a local gateway that reads chunked requests. No credentials or production arias are used.

The parent has five completed turns. Turn 3 has three nodes: prose, a real bash call, and final prose. Each reply includes its request label, so wrong-aria content is distinguishable. Streaming tests hold an SSE response on an explicit event. Tool-running tests block bash on an owned FIFO and release it after the fork; they do not simulate a tool by delaying plain prose.

The driver records `FIGARO_MARKS`, a wire tape, gateway lifecycle events and captures per gesture. It reacts to file notifications rather than repeatedly querying daemon status. Each result includes the settled capture, the captures after release/G/coordinate jump, and the return hops. This sampling can miss a very short transient paint; a clean result means no sentinel was observed, not that every terminal frame was photographed.

Artifacts: `/var/tmp/review3-27068b2c/`. The driver and gateway are `matrix.py` and `gateway.py`; each arm has its own `matrix.json`, `marks.jsonl`, `wire.tape`, per-gesture captures and generated store.

## Pane results

Baseline is `2035b5d9`, the integration head containing the first head-cut and shelf fixes. The first corrected arm is `cf797d1e`. Extra node/stay cases also used `cf797d1e`. Tool-running cases used `ce50a45b`.

| Shape | Baseline | Corrected arm | What made an observed sentinel disappear |
|---|---|---|---|
| Idle `:fork` at head | none observed | none observed | n/a |
| Idle `:fork --stay`, then attend child | none observed | none observed | n/a |
| Idle shell head fork, then `:attend` | none observed | none observed | n/a |
| Idle `:fork parent:3` | persistent tail sentinel | none on cf797d1e | Baseline: G and explicit child-turn jump did not clear it; leaving for parent hid it; returning restored it |
| Idle shell `fork parent:3`, then `:attend` | persistent tail sentinel | none on cf797d1e | Same as preceding row |
| Idle `:fork parent:3.2` | none observed | none observed | n/a |
| Idle shell `fork parent:3.2`, then attend | not run | none on cf797d1e | n/a |
| Idle `:fork parent:3 --stay`, then attend | not run | none on cf797d1e | n/a |
| SSE-streaming parent, pager head fork | none observed | none observed | n/a |
| SSE-streaming parent, pager head `--stay` then attend | none observed | none observed | n/a |
| SSE-streaming parent, shell head fork then attend | none observed | none observed | n/a |
| SSE-streaming parent, `:fork parent:3` | sentinel on back-hop | none on cf797d1e | Leaving the child hid it; returning restored it |
| SSE-streaming parent, `:fork parent:3.2` | not run | none on cf797d1e | n/a |
| SSE-streaming parent, `:fork parent:3.1 --stay`, then attend | not run | none on cf797d1e | n/a |
| Fork of an interior fork | none observed | none observed | n/a |
| Bash running, pager head fork | not run | missing turn on ce50a45b | Parent tool completion did not fill it; G caused a history read which did |
| Bash running, pager head `--stay`, then attend | not run | missing turn on ce50a45b | Same as preceding row |
| Bash running, shell head fork then attend | not run | missing turn on ce50a45b | Same as preceding row |
| Bash running, `:fork parent:3` | not run | none on ce50a45b | n/a |
| Bash running, `:fork parent:3.2` | not run | none on ce50a45b | n/a |

Every matrix scenario also exercises `f j`, `a`, Ctrl-O and Tab/Ctrl-I after the fork. The baseline's interior forks repeatedly returned to the same gap on the back-hop; that behavior was absent in the cf797d1e rerun.

## Distinct failures and minimal reductions

### 1. A completed suffix did not clear the clone's tail uncertainty

Pane label: `the rest of this turn is not loaded`.

Minimal store sequence:

1. Fold complete parent turns 1 through 5.
2. Clone below turn 3. The clone correctly reports that content was dropped above it.
3. Fold the child's complete tail, turns 3 and 4, with `More.After=false`.
4. Query through the coordinate ceiling.

Before the fix, the query returned `{4,1}..{max,max}` even though the complete child was held. No code consumed the page's `More.After`. Filling that gap used its successor, which wrapped to the zero anchor and therefore read the tail again.

On the baseline matrix, marks recorded 39 history reads, 38 at anchor zero. The same matrix on cf797d1e recorded one history read and no anchor-zero reads.

The reduction was sent immediately to `3b805a8c`. Its fixes and canaries are `e3c46bd1` and `cf797d1e`: consume eligible tail knowledge, saturate the ceiling successor, clamp below the first turn, and assert that an idle retained pager asks for no phantom gap.

A later shelf guard, `fe992a96`, prevents adopting an uncertain tail with a zero-read seed plan. That closes the path where no page would arrive to correct the belief.

### 2. A partial page was allowed to speak for a whole stored message

Minimal sequence:

1. Fold one complete turn containing two nodes.
2. Fold a valid page containing only node zero, with `ClippedTail=true` and `More.After=true`.
3. Query through the ceiling.

On cf797d1e this recreated a phantom gap `{1,2}..{max,max}`. `TailFrom(1)` returns the START of the last message, not the highest covered node. The edge-authority comparison therefore accepted the partial page's statement as a statement about the store's tail.

The flag-aware model reduced this to bytes `[1,0,0, 0,1,0, 0,0,1]`. Sent immediately to the owner; `ce50a45b` adds `Store.Top` and `TestAClippedPageDoesNotSpeakForTheTail`.

### 3. Reversed parts exposed a false implementation of Page.Span

After adding reversed part order, the model found seed `[48,158,48]`: coverage remained correct, but edge knowledge differed because `Span` took the first and last slice elements rather than the extrema.

The owner clarified that ascending, non-overlapping parts are a wire invariant, while arbitrary PAGE arrival order is supported. Reversed parts were removed from the wire fuzzer after this clarification. The separate helper defect was nevertheless fixed in `c4158a0b`; its canary checks that `Span` describes the actual covered range rather than array order.

### 4. Head forks dropped a live turn and then read above it

Pane label: `1 turn not loaded`.

Minimal real reproduction:

1. Create five completed turns and listen to the parent.
2. Start turn 6 with prose and a bash call blocked on a FIFO.
3. While the tool runs, use `:fork -- child question`.
4. The child starts turn 7, but the pane contains turns 1-5 and 7 with a hole for turn 6.

The marks are explicit:

```
lineage: shared=7
clone:   kept=5
seed:    tail from7, parts=0
```

`CloneBelow` deliberately drops the live region. The divergence is an upper bound on what may be shared, not proof that all those turns were actually retained. A seed floored at 7 cannot recover the discarded live turn 6.

The hole remained visible during the settled observation and after releasing the parent's tool. G then caused a real history read at anchor 7, returning six parts; child turn 6 appeared, including its interrupted-tool result, and the gap filled. This was not merely scrolling the sentinel out of sight.

The bridge-level canary is `TestForkSeedIncludesUnretainedLiveTurn`: five closed turns, live turn 6, retarget with divergence 7, then fold only what the returned seed permits. Its failure was `seed {kind:1 from:7} skipped discarded live turn: gap {5,1}..{6,max}`.

Sent immediately to the owner. `7bf9a1c9` uses actual cloned coverage for the seed floor. `0d4ac217` schedules chasing a hole introduced by a live frame without waiting for another key. I pointed out that its initial no-key canary tested only gap detection, not scheduling; `8c9f4c24` replaces it with a notification-handler test that waits for the actual read and rejects the zero anchor.

All five FIFO-gated shapes were clean on the later pane reruns: no observed sentinel on fork, parent completion, or back/forward hops.

### 5. The renderer's gap iterator did not share Query's lower bound

Hold turn 2, set `More.Before=true`, and ask from `Anchor{}` through turn 2.

- `Query` returns a gap starting at `(1,0)`.
- `ForEachSegment`, which the renderer uses, returned one starting at `(0,0)`.

No range at turn zero is required: the iterator itself creates the invalid gap. `TestGapIteratorHonorsFirstTurnLikeQuery` demonstrates the disagreement and was sent to the owner. `8c9f4c24` factors the bounds into `Store.bound`, used by both walkers; the canary is in that commit and passes.

### 6. A live head fork advertised mutable content as shared

This was worse than a sentinel: the pane could look complete while holding the wrong tool result.

After the visible gaps were fixed, I extended the FIFO cases to record the lineage claim made during the hop, release the parent's tool, and compare the canonical nodes of parent and child below the claimed divergence. At `8c9f4c24`:

| Head-fork entry point | Claimed divergence | Turn incorrectly called shared | Parent after release | Child |
|---|---:|---:|---|---|
| `:fork` | 7 | 6 | tool OK, TOOL_DONE, closing prose | interrupted-tool error, no closing prose |
| `:fork --stay`, then attend | 8 | 7 | same successful result | same error repair |
| Shell fork, then attend | 9 | 8 | same successful result | same error repair |

The source had stopped inside its final turn, so that turn was still mutable. A past-the-end LT is not a sealed coordinate and must not be memoized as one. The floor repair filled the missing turn but could not make the falsely broad sharing claim true. After enough hops, retaining that repaired child turn into the parent would fabricate history.

Sent immediately to the owner. `77dbc63a` conservatively excludes the last turn when the base is beyond the current log, and caches only exact sealed-coordinate answers. An idle head fork may re-read one extra turn; that is preferable to retaining the wrong content.

`2449b10f` adds `TestLineage_EveryTurnItCallsSharedIsByteIdentical`, which asks for lineage while the parent is still working, then compares after the parent succeeds and the child repairs. My final real-pane arm uses that head and compares EVERY turn below the initial claim, not merely a fresh claim after completion.

## Model fuzzing

`FuzzForkCloneReadCoverage` maintains a separate map of exactly held `(turn,node)` payloads and a separate `More` state. Operations clone into a new branch, fold actual paginated reads in arbitrary arrival order, and evict below random anchors. It checks:

- held data is neither lost nor replaced by another branch's payload;
- a held coordinate is never reported as a gap;
- page edge knowledge is adopted only when the page reaches the relevant held edge;
- a complete source has no phantom trailing gap;
- the renderer iterator never produces a gap below turn 1;
- inquiry-only turns occupy one anchor despite having no nodes.

The first coverage-only arm completed 32,953 sequences in 20 seconds. Adding edge knowledge immediately found failure 2. Adding reversed parts found failure 3; that permutation is now outside the wire model, while out-of-order page arrivals remain. After the edge and iterator fixes, the expanded model completed 19,609 sequences in 30 seconds with no mismatch. That arm includes inquiry-only turns, independent edge knowledge, and checks of both query paths.

The model has a bounded input length of 300 bytes. It models sealed page coverage and clone/eviction operations; live-turn discard is covered separately by the bridge canary and the real FIFO-gated pane cases. It is not a claim about arbitrary malformed frames or every renderer styling combination.

## Final verification

The full eleven-scenario pane matrix was repeated on `77dbc63a`: no observed sentinel in any case or its return hops. On `2449b10f`, all five FIFO-gated cases were repeated with the lineage claim captured BEFORE releasing the parent:

| Case | Initial divergence | All canonical nodes below that claim equal after completion | Sentinel |
|---|---:|---|---|
| Pager head | 6 | yes | none observed |
| Pager head with `--stay`, then attend | 7 | yes | none observed |
| Shell head, then attend | 8 | yes | none observed |
| Pager turn cut | 3 | yes | none observed |
| Pager node cut | 3 | yes | none observed |

The parent completed through its live agent, not an obsolete pre-fork log handle. Both that detail and asking lineage before completion matter: otherwise this test could pass without exercising the unsafe claim.

Nix build, vet and `go test -count=1 ./...` pass on `2449b10f` plus the new model fuzzer. The reductions sent during the review have canaries on the owner's branch. I kept the model fuzzer here and did not duplicate the canaries it adopted.

## Evidence and scope

- `matrix-before`: baseline 2035b5d9.
- `matrix-fixed`: cf797d1e, same eleven-scenario matrix.
- `matrix-extra`: cf797d1e, extra node/stay scenarios.
- `matrix-tools-before`: ce50a45b, five FIFO-gated tool scenarios.
- `matrix-tools-fixed`: 0d4ac217, visible tool-gap fixes.
- `matrix-tools-lca-before`: 8c9f4c24, canonical mismatch despite no visible gap.
- `matrix-tools-final`: 77dbc63a, tool cases after conservative lineage.
- `matrix`: 77dbc63a, final eleven-scenario rerun.
- `matrix-tools-verified`: 2449b10f, final tool cases comparing every turn below the initial claim.
- `logs/clone-gap.log`, `model-flags.log`, `fuzz-edges.log`, `live-seed.log`, `gap-iterator.log`: reductions and their exact failures.
- Each pane arm contains `marks.jsonl`, `wire.tape`, generated canonical data, and per-action captures. No production store was used.

This review found two kinds of sentinel that should not be conflated: an unfillable phantom beyond a complete tail, and a real missing live turn skipped by the seed. The first needs correct edge knowledge; the second needs a floor derived from actual retention and a fill request triggered by incoming content. Fixing only the text of the sentinel or forcing a repaint would address neither.
