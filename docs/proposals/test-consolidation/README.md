# Test consolidation: delete duplicate contracts, shrink the expensive gate

Proposal for Gluck to comment on. Owner: Figaro Engineer. Branch `proposal/test-consolidation`, worktree `/var/tmp/figaro-test-consolidation`. No new worker, production change or broad deletion has been started for this proposal.

The earlier 26-line deletion was not a campaign. This is the concrete inventory, first deletion list and ordered plan. Large refactors remain held until Gluck comments. The queue boundary fix shipped separately in 0.69.8 with affected, race and full Go/flake gates.

## Baseline and limits of the numbers

Source: main `5bb56f876` (0.69.8). Parsed all Go test files with Go's AST: **83 package directories, 1,466 files, 215,877 test-file lines, 5,332 top-level Test-prefixed function declarations**. This is a source inventory across build tags/platforms, not a claim that every declaration executes on gluck. It excludes TestMain, counts a table-driven test once, and does not inflate counts with generated subtests. Benchmark and fuzz declarations are separate in the CSV.

Runtime: the existing immutable 0.69.8 final Go gate, `go test -count=1 -timeout 25m ./...`, with the observed package seconds from its output. Affected race tests ran earlier in the same gate script. No new broad suite was run to write this proposal. Package times include setup, subprocesses and tests, and overlap under Go's package concurrency. They cannot be summed as suite wall time. Individual-test timings were NOT captured, so I do not invent per-test speed savings.

The Go/race/build/vet gate wall time was **8m43s**; the separate full flake gate was **15m55s**. The CLI package alone was **448.623s**, the dominant package on the critical path. Its 1,466 test declarations and 57,593 source lines are a much bigger target than deleting a handful of nanosecond helper assertions.

Raw reproducible inventory is [inventory.json](inventory.json), [inventory.csv](inventory.csv), and the captured package durations are [package-runtime.json](package-runtime.json). The complete human-readable [package table](package-table.md) includes every package with test files and explicitly labels absent timing as not measured.

### Main concentrations

| Package | Test declarations | Test lines | Measured package seconds |
|---|---:|---:|---:|
| internal/cli | 1,466 | 57,593 | 448.623 |
| internal/angelus | 700 | 32,641 | 32.119 |
| api/form | 110 | 3,831 | 21.559 |
| figwal/xwal | 261 | 15,248 | 17.641 |
| internal/store | 387 | 20,400 | 17.100 |
| internal/figaro | 248 | 12,984 | 14.485 |
| internal/tool | 100 | 1,974 | 13.870 |
| internal/provider/anthropic | 98 | 4,370 | 12.724 |
| internal/provider | 102 | 4,873 | 9.776 |
| figwal/crashtest | 4 | 569 | 8.746 |
| internal/provider/routed | 81 | 3,549 | 4.969 |

## Wave 1: delete the exact weak or duplicate assertions

These are proposed deletions, not rewritten fixtures. The line counts below are the current function bodies including their signature/closing brace, not promised whole-file deletion counts. Comments and unused helpers may also disappear.

| Delete | Function lines | Why | Behavior retained |
|---|---:|---|---|
| `internal/figaro/projector_boundary_test.go:TestNilProjectorIsLegal` | 6 | Declares a nil local interface and asserts it is nil. It never constructs an Agent; it proves nothing about the advertised boundary. | Real Agent construction and projection behavior tests; keep the separate dependency check until the boundary ratchet covers that edge. |
| `internal/figaro/agent_test.go:TestAgent_ID` | 5 | Boots an agent only to assert its ID is nonempty. Real backend/Agent tests already create and use addressed agents. | Prompt/subscribe and lifecycle behavior tests. |
| `internal/cli/interrupt_exit_test.go:TestRawPathUsesTheSameConstant` | 5 | Repeats `interruptExit(true)==130` and `exitInterrupted==130`, already checked in `TestInterruptExit`. | `TestInterruptExit` unchanged. |
| `internal/figaro/prompt_merge_test.go:TestMergePromptEvents_JoinsWithABlankLine` | 13 | Helper-level duplicate of the real Inbox drain row `TestDrain_QueuedRunBecomesOneInquiry`, which checks the exact same blank-line result plus queue exhaustion. | Retain the Inbox row and the new actual Responses wire boundary regression. |

**First tranche: 4 test declarations / 29 function lines**, plus removable comments. Combined with the already shipped optional-interface test removal: 5 declarations and at least 55 lines. This is deliberately honest: it reduces noise, not the eight-minute bottleneck. No latency claim from this tranche.

Do not delete the empty-input case from `TestMergePromptEvents_EmptyAndSingle`, form merge order, or no-mutation assertions merely because other queue tests exist: those are distinct conditions. Likewise keep the new queued-message separator/prefix stability rows while diagnosing this actual bug.

## Wave 2: remove redundant keymap snapshots, retain execution coverage

The current CLI has three golden fronts (compiled table, pager behavior, input behavior), hand-maintained duplicate lists, docs parity checks and dispatch/index checks. Not every one is needed to guard the same mapping.

Proposed first two deletions:

| Candidate | Function lines | Reason and retained oracle |
|---|---:|---|
| `TestOpensTranscript_MatchesTheHandKeptList` in keymap_test.go | 35 | A second handwritten opener list becomes a maintenance gate whenever a legitimate key is added. Keep `TestKeymap_OpenersReallyOpenThePager`, `TestKeymap_InlineKeysStayInline` and the pager behavior golden: they actually dispatch keys. |
| `TestHelpBody_MatchesTheOldHandWrittenPanel` in keymap_test.go | 77 | Pins the entire old prose panel glyph-for-glyph. A documentation wording change is not a keybinding regression. Keep coverage that every visible binding is documented, hidden bindings stay hidden, and the panel renders the table. |

**Second tranche: 2 declarations / 112 function lines**, with no new replacement golden. Wave 1 + these two = **6 proposed declarations / 141 function lines**, plus the shipped 26-line deletion = **167 lines** before unused helpers/comments. Function-body counts alone are not measured runtime savings.

Next consolidation, after Gluck's comments: `TestKeymap_IndexAgreesWithTheTable` (39 lines) and `TestKeymap_ActionArraysAgreeWithTheIndex` (19) inspect closely related internal lookup structures. Keep one invariant at the lowest owner and the behavioral dispatch sweep; remove the repeated index/array view only after proving which assertion is unique. Do not combine these by growing an omnibus test.

The compiled golden test is only three lines but its fixture is a separate snapshot of the key table. Review whether it adds any independent contract beyond row validation + actual dispatch goldens. If not, delete that golden and its generation machinery rather than refreshing three snapshots for one binding change. This decision is explicitly open; it is not approved by a generic request to reduce test count.

## Wave 3: attack the 448-second CLI package, not just line counts

Preserve real TTY testing. We have already shipped bugs that model-only tests missed. The goal is to remove repeated scenarios/setup, not make the suite green by deleting the only real seam.

Concrete families to consolidate:

1. `restart_bindings_e2e_linux_test.go` (`TestEveryRestartKeepsBindings`, `TestStopForgetBindingsStartsTheNextDaemonUnattended`, `TestARespawnedPaneTakesTheAttendanceUpgradeHandsIt`) and `listen_reconnect_e2e_linux_test.go:TestListenFollowsItsAriaAcrossEveryRestart`. One shared compiled binary, isolated per-case daemon roots, and one restart matrix should distinguish binding continuity from stream continuity. Remove duplicate restart modes that assert no independent result. Keep forgotten attendance and pane reattendance as separate outcomes.
2. `restart_delivery_e2e_test.go`: queued delivery after graceful stop and crash; tool interruption after crash; stop-then-crash resume-once; user hangup and stop-hup. Enumerate the unique failure boundary for each. Merge repeated setup/waiting, not the one-resume/no-replay assertions. These are not all duplicates, and deletion must name the retained row.
3. `keymap_recipes_test.go`: recipe execution, example-file parity and docs table checks overlap a single recipe source. Retain executable examples and public docs coverage once; remove repeated string pinning when it adds no behavior.
4. `pager_chase_settles_test.go`: the two tiny wrappers may share a scenario helper, but live-follow and deterministic replay have different clock semantics. Keep both outcomes unless the actual path is identical. Short source length alone is not a reason to remove one.
5. Pure transport/projection serialization tables should run at their owning packages, not as many CLI daemon processes. Move a scenario only when the current CLI assertion is purely an output transformation already exercised below; keep a small real end-to-end row proving wiring.

First measurement for this wave uses one targeted `go test -json` on those named CLI families, not another whole repository gate. Record per-test elapsed and child-build/setup costs before edits, then the same command after. The current logs provide package totals only. No honest speedup target can be promised until that row exists.

Proposed success criterion: fewer duplicate restart/TTY setups and a measured reduction on the same named family, while keeping every unique crash, binding, queue delivery and cancellation boundary. Do not trade away secret-leak, data-loss, authorization or unsafe-generation replay rows for a stopwatch result.

## Wave 4: settle test ownership after the history/log carve

The ordered carve is already shipped in 0.69.7, not proposed again. Branch `lane/history-log-carve`; implementation commits `c3448294d` and `7b19cdb6b`; plan remains `~/.local/state/impresario/drafts/lineage-log-carve-plan.md`. Its existing ledger is `docs/design/layers/02-provider-store.md`.

It moved stored ancestry/contributing ranges to figwal and typed logs to arialog, removed provider/decide imports of store, and deleted the duplicate store splitter. Production Go was net -25 lines. The test declaration count was unchanged by the carve; the +275 test source lines were mostly longer typed imports/type qualifications, not 275 new tests. The later 26-line deletion was a separate action.

Now put each contract at one owner:

- `figwal/xwal`: ancestry and contributing-range rule, root/equal/interior-fork behavior and missing-node contracts.
- `figwal/tree`: coordinate splitting and cache residency, not provider policy.
- `internal/arialog`: typed Entry/Log/MemLog and generic traversal semantics.
- `internal/store`: concrete adapter/channel-coordinate semantics, especially LT versus FigaroLT and migrations; keep a small adapter parity row, not a clone of every generic log case.
- `provider/decide`: encodings, capability and request policies, not store implementation identities.

The moved MemLog concurrency test is not duplicated in store now. Next inspect `TestScanIsReadWithoutTheArray`, `TestXwalLogScanMatchesRead`, `TestTreeLogAnswersLikeItsSubstrate` and `treelog_compat_test.go`. They sound related but cover fallback traversal versus concrete XWAL versus shared-cache view. Extract the genuinely common assertions once; delete duplicates, preserving channel/fork distinctions. Do not flatten them into one test that loses the ability to locate a failing owner.

No new large carve is authorized here. The next production carve must come back as one bounded proposal, net-negative implementation lines, with Gluck's comment first. This campaign works on the existing separation, not another architecture commission.

## Blocking tests: delete false contracts, not failing evidence

A test that requires stale formatting, a duplicate hand-kept list, or a local type identity with no user behavior is a deletion candidate. It should not be rewritten every time code moves.

A failed test is not automatically bad. In the previous release, base/candidate comparison showed a real pager-height regression from an added status row. The correct fix was the production display seam, not deleting the golden. Likewise the actual queue-separator and no-replay tests exercise requested defects. Preserve them until the underlying behavior is intentionally changed.

For genuine preexisting flakes, name exact test/base evidence once. Either repair the concrete product race when it is real, or remove an invalid timing oracle with a written reason. No infinite canary/review ceremony, and no relabeling a regression as a baseline exception to ship it.

## Ordered delivery and accounting

1. **Now, after Gluck comments:** Wave 1 deletions as a test-only commit on a clean branch. One affected test run. Report exact declarations/lines removed; no production release solely for cosmetic deletion.
2. **Next:** the two Wave 2 deletions, then the shared-index/compiled-golden decision. Keep functional goldens and actual TTY path. Again one affected run, not a broad proof cycle.
3. **Then:** targeted JSON timing of the named restart/reconnect families; one consolidation patch at a time. Before/after commands and actual elapsed, no source-line-to-time conversion.
4. **After that:** typed-log test ownership cleanup at the moved layer boundaries. No store format changes, migration changes, new fixture factory or broad test framework.
5. **Ongoing:** every feature patch names its actual behavioral regression once. New test count is not a success measure. Delete obsolete scaffolding in the same patch; do not create a permanent mirror oracle for moved types.

Report per tranche: deleted/merged declarations, test source change excluding moved-code inflation, unique behavior retained, exact affected command, measured elapsed where available, flake count observed versus fixed, and any production delta. No estimated dollar or inference savings. The passive hourly collector and queue fix remain independent feature deliveries.

## Decisions requested on this page

- Approve the six exact deletions in Waves 1 and 2 (141 function lines), keeping the named behavior checks?
- Prefer one behavior golden per input surface plus row validation, allowing deletion of the compiled snapshot if its unique coverage is empty?
- Approve one targeted restart-family timing/consolidation pass as the first performance tranche, rather than another broad refactor?

Comments here are the approval record. No blind bulk deletion or new worker is implied.

## Appendix: all package counts and measured runtime

| Package | Tests | Files | Test lines | Package seconds |
|---|---:|---:|---:|---:|
| `internal/cli` | 1466 | 386 | 57,593 | 448.623 |
| `internal/angelus` | 700 | 223 | 32,641 | 32.119 |
| `api/form` | 110 | 30 | 3,831 | 21.559 |
| `figwal/xwal` | 261 | 71 | 15,248 | 17.641 |
| `internal/store` | 387 | 133 | 20,400 | 17.1 |
| `internal/figaro` | 248 | 84 | 12,984 | 14.485 |
| `internal/tool` | 100 | 15 | 1,974 | 13.87 |
| `internal/provider/anthropic` | 98 | 32 | 4,370 | 12.724 |
| `internal/provider` | 102 | 37 | 4,873 | 9.776 |
| `figwal/crashtest` | 4 | 3 | 569 | 8.746 |
| `figwal/log` | 22 | 6 | 1,059 | 7.701 |
| `figwal/tree` | 25 | 12 | 1,541 | 7.398 |
| `internal/formsim` | 15 | 4 | 1,407 | 7.379 |
| `internal/sandbox/local` | 15 | 4 | 435 | 6.533 |
| `internal/authz` | 162 | 34 | 5,529 | 5.832 |
| `internal/provider/routed` | 81 | 29 | 3,549 | 4.969 |
| `internal/upstream` | 19 | 8 | 975 | 3.962 |
| `internal/formgen` | 10 | 2 | 294 | 3.112 |
| `internal/render` | 54 | 15 | 1,216 | 3.075 |
| `internal/daemon` | 58 | 20 | 2,424 | 2.12 |
| `internal/outfit` | 55 | 16 | 1,795 | 1.797 |
| `internal/peer` | 39 | 9 | 1,445 | 1.756 |
| `internal/methods` | 20 | 2 | 513 | 1.526 |
| `internal/fuzzy` | 20 | 2 | 536 | 1.225 |
| `internal/wirelog` | 26 | 5 | 883 | 1.04 |
| `internal/auth` | 84 | 21 | 2,115 | 1.039 |
| `internal/otel` | 23 | 7 | 672 | 1.037 |
| `internal/troupe` | 63 | 10 | 1,787 | 0.931 |
| `internal/topo` | 9 | 3 | 229 | 0.928 |
| `internal/sandbox/box` | 18 | 4 | 715 | 0.869 |
| `api/handle` | 7 | 1 | 135 | 0.661 |
| `api/rpc` | 57 | 14 | 1,341 | 0.443 |
| `sdk/aria` | 44 | 13 | 1,653 | 0.425 |
| `internal/boot` | 55 | 15 | 1,393 | 0.408 |
| `figwal/segment` | 35 | 11 | 1,239 | 0.361 |
| `internal/actor` | 18 | 2 | 478 | 0.329 |
| `internal/modelcat` | 19 | 1 | 563 | 0.327 |
| `figwal/disk` | 63 | 13 | 2,455 | 0.305 |
| `internal/provider/copilot` | 15 | 6 | 888 | 0.305 |
| `internal/workerpool` | 15 | 4 | 700 | 0.304 |
| `internal/livelog/aria` | 69 | 19 | 2,806 | 0.211 |
| `internal/figaro/wire` | 23 | 3 | 657 | 0.171 |
| `internal/compose` | 38 | 17 | 1,592 | 0.169 |
| `internal/decide` | 46 | 5 | 1,090 | 0.163 |
| `api/transport` | 11 | 3 | 284 | 0.138 |
| `internal/provider/responses` | 69 | 17 | 2,964 | 0.119 |
| `internal/tokens` | 14 | 1 | 174 | 0.104 |
| `internal/catalog` | 10 | 1 | 273 | 0.096 |
| `internal/formdelta` | 10 | 1 | 461 | 0.086 |
| `internal/config` | 78 | 19 | 1,923 | 0.071 |
| `internal/livelog/render` | 21 | 8 | 643 | 0.059 |
| `internal/term` | 20 | 6 | 397 | 0.034 |
| `internal/sandbox` | 8 | 5 | 383 | 0.025 |
| `internal/catalogmaps` | 1 | 1 | 21 | 0.024 |
| `internal/relay` | 9 | 2 | 406 | 0.024 |
| `internal/primitive` | 9 | 1 | 186 | 0.018 |
| `internal/tape` | 4 | 1 | 130 | 0.017 |
| `api/models` | 10 | 2 | 232 | 0.016 |
| `internal/gc` | 16 | 1 | 473 | 0.013 |
| `internal/update` | 9 | 2 | 216 | 0.013 |
| `internal/provider/meta` | 1 | 1 | 31 | 0.011 |
| `internal/toolout` | 8 | 1 | 139 | 0.011 |
| `api/livedoc` | 8 | 2 | 244 | 0.01 |
| `internal/arialog` | 2 | 1 | 73 | 0.01 |
| `internal/hierarchy` | 32 | 3 | 817 | 0.01 |
| `internal/keyconf` | 9 | 1 | 223 | 0.01 |
| `internal/mark` | 2 | 1 | 59 | 0.01 |
| `api/message` | 11 | 1 | 251 | 0.008 |
| `internal/livelog/render/mouse` | 4 | 1 | 145 | 0.008 |
| `internal/provider/openaichat` | 29 | 8 | 1,041 | 0.008 |
| `internal/navstack` | 4 | 1 | 116 | 0.007 |
| `api/decide` | 22 | 1 | 321 | 0.006 |
| `api/quote` | 9 | 2 | 213 | 0.006 |
| `internal/apikey` | 7 | 1 | 105 | 0.006 |
| `internal/logring` | 9 | 2 | 211 | 0.006 |
| `internal/recent` | 6 | 1 | 106 | 0.006 |
| `internal/tui` | 9 | 1 | 190 | 0.006 |
| `internal/uiir` | 1 | 1 | 58 | 0.006 |
| `internal/cli/figtree` | 4 | 1 | 177 | 0.005 |
| `internal/cmdkit` | 35 | 9 | 1,098 | 0.004 |
| `internal/partialjson` | 9 | 1 | 247 | 0.004 |
| `internal/provider/anthropicmodels` | 3 | 1 | 35 | 0.004 |
| `internal/turns` | 11 | 2 | 219 | 0.003 |
