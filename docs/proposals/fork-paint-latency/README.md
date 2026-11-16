# End-to-End Fork and Fork-to-Paint Latency Analysis (`27b36ef37`)

Measured on an isolated btrfs snapshot of Gluck's real 2,032-figaro / 2,479-node / 2.5 GB store (`HEAVY_CLASS=bench`, zero concurrent jobs, zero LLM API calls) driving the stamped `27b36ef37` binary inside a real `120x40` `tmux` (`-f /dev/null`) PTY. Full per-trial data lives in [`data.json`](./data.json); the visual waterfall and interactive breakdown live in [`index.html`](./index.html).

## 1. Benchmark Targets (Real Store Snapshot)

| Cohort | Figaro ID | Handle | IR Messages | User Turns | Assistant Rounds | Context Tokens | Studied Forms |
|---|---|---|---:|---:|---:|---:|---:|
| `medium_189k` | `04890755` | `aerosol-bubblegum` | 194 | 13 | 95 | 189,127 | 0 |
| `large_524k` | `ace9cc9f` | `osmosis-obtain` | 2,318 | 518 | 1,119 | 523,687 | 4 |
| `large_597k` | `b7f1f42c` | `popcorn-rulebook` | 2,866 | 671 | 1,363 | 597,497 | 3 |
| `large_835k` | `e8e5c2fa` | `unmarked-makeover` | 2,528 | 279 | 1,172 | 834,512 | 1 |
| `large_988k` | `1068ad21` | `antelope-nextdoor` | 2,774 | 455 | 1,337 | 987,636 | 1 |

Note on `_meta.turn_count`: `AriaMeta.Fold` (`internal/store/meta_heal.go:80`) increments `TurnCount` on `m.Role == message.RoleOutput` (assistant rounds), whereas `:N` fork coordinates address user turns stamped by `turns.StampIDs`. Interior cuts below were tested at 20%, 50%, and 85% of each figaro's true user-turn span.

## 2. Where Every Millisecond Goes (p50 Summary Matrix)

### A. Interactive Pager `:fork` and `:fork <id>:<turn>` (`120x40` `tmux` PTY, Warm Store)

| Target | Cut | PTY Key-to-Paint p50 (min..max) | Reported `key.paint` | Daemon `fork` RPC | Optimistic `hop.paint` | Pager `hop` (`read` + Glamour) | Glamour Cold / Warm |
|---|---|---:|---:|---:|---:|---:|---:|
| `04890755` (194m, 189k) | `head` | **82.7 ms** (74..92) | 70.4 ms | 59.6 ms | 62.3 ms | 10.1 ms | 0.5 / 0.2 ms |
| `04890755` (194m, 189k) | `:6` | **90.0 ms** (86..93) | 76.1 ms | 67.9 ms | 70.3 ms | 7.7 ms | 0.0 / 0.0 ms |
| `ace9cc9f` (2,318m, 524k, 4 studies) | `head` | **164.7 ms** (164..197) | 152.2 ms | 102.9 ms | 105.4 ms | 48.7 ms | 4.9 / 5.4 ms |
| `ace9cc9f` (2,318m, 524k, 4 studies) | `:259` | **241.7 ms** (231..360) | *151.1 ms* | 180.8 ms | 185.5 ms | 47.4 ms | **129.4** / 24.7 ms |
| `b7f1f42c` (2,866m, 597k, 3 studies) | `head` | **159.5 ms** (156..164) | 144.4 ms | 92.7 ms | 95.2 ms | 51.1 ms | 6.5 / 6.5 ms |
| `b7f1f42c` (2,866m, 597k, 3 studies) | `:336` | **193.7 ms** (182..283) | 181.3 ms | 124.1 ms | 136.2 ms | 56.5 ms | **119.7** / 32.4 ms |
| `e8e5c2fa` (2,528m, 835k, 1 study) | `head` | **160.1 ms** (153..173) | 145.9 ms | 86.1 ms | 88.9 ms | 59.2 ms | 0.2 / 0.2 ms |
| `e8e5c2fa` (2,528m, 835k, 1 study) | `:140` | **168.5 ms** (156..208) | 155.8 ms | 119.5 ms | 123.1 ms | 35.6 ms | **54.5** / 5.3 ms |
| `1068ad21` (2,774m, 988k, 1 study) | `head` | **159.8 ms** (153..171) | 142.6 ms | 90.3 ms | 92.9 ms | 51.7 ms | 12.2 / 11.5 ms |
| `1068ad21` (2,774m, 988k, 1 study) | `:228` | **239.4 ms** (231..323) | *151.1 ms* | 180.0 ms | 190.0 ms | 54.4 ms | **102.0** / 27.3 ms |
| `1068ad21` (2,774m, 988k, 1 study) | `:387` | **259.6 ms** (248..325) | *151.0 ms* | 186.6 ms | 197.4 ms | 58.4 ms | **97.3** / 23.6 ms |

### B. CLI `figaro fork` + `figaro listen <branch>` (`120x40` PTY, Cold vs Warm Store) & `NewAgent` Wake

| Scenario | `04890755` (189k) | `ace9cc9f` (524k) | `b7f1f42c` (597k) | `e8e5c2fa` (835k) | `1068ad21` (988k) |
|---|---:|---:|---:|---:|---:|
| Cold CLI `fork` (head) + PTY `listen` | 149.4 ms (79.5 + 69.9) | n/a | 228.7 ms (118.7 + 109.9) | 226.7 ms (108.5 + 118.2) | 217.6 ms (82.0 + 135.6) |
| Cold CLI `fork` (`:mid`) + PTY `listen` | 143.3 ms (90.9 + 52.3) | n/a | 328.8 ms (162.3 + 166.6) | n/a | 323.6 ms (168.7 + 154.9) |
| Warm CLI `fork` (head) + PTY `listen` | 136.6 ms (76.1 + 60.5) | 212.2 ms (119.6 + 86.5) | 203.2 ms (133.6 + 97.6) | 200.3 ms (99.9 + 101.3) | 192.1 ms (80.1 + 111.1) |
| Warm CLI `fork` (`:mid`) + PTY `listen` | 124.1 ms (84.4 + 41.5) | 326.1 ms (195.8 + 130.3) | 291.5 ms (145.2 + 154.9) | 246.3 ms (136.3 + 111.4) | 307.9 ms (165.1 + 141.3) |
| Live `NewAgent` Wake on Fork (`restoreOne`) | 8.9 ms | 62.1 ms | 57.9 ms | 60.7 ms | 58.5 ms |

## 3. Root-Cause Code Walkthrough (`file:line`)

1. **XWAL Branch Manifest + Synchronous Birth `fsync` (53-83 ms + 14-17 ms)**:
   `XwalStore.forkWithKind` (`internal/store/xwal_store.go:447-476`) takes `s.mu` across `trunks.ForkTail` / `forkAtLocked` (39-83 ms cloning multi-channel directory metadata across a 2,479-node store) and `writeBirth` (`xwal_store.go:484-518`), which appends the birth form patch, genesis IR record, and `aria_id` stamp before calling `x.SyncCoherent()` (14-17 ms `fsync`).
2. **Interior Fork Triple Full-History Scan + `@topology` `fsync` (+80 to +105 ms on `:N` cuts)**:
   - `handlers.forkPointOf` (`internal/angelus/topology_handlers.go:205-229`) calls `log.Read()` and unmarshals all 2,500-2,866 IR messages just to map `:turn` to `atMainLT` (21-33 ms).
   - When `:N` lands inside an ancestor trunk (`owner.Trunk != req.FigaroID`), `handlers.place` (`topology_handlers.go:296, 476`) writes an override edge into `@topology` (`internal/store/topology_form.go:23-38`) and waits for another synchronous `SyncChannelThrough` `fsync` (36.5-50.5 ms).
   - `handlers.seedForkMeta` -> `heldBy(child)` (`topology_handlers.go:391, 447-457`) calls `log.Read()` a second time over `1..atMainLT` to fold token/message counters (8-23.5 ms).
3. **Per-Study Synchronous Libretto `Retain` + Redundant Form Re-open (4-22 ms)**:
   `XwalBackend.ForkWith` (`internal/store/xwal_backend.go:558-568`) calls `inheritStudies(child)` (`xwal_backend.go:656-672`), which opens `child`'s form and loops sequentially over `studiesOf(snap)`, calling `lib.Retain(child)` (`internal/store/libretto.go:116-143`). Each `Retain` awaits an individual `fsync` batch on `@libretto::<id>` (~4-5 ms per studied form: 4.5 ms for 1 study, 15-22 ms for 3-4 studies). Immediately afterward, `b.dropForm(child)` (`xwal_backend.go:564`) closes `child`'s `Form` and `stampCommitted` (`xwal_backend.go:565`) reopens and replays it from disk.
4. **Cold `AriaReader.serverFor(child)` Full-History Projection on Every New Fork (33-69 ms)**:
   Although `child` shares 99.9% of `parent`'s sealed turns, `AriaReader` (`internal/angelus/reader.go:103-146`) keys `r.servers` strictly by `ariaID` with no prefix inheritance from `parent` (and unlocks `r.mu` at line 112 without singleflight, risking concurrent builds when `seedMetrics` and `seedSubject` arrive together). Opening a newly forked 2,866-msg figaro therefore reads all 2,866 IR records (20-31 ms), projects all 279-671 turns (2-9 ms), runs `formdelta.PerRecord` (`internal/formdelta/formdelta.go:46-112`) across every record and studied libretto (7-17 ms; and below a `FormBaseLog` watermark, `Form.PatchesBetween` at `internal/store/form.go:273` falls back to `patchesFromLog` per record, issuing 70+ individual XWAL reads), restores `aria.Server` (15-24 ms), and recomputes metrics: all to return a 30-message tail page. If the fork is then woken (`NewAgent` at `internal/figaro/agent.go:239-250`), it repeats the exact same `Read` + `projTurns` + `attachFormDeltas` + `Commit` loop (+58-62 ms).
5. **Synchronous Glamour Markdown Layout of 600-1,225 Off-Screen Rows + Double Goldmark Parse (55-129 ms cold)**:
   On a fresh CLI (`figaro listen <alt>`) or an uncached interior cut (`:fork <id>:N`), `transcript.settle()` (`internal/cli/transcript_window.go:411-419`) runs `rebuildIndex` (`internal/cli/transcript_index.go:175-245`) up to 4 times while `tuneTail()` widens the window to the 800-row page budget (`pageRowBudget() = 2400/3`). That synchronously renders 22-36 messages (980-1,225 terminal rows) through Glamour before emitting a single byte to the `120x40` PTY (which only displays 37 body rows). Worse, `markdownRenderer.Render` (`internal/render/markdown.go:84-131`) parses the Goldmark AST **twice** for every block containing a hyphen (`-`): once in `holdHyphens` (`markdown.go:99`) and a second time in `Render` (`markdown.go:86`).
6. **Production Bug Discovered by Real-Store PTY Testing (`internal/cli/livelog_bridge.go:840-874`)**:
   When `:fork <id>:<turn>` cuts at an early turn below the parent's retained ~30-message tail window (`:56`, `:91`, `:104`, `:134`), `t.client.CloneBelow(base)` returns 0 held messages (`kept == 0`). `livelogTurn.retarget` sets `floor := base` and calls `t.client.FirstMissing()`, which returns `(_, false)` on an empty client, leaving `floor = base` and returning `seedPlan{kind: seedTail, from: base}`. `ReadBeforeFloor(tail, floor=base)` then excludes turns `1..base-1` (the child's entire history!), returns 0 parts, and renders an empty new-figaro landing page (`═══`) despite the fork holding 200k+ tokens.

## 4. Instrumenting Fork and Fork-to-Paint in `figscope`

### Why Current Telemetry Misses It
1. **`figaro.pager.key_to_paint` Clamps at 151 ms (`internal/cli/keypaint.go:86`, `command_router.go:95, 539`)**:
   `opensPit("fork")` returns `false` because `"fork"` is in `overlayVerbs` (`command_router.go:73`). `t.awaiting` stays `false`. When `:fork` exceeds `progressDelay = 150ms`, the progress/status repaint on the **parent** figaro triggers `paint()` -> `t.answerKeys()` at `151.0-151.2 ms`, draining `t.keys` 100-210 ms before the forked figaro actually arrives on screen.
2. **No Daemon Span on `angelus.fork` (`topology_handlers.go:235`)**:
   Unlike `angelus.create` (`topology_handlers.go:33`), `fork` emits no OTel span, and `figscope`'s `keptSpans` (`~/dev/figscope/model.go:54`) drops all spans outside `{"figaro.qua", "figaro.round", "figaro.methods", "cli.submit", "decide"}`.

### Concrete OTLP & `figscope` Schema
- **Client Span `cli.fork` (`internal/cli/fork_trace.go`, modeled on `submit_trace.go`)**:
  - Started at keystroke (`in.readAt` passed via `withSubmittedAt`) or CLI `runForkCmd` entry; propagated to `angelus.fork` and `figaro.read` via `traceparent` on RPC params (same pattern as `QuaRequest.TraceParent`).
  - Attributes: `figaro.parent_id`, `figaro.id` (branch), `fork.surface` (`"pager"` | `"cli"`), `fork.interior` (`bool`), `fork.at_turn` (`int64`), `cli.OptimisticPaint` (`bool`), `cli.window_rows` (`int64`), `cli.glamour_misses` (`int64`), `cli.painted` (`bool`).
  - Monotonic Span Events (all stamped from client `time.Now()` to eliminate cross-process clock skew if federated): `fork.rpc_sent`, `fork.rpc_acked`, `hop.optimistic_paint`, `hop.read_done` (`wire_bytes`, `parts`), `ui.markdown_done` (`render_ms`, `misses`), `fork.first_paint` (`pty_bytes`).
- **Daemon Child Span `angelus.fork` (`internal/angelus/topology_handlers.go:235`)**:
  - Attributes: `figaro.parent_id`, `figaro.id`, `fork.interior`, `fork.at_lt`, `fork.parent_resident` (`bool`), `fork.studies` (`int`).
  - Events: `fork.turn_resolved`, `fork.xwal_branched`, `fork.birth_synced`, `fork.studies_retained`, `fork.placed`, `fork.meta_seeded`.
- **Histograms (`internal/otel/otel.go`)**:
  - `figaro.fork.duration` (daemon, `subMS` buckets, attrs: `interior`, `parent_resident`)
  - `figaro.fork.to_paint` (CLI, `subMS` buckets, attrs: `surface`, `interior`, `cache_state`)
  - Fix `keypaint.go`: arm an `awaitSubjectGen` fence on async subject-switching `overlayVerbs` (`fork`, `attend`, `listen`, `new`) so intermediate progress frames on the outgoing figaro do not drain `t.keys`!
- **`figscope` Changes (`~/dev/figscope/`)**:
  - `model.go:54`: add `"cli.fork": true, "angelus.fork": true, "figaro.read": true` to `keptSpans`.
  - `otlp.go:150, 349`: add `"figaro.fork."` to `keptMetricPrefixes` and attach `service.version` in `pointAttrs` for `figaro.fork.*` alongside `figaro.pager.*`.
  - `derive.go`: add `assembleForks(spans []Span) []ForkTrace` joining `cli.fork` and `angelus.fork` by `ParentID`/`TraceID`, computing sub-layer deltas strictly within each process's own monotonic clock (`ev.TimeN - sp.StartN`) rather than subtracting daemon timestamps from CLI timestamps.

## 5. Prioritized Engineering Fixes

| Rank | Fix | Exact Target (`file:line`) | Expected Latency Win | Complexity / Risk |
|---:|---|---|---|---|
| **P0** | **Fix `kept == 0` empty-landing bug on interior `:fork <id>:<turn>`**: when `t.client.CloneBelow(base)` holds 0 messages (`kept == 0`), return `seedWindow` instead of `seedTail{from: base}`. | `internal/cli/livelog_bridge.go:863-873` | Fixes 100% blank-screen bug on deep `:N` pager forks | Trivial (3 lines), zero risk |
| **P1** | **Viewport-first Glamour rendering + single Goldmark parse**: seed initial paint with `transcriptMinPageSize` (6 msgs / ~45 rows covering the 40-row screen), paint PTY frame immediately, and defer `tuneTail()`'s 800-row expansion + eliminate duplicate AST parse in `holdHyphens`. | `internal/cli/transcript_window.go:411-419`, `internal/render/markdown.go:84-105` | **-65 to -95 ms** on cold `listen` and interior `:N` forks | Low-Medium, isolated to UI/render |
| **P2** | **Clone/share parent `AriaReader` prefix + singleflight per figaro**: seed `r.servers[child]` from `r.servers[parent]` truncated at `atMainLT` (or `NewAgent`'s `ariaSrv`), and guard `serverFor` with a per-figaro mutex so `seedMetrics` + `seedSubject` never double-project. | `internal/angelus/reader.go:103-146`, `internal/figaro/agent.go:246` | **-40 to -55 ms** on `hop.read` / `listen` + **-50 ms** on `NewAgent` wake | Medium, clean append-only prefix invariant |
| **P3** | **Eliminate triple IR scan & parallelize study `Retain` + keep `Form` open in `ForkWith`**: index turn->LT bounds or reuse one `OpenFigIR` slice across `forkPointOf` + `heldBy`; retain `child` `Form` in `ForkWith` instead of `dropForm`+reopen; fan out `lib.Retain` concurrently. | `internal/angelus/topology_handlers.go:205,447`, `internal/store/xwal_backend.go:561-672` | **-35 to -55 ms** on interior `:N` forks & multi-study forks | Medium, touches store birth path |
| **P4** | **Add `FormBaseLog` checkpoints to `stumpFormLog` (`@topology`, `@libretto::*`) & retain resident patches**: avoid O(history) `RangePatches` walks (up to 176.5 ms on 1,985-record librettos) and per-record `patchesFromLog` disk fallbacks below segment watermarks. | `internal/store/topology_form.go:49`, `internal/store/form.go:273` | **-10 to -20 ms** on fork/deltas; **-500+ ms** on daemon boot | Medium, durable format compatible via segment headers |
