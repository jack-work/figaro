# Storm triage: the runtime assault and its findings

Self-contained per Gluck's constraint (2026-08-14, relayed via aria
bbfe7a37): plans/progress.md is CLOSED at 4012 lines; everything about
this investigation lives here. PRESCRIPTION ONLY: internal/ is
read-only for this work — harnesses, scripts, profiles and captures are
free; the deliverable is measured evidence, named culprits, and exact
fixes for someone else's hands.

Aria 94f0752b · role @980dc16c · branch feat/form-deltas-ui @ b5f736a1.

## The question

Gluck's live session reached >1 GB with "several bugs" after the turn
delta + turn cache + reader convergence work. Convict or acquit:

- S1: the `Seal(nil)` / `legacyWhole` latch — a tail minted by
  OpenTurn/OpenInquiry has no LT bracket; one unbracketed turn latches
  the aria's cache: eviction disabled, budget bypassed, AND account()
  early-returns so doctor mem UNDER-REPORTS exactly when retention is
  unbounded. Checkable by reading; profile confirms.
- S2: reader registry first-sight full materialize + tail-bump
  re-materializes under chatty forms.
- S3: aliasing chain — resident composed turns pin decoded-IR strings
  past the IR window's trim.
- S4: the idle creep (~1.1 MB/5s on an idle fresh daemon, pre-existing
  finding, unattributed).

## The plan

1. PPROF ARMED AT BIRTH: FIGARO_PPROF=1 in fish rc (done); a figla
   reminder restarts the daemon armed while this aria is dormant and
   takes /var/tmp/heap-boot.pb.gz (armed, fires 03:52).
2. THE STORM: ~100 arias, cheap/low-end models across providers, driven
   through tmux ptys as REAL USER ACTION — typing (one char per read),
   mid-turn ^C/hup, pager scrolls, forks, attends, casts, studies,
   resizes, reconnects. Frame-by-frame pty capture (capture-pane -e -S -
   per tick into /var/tmp/storm/frames/) so any paint regression is
   provable after the fact.
3. PROFILE WHILE IT BURNS: heap at boot/mid/peak with -base diffs, 30s
   CPU, goroutine dumps, GC stats (GODEBUG=gctrace opt.), fd/endpoint
   counts, doctor mem series. delve/strace/SIGQUIT if sampling stops
   short of a name.
4. FINDINGS + PRESCRIPTIONS accrue below, one section per culprit:
   evidence first, exact fix second.

## The harness (built 2026-08-14 by the storm worker, aria 7f312ebe)

Everything lives under `/var/tmp/storm/`, nothing under a real state dir.

| file | what it is |
|---|---|
| `bin/figaro` | the branch build, STAMPED (`b5f736a19d61`). The installed nix binary is `ec3ab03a` — **16 commits behind, i.e. it predates the turn cache entirely**. Storming the daemon Gluck happens to be running would have measured a build without the suspects in it. |
| `lib.sh` | isolation (`FIGARO_STATE_DIR`/`RUNTIME_DIR`/`CONFIG_DIR` under the storm root, `FIGARO_PPROF=1`), heap/cpu/goroutine/allocs pullers, `smaps_rollup` footprint (PSS/anon, not RSS). |
| `fleet.sh` | breadth: 100 backed arias × 3 turns, 12 at a time, cheap models round-robined across two providers (copilot `gpt-5-mini`, `gpt-5.4-mini`, `mai-code-1.1-flash`, `claude-haiku-4.5`; anthropic `claude-haiku-4-5`). |
| `deep.sh` | depth: 5 arias × 10 FAT turns each (a 3000-number `seq` per turn), because Gluck's >1 GB was one aria running for hours, not a hundred running once. |
| `tmuxcrew.sh` | 6 real ptys driven as a user: **one character per read**, `-l` pager open, mid-turn `^C`, `H` hang-up, `j/k/G/gg` motions, `^O` verbose toggle, `!` status panel, two `resize-window`s mid-stream, `show`/`status`/`state set` on other arias' ids, `fork`, detach/reattach. Frame-by-frame `capture-pane -e -p -S -` into `frames/` (460 frames kept). |
| `sampler.sh` | every 20s: `doctor mem -j` → `log/mem.jsonl`, PSS/anon → `log/footprint.txt`, a heap profile every third tick. |
| `pager.py` + `reader2.sh` | speak `aria.page` straight down the daemon socket (the method the transcript pager and `listen` use) so the READER path can be A/B'd without paying a CLI spawn per read. |
| `clientmem.sh` | pins a real `figaro listen` pager in a pty on one aria, pumps fat turns into it from another shell, and samples the CLIENT's RSS beside the daemon's — the "which process is the gigabyte" instrument. |
| `prof/` | `boot`, `t*` series, `peak1` (+cpu, +goroutine), `agentpeak` (+allocs), `idleA/idleB`, `s2-boot/pass1/pass2`, `r2-*`. |
| `scratch/stormprobe/` (in-repo, harness only) | drives `aria.Server` through the AGENT shape and the READER shape in-process, no provider, no daemon — the deterministic half of the S1 proof, plus the O(R²) sizing measurement. |
| `scratch/s3probe/` (in-repo, harness only) | composes 200 tool round-trips, drops the decoded IR, and measures what the nodes still hold — the aliasing question, answered with a number. |

Storm as run: 103 arias born, ~200 real turns, 6 ptys, peak 600 goroutines,
RSS 52 MB → 140 MB. No daemon of Gluck's was touched; census clean.

## Findings

**Verdict table** (detail in the sections below; every number measured on the
branch build `b5f736a19d61`, isolated daemon, `FIGARO_PPROF=1`).

| # | suspect | verdict | the number |
|---|---|---|---|
| S1 | `Seal(nil)` / `legacyWhole` latch | **CONVICTED** | agent retention linear & unbounded (12.7 → 25.4 → 50.6 MiB at 200/400/800 turns) while the accountant reports **0 B, 0 evictions** — or worse, FREEZES at whatever it counted when the agent hydrated (262.4 KiB across four more live turns, aria growing to ctx 170.8k) |
| S2 | reader first-sight materialize + tail-bump | **PARTLY** | first sight 53.2 MB / 100 arias (~530 KB each on 4–7-message arias); warm 8.5 MB; **`servers` map never pruned** — 1.19 MiB of composed UI IR survived the sweep that took `resident_arias` 100 → 0. Chatty-form limb **acquitted** |
| S3 | aliasing chain pins decoded IR | **ACQUITTED** | dropping 67.4 MiB of decoded IR left 2.74 MiB behind for 2.38 MiB of node text (1.15×). Nodes copy, they do not alias |
| S4 | the idle creep (~1.1 MB/5s) | **CONVICTED, and it is not figaro's code** | otel `sdk/log` BatchProcessor clones a 512-record buffer **every second even when the queue is empty**: 61.90 MB in 316 idle seconds = **196 KB/s, 11.8 MB/min, 16.9 GB/day**, 67% of all allocation on an idle daemon |
| S5 | (new) sizing churn: `nodeSize` marshals | **CONVICTED** | a turn that streams in R rounds allocates O(R²): 0.94 / 3.06 / 10.90 / **41.02 MiB** for 10 / 20 / 40 / 80 rounds of 4 KiB nodes |

(accruing — nothing asserted before its profile)

### S1 — CONVICTED. The `legacyWhole` latch disables the turn cache for every live agent.

**Evidence 1 — deterministic, in-process** (`go run ./scratch/stormprobe -turns N -kb 32`,
two nodes per turn, 16 MiB budget = what `config.toml` ships):

```
turns   AGENT shape (OpenTurn→Update→Close→Seal(nil))   READER shape (Restore with LT brackets)
200     budget resident   0.00 MiB  evictions   0  |  heap retained 12.69 MiB
        budget resident  12.52 MiB  evictions   0  |  heap retained 12.67 MiB
400     budget resident   0.00 MiB  evictions   0  |  heap retained 25.35 MiB
        budget resident  15.96 MiB  evictions 145  |  heap retained 16.17 MiB
800     budget resident   0.00 MiB  evictions   0  |  heap retained 50.64 MiB
        budget resident  15.96 MiB  evictions 545  |  heap retained 16.24 MiB
```

The agent shape's retention is **linear and unbounded** (12.7 → 25.4 → 50.6 MiB)
while the accountant reports **0.00 MiB and zero evictions at every size**. The
reader shape, given the identical content, clamps at the 16 MiB budget and
evicts. The only difference between the two shapes is the LT bracket.

**Evidence 2 — the live daemon under the storm.** `doctor mem -j` at 108 live
arias, ~200 turns composed, 5 of them deep (41 messages, 88k ctx):

```json
{"live_arias":108,"resident_arias":108,"ui_window_budget":16777216,
 "heap_alloc_bytes":63154688,"goroutines":600}
```

`ui_window_bytes` and `ui_window_evictions` are **absent from the JSON**
(`omitempty` on zero) for the whole run — 27 samples, from the first turn to
the last. The turn cache accounted nothing, all storm long. `doctor mem`'s
human output says `ui window resident=0 B of 16.0 MiB evictions=0` beside a
daemon holding two hundred composed turns.

**The mechanism, exactly:**

1. `aria/server.go` `OpenTurn` / `OpenInquiry` append `Turn{ID: id}` — no LTs;
   nothing on the agent path ever supplies them (`compose/turns.go:75` sets
   `LTs: []uint64{first, last}`, but that is the READER's projection).
2. `figaro/agent.go:1217` `finishTurn` calls `a.ariaSrv.Seal(nil)`; `Seal`
   only assigns `tl.LTs` when `len(lts) > 0`.
3. So `turncache.go noteLegacy` sees `len(t.LTs) < 2` on the aria's **first**
   turn and sets `legacyWhole` — a field of the whole cache, never cleared.
4. `account()` early-returns under the latch: the turn is not put in the LRU,
   `b.total` never rises, so nothing is ever chosen as a victim. Eviction is
   not merely declined for the unbracketed turn — it is switched off for every
   turn that aria will ever run.
5. And `doctor mem` UNDER-REPORTS exactly where retention is unbounded, which
   is why the memory picture looked innocent while the process grew.

Nothing frees this before `Registry.Hibernate` (default `dormant_after_minutes
= 15`) tears the agent down and `Agent.Kill` → `ReleaseCache` returns the
refs. A session being actively typed at NEVER goes idle for 15 minutes — which
is precisely Gluck's >1 GB session.

**PRESCRIPTION (S1).** Three parts; (a) alone is not enough.

(a) **Make the latch per-TURN.** Replace the cache-wide `legacyWhole bool` with
    a per-turn `pinned bool` in `turnMeta`, set by `noteLegacy` when that turn
    lacks a bracket. `hollow(i)` refuses a pinned turn; `account(i)` still
    counts it (see (c)); eviction skips it exactly as it skips the tail. One
    unbracketed turn then costs its own bytes, not the aria's whole history.

(b) **Give the tail its bracket at seal.** `finishTurn` has the LTs at hand —
    the turn's first and last logical times are what `compose/turns.go`
    computes from the same records — so `Seal(nil)` should become
    `Seal([]uint64{first, last})`. `Server` already has `s.baseTurn`/`s.base`
    and the agent knows its figLog span; the cheapest correct source is the
    agent's own turn bracket (the same one `turnSource` is asked for on a
    miss). With (b) in place (a) is a safety net for legacy logs rather than
    the common path — and the turn cache actually starts working for live
    arias, which is the feature that was believed shipped.

(c) **`account()` must never silently skip.** A pinned turn should be counted
    in `b.total` even when it cannot be evicted, so `doctor mem` reads
    `resident=41 MiB of 16 MiB (pinned: 41 MiB)` instead of `0 B`. An
    accountant that stops counting when it stops being able to act is how a
    1 GB process reported a 4 MiB window. Suggested surface: add
    `UIWindowPinnedBytes` to the mem report and print it whenever non-zero.

**Canary.** `go run ./scratch/stormprobe -turns 800 -kb 32` must print a
bounded AGENT resident/retained pair after the fix, and does not today.

**The control that makes it airtight.** On the same daemon, with every agent
gone, one `figaro listen` on a dormant aria (the READER path, whose turns come
from `compose.Turns` WITH brackets) moved the accountant immediately:

```
ui window  resident=146.3 KiB of 16.0 MiB  evictions=0     # one dormant aria paged
ui window  resident=1.1 MiB  of 16.0 MiB  evictions=0      # 100 dormant arias paged
```

Same budget, same process, same composed content — accounted when read, unaccounted
when lived. The latch is agent-specific, and the reader is the innocent party.

**The refinement that answers the anomaly logged above ("boot-committed
bracketed turns should account; they did not").** They do — and then the meter
FREEZES. A live agent's cache is empty until something reads it: `Agent.Read`
→ `hydrate()` → `AdoptIfEmpty(a.materializeTurns())` → `ReplaceAll`, whose
turns come from `compose.Turns` and therefore DO carry brackets, and which
resets `legacyWhole = false`. So:

```
fresh daemon, nothing resident                    ui window       0 B
wake deep-4 (11 turns of history) with one turn   ui window  262.4 KiB   <- hydrate accounted the history
page the live aria                                ui window  262.4 KiB
+1 live turn                                      ui window  262.4 KiB
+1 more live turn (fat: 13 KB tool result)        ui window  262.4 KiB
aria now 83 messages, ctx 170.8k/200.0k           ui window  262.4 KiB
```

The number never moves again, because the FIRST live turn appended through
`OpenTurn` re-latches `legacyWhole` and `account()` has returned early ever
since. Everything already in the LRU stays there; everything the aria does
from that moment on is invisible and unevictable.

That is worse than reporting zero. Zero looks like a bug; **262.4 KiB looks
like an answer** — a small, plausible, stable window figure sitting beside an
aria that has quietly grown past its context limit. If anyone read `doctor mem`
during the >1 GB session and saw a calm `ui window`, this is why.

(It also explains the earlier "resident=1 agent, ui window still 0 B" note: an
agent nobody has PAGED has composed nothing, so zero was honest there. The lie
needs a read to start it and a live turn to freeze it.)

### S2 — PARTLY CONVICTED. First sight is O(whole log); the "chatty form" limb is ACQUITTED.

Measured on the storm's own store (100 arias), reader-only daemon (`live=0`),
paging through `aria.page` — the method the transcript pager and `listen` use.
(`figaro show` renders from `figaro.context` and never reaches the reader's
windowed servers; an earlier arm that used `show` measured the wrong path and
is discarded.)

| arm | what | wall | allocated |
|---|---|---|---|
| pass 1 | first-sight page of 100 arias (fresh daemon) | 1.74s | **53.2 MB** (~530 KB/aria, on arias of 4–7 messages) |
| pass 2 | the same 100 pages, warm | 1.67s | **8.5 MB** (~85 KB/aria) |

Pass 1's bill: `bufio.NewReaderSize` 23.4 MB (44%) — one buffered reader per
segment opened — `figwal segment.JSONLCodec.ReadFrame` 6.2 MB, `form.makeNode`
4.0 MB. Pass 2 is 6× cheaper but **not free**: the staleness probe re-opens
the log (`bufio.NewReaderSize` 3.8 MB, `ReadFrame` 1.0 MB, `os.Stat`) and
`aria.nodeSize` costs another 1.5 MB — `nodeSize` is `json.Marshal(node)`, so
sizing a page marshals every node in it, to a buffer that is then discarded.

**The chatty-form limb is acquitted, by measurement.** 40 pages of one aria
with a `figaro set --id <aria>` between each allocated **no** compose,
projection or decode at all (the delta is `json.Marshal` from the form write
path). A form patch lands on the FORM channel; `serverFor`'s probe reads the
MAIN log's tail, which does not move, so the short-circuit holds. The
suspicion was reasonable and is wrong.

**What is left, and it is real:**

1. **First sight costs the whole log, per aria, per daemon lifetime** — and the
   reader's own doc measures that at 13.1 MB/op for a 10k-message aria. A
   hundred arias browsed once is a hundred whole-log decodes.
2. **`AriaReader.servers` is never pruned.** Nothing deletes from the map: one
   `readAria` (an `aria.Server` + its `*Metrics`) accumulates per aria ever
   paged, and survives `EvictIdle` dropping the backend's cached log. Bounded
   by the number of arias a daemon is asked about, which for a long-lived
   daemon is "all of them".
3. **`TailBracket() == 0` disables the short-circuit.** An aria whose newest
   turn has no bracket re-materializes the WHOLE log on EVERY page. Reader
   turns are bracketed by `compose.Turns`, so today this fires only for an
   aria with no turns — but it is the same missing-bracket assumption that S1
   is made of, and it is one `Restore(agentTurns)` away from being the common
   case.

**PRESCRIPTION (S2).**

(a) Bound the servers map: an LRU keyed by aria id (or a sweep hook on the
    existing 30–120s eviction sweep that drops `readAria`s whose backend log
    was just evicted). `EvictIdle` already computes exactly that set — hand it
    to the reader instead of letting the two caches disagree about who is
    dormant.
(b) Make the staleness probe cheap: cache the last-seen tail LT per aria beside
    the server and re-probe only when the backend signals an append (the
    backend already knows — it is the one writing). Today every warm page
    re-opens a segment reader to ask a question the writer could have answered.
(c) Range-project on first sight instead of projecting the whole log: the
    reader's doc already names this ("a range-projecting reader that composes
    only the window would remove the asymmetry; it is the obvious next step
    and deliberately not this one"). The window it needs is exactly the
    `ChunkFor` bracket the turn cache computes.
(d) `nodeSize` should not `json.Marshal` (see S5).

### S3 — ACQUITTED as an aliasing amplifier. A different, smaller thing is true.

`go run ./scratch/s3probe -msgs 200 -lines 5000` builds 200 tool round-trips
whose results are 5000 lines each (compose caps a tool node at 200 lines),
composes them, drops every decoded record, and measures what the nodes hold:

```
decoded fig IR          67.41 MiB
composed nodes carry     2.38 MiB of text
after dropping the IR    2.74 MiB retained by the nodes alone   → 1.15x
```

If composed nodes aliased the decoded strings, dropping 67 MiB would have
freed nothing. It freed 96% of it. `compose.tailBound` rebuilds its string
(`strings.Split` → `strings.Join`), and `textNode`'s `strings.TrimRight` only
ever trims a trailing-newline suffix, so **the IR window's trim does free
memory even while composed turns are resident**. S3 as charged is not the
culprit.

Two true things found while looking:

1. **A node costs ~470 bytes before it holds any text.** The same probe with
   one-line results retains 0.18 MiB for 0.01 MiB of text (16×) across 400
   nodes. Node-heavy turns (many small prose/thinking blocks) are dominated by
   struct + slice + map overhead, not by content.
2. **`nodeSize` charges the JSON length, so it under-charges exactly those
   turns.** The budget believes a 400-node turn of tiny nodes costs its
   marshaled bytes; it actually costs those bytes plus ~190 KB of node
   structure. Combined with S1 (which charges nothing at all) the accountant
   has been wrong in both directions.

**PRESCRIPTION (S3).** No fix for the aliasing charge — there is nothing to
fix. Add a fixed per-node constant to `turnBytes`
(`n += nodeStructOverhead` with `nodeStructOverhead ≈ 400`, spelled with the
measurement in the comment) so a node-heavy turn is charged for what it costs,
and keep the "do not reflect-walk" rule the comment already states.

### S4 — CONVICTED, and the culprit is in a dependency, not in figaro.

The daemon, with **zero arias resident, zero clients, 16 goroutines and
nothing written to `logs.jsonl` for four minutes**, allocated 92.81 MB in 316
seconds. Two thirds of it has one name:

```
61.90MB 66.70%  slices.Clone[[]go.opentelemetry.io/otel/sdk/log.Record]
 7.05MB  7.60%  compress/flate.NewWriter      <- this profiler's own heap pulls
 4.88MB  5.25%  compress/flate.(*compressor).init          "
 2.50MB  2.69%  runtime/pprof.allFrames                    "
```

**196 KB/s · 11.8 MB/min · 16.9 GB/day**, at idle. It matches the
pre-existing, unattributed "~1.1 MB/5s on an idle fresh daemon" almost exactly
(1.1 MB/5s = 13.2 MB/min), so that finding and this one are the same animal.

**The mechanism**, in `go.opentelemetry.io/otel/sdk/log@v0.19.0`:

```go
// batch.go poll(): interval defaults to 1s, buf is dfltExpMaxBatchSize = 512 Records
qLen = b.q.TryDequeue(buf, func(r []Record) bool {
        ok := b.exporter.EnqueueExport(r)
        if ok {
                buf = slices.Clone(buf)   // <- clones all 512, not the n dequeued
        }
        return ok
})

// exporter.go EnqueueExport():
if len(records) == 0 {
        // Nothing to enqueue, do not waste input space.
        return true                        // <- true, so the clone runs anyway
}
```

An empty queue returns `true`, so every tick clones the whole 512-record
buffer. 512 × ~380 B ≈ 195 KB — the measured rate to within a rounding error.
(The SDK even carries a `// TODO: investigate using a sync.Pool instead of
cloning.` two lines above.) `figaro` reaches this through
`internal/otel/otel.go:204`, `sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp))`,
with every default in place.

This is **CHURN, not retention**: it does not explain the 1 GB, and no amount
of it grows RSS without bound. What it does is force a GC roughly every 20
seconds on a daemon doing nothing (`num_gc` climbed 19 → 24 over two idle
minutes in `log/mem.jsonl`), keep the heap floor oscillating 8–20 MB, and make
every allocation profile of this process two-thirds noise — which is exactly
how it stayed unattributed for so long.

**PRESCRIPTION (S4).** In `internal/otel/otel.go`, configure the processor
instead of taking the defaults:

```go
sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp,
        sdklog.WithExportInterval(30*time.Second),   // 1s -> 30s: 30x less clone
        sdklog.WithExportMaxBatchSize(128),          // 512 -> 128: 4x smaller clone
))
```

That is 120× off the idle bill (196 KB/s → ~1.6 KB/s) and costs at most 30s of
latency to a *file* that nothing tails in real time. Two follow-ups worth
doing: (1) report it upstream — the fix there is one clause,
`if ok && n > 0 { buf = slices.Clone(buf) }`, and the SDK's own TODO agrees;
(2) `doctor mem` could carry an `alloc-rate` line (two `ReadMemStats` a second
apart) so the next idle creep is visible without a profiler.

### S5 — NEW SUSPECT, CONVICTED. Sizing a turn marshals it, once per round.

`turnBytes` → `nodeSize` is `json.Marshal(node)` (`page.go:88`). It runs on
`Append`, on `recompose`, and on **`TailMutated`, which the Server calls on
every `Close` and every `OpenInquiry`** — i.e. once per streaming round, over
the whole turn accumulated so far. `Update` also copies the entire node list
per frame (`s.open.nodes = append([]livedoc.Node(nil), nodes...)`), and the
agent recomposes the turn's nodes from its messages on every frame, where
`compose.tailBound` `strings.Split`s the whole tool output into lines again.
Three O(turn) costs per round; the total over a turn is quadratic in its
length.

Measured (`go run ./scratch/stormprobe`, the `SIZING` lines — garbage, not
retention, so this is `TotalAlloc`):

```
rounds   4 KiB nodes   garbage
    10                  0.94 MiB
    20                  3.06 MiB   (3.3x for 2x the rounds)
    40                 10.90 MiB   (3.6x)
    80                 41.02 MiB   (3.8x)
```

An 80-round agentic turn — a long tool-heavy turn, which is what figaro is FOR
— burns 41 MB of garbage in the UI-IR layer alone. It also shows up in the
reader: `nodeSize` was 18% of the allocation of a warm page (1.5 MB of 8.5 MB
for 100 pages).

**PRESCRIPTION (S5).**

(a) **Stop marshaling to measure.** `nodeSize` should sum the fields it
    already knows (`len(Markdown) + len(Output) + len(Input) + len(Summary) +
    len(Name) + len(ID) + …` plus the per-node constant from S3) — the same
    estimate `turnBytes` already claims to be making at the turn level. The
    comment there ("an estimate at insert, like every other window in the
    process; do not sum reflect-walked struct sizes") is right; the
    implementation quietly does the most expensive possible thing instead.
(b) **Make `TailMutated` incremental.** It re-sizes the WHOLE tail on every
    round. It is called right after a mutation whose extent the caller knows
    (`Close` folds in `s.open.nodes` from `s.open.from`): size only the new
    suffix and add the delta.
(c) `Update`'s full node-list copy per frame is the same shape of problem and
    the same fix (copy the suffix, not the world) — but it is the one with a
    correctness constraint (the copy is what makes the previous frame a stable
    diff base), so measure before touching it.

### Frame QA: what the 1039 pty frames say about paint

The storm was photographed so a paint regression could not hide behind a
memory investigation. Detector (`-e -p -S -` captures, ANSI stripped, live
region = last 46 rows, duplicate SEQUENCES of 3 rows rather than adjacency,
per trap 8):

```
frames 1086   tui-frames 1039   no-chrome 47 (shell-only panes)
over-wide rows            0
cli errors                0
spinner glyph in a frame tagged settled   8
duplicate 3-row sequences in the live region  213
```

- **Over-wide rows: none**, across two mid-stream `resize-window`s
  (120→90→130 columns) with six panes streaming.
- The 213 duplications are all one situation, and it is one the harness
  created: the crew typed `show`/`status` **into a pane whose figaro was still
  attached and streaming**, so foreign output landed inside the live region
  and the painter re-emitted its tail below it. A scrollback-preserving
  painter cannot rewrite rows that have scrolled, so this is expected rather
  than a regression — but it is worth knowing that a user who runs a command
  in the same shell as a live pager sees the tail twice.
- The 8 spinner-in-settled frames each still had a tool running when the
  "settled" snapshot was taken; none shows a *frozen* spinner in a finished
  frame (the next frame in each filmstrip advances or clears it).
- One frame worth a second look independent of this investigation:
  `frames/storm_0/1786724362295-r2-stream.txt` shows `disconnected ⠸` with a
  live spinner while a queued-messages panel is up. Not a memory finding;
  filed here because the capture exists.

First crew run (kept at `frames-run1/`) is INVALID as UI evidence: its rounds
2–4 died on `-O system.provider=…` (`--outfit takes outfit names`), so those
panes only echoed a CLI error. Fixed by minting a storm-only outfit
(`root/config/outfits/stormcheap.toml`, house + copilot + `gpt-5-mini`); the
numbers above are the corrected run.

## Does this add up to a gigabyte? Not yet — and that matters.

Honesty first: **S1 is convicted of unbounded retention and of lying about it,
but the arithmetic does not reach 1 GB on its own.** Measured on the storm's
own store, a fat turn (one `seq 1 3000` tool result, ~13 KB, plus prose) costs
about **20 KiB of composed UI IR**: paging the whole of `deep-4` (41 messages,
10 fat turns, 88k ctx) moved the accountant by 0.2 MiB. `compose.tailBound`
caps a tool node at 200 source lines, so a tool node is ~20 KB even when the
command printed a megabyte.

At 20 KiB/turn, one aria reaches 1 GB after ~50,000 turns. Gluck's session did
not run 50,000 turns. So the retention S1 licenses is real, is a bug, and is
NOT by itself the gigabyte. Something else is carrying most of that weight,
and the honest thing to do is say so and name where to look next.

**The most promising unexamined lead: the CLIENT, not the daemon.** The TUI
holds its own `aria.Store`, and `transcript.enter()` calls
`client.SetClosedLimit(0)` — the count-based trim is SUSPENDED for as long as
the pager is up, deliberately, with retention handed to `evictStale()`. But
`evictStale` is called from exactly one place (`resetToTail`), it refuses to
evict inside the window (`if t.from.Less(floor) { floor = t.from }`), and a
reader who has scrolled up therefore keeps everything from their position to
the tail. A pager left open on a long session is precisely Gluck's shape.

First measurement, turns pumped into an aria with a real `figaro listen` pager
attached in a pty (`clientmem.sh`, `log/clientmem.txt`; two runs interleave in
that file — a thin aria whose model kept refusing the tool call, and `deep-4`
on haiku, which did not):

```
THIN aria (10 turns)                    FAT aria, deep-4 (8 tool turns)
t0     client 44.8 MiB  daemon 66.6     t0     client 47.1 MiB  daemon 65.4
turn1  client 46.9 MiB  daemon 66.7     turn1  client 49.5 MiB  daemon 68.9
turn4  client 48.9 MiB  daemon 66.0     turn4  client 49.6 MiB  daemon 74.0
turn8  client 48.9 MiB  daemon 72.4     turn8  client 51.7 MiB  daemon 79.8
```

Two rates fall out, and the second is the interesting one:

- **client ≈ 0.6 MiB per fat turn** while followed, and it does not come back.
  A thousand-turn day is ~600 MB in the terminal you are typing in. Not proof
  of unboundedness — 8 turns is 8 turns — but it is the right order of
  magnitude for the symptom, and it is the cheapest thing left to measure.
- **daemon ≈ 0.9 MiB per fat turn** across the same window (65.4 → 79.8 MiB
  for 16 fat turns over two arias). That is **45× more than the 20 KiB of
  composed UI IR** the turn cache retains for the same turn, so most of it is
  the other caches filling — the IR window (4 MiB), the translation window
  (4 MiB), the figwal segment cache (32 MiB), provider request buffers — which
  PLATEAU, and extrapolating them to a gigabyte would be dishonest. What does
  not plateau is the composed UI IR, because of S1.

So the gigabyte most likely lives in a *client*, or in a daemon whose bounded
caches were all full AND whose live arias had each retained thousands of
composed turns. Both are reachable from here; neither is proven by this storm.

**And the recipe for catching the real thing, now that pprof is armed at
birth.** The next time a figaro passes ~500 MB, before restarting anything:

```sh
# WHICH process? the daemon or the terminal you are typing in
ps -o pid,rss,args -C figaro
# the daemon's own view, and the heap, diffed against the boot baseline
figaro doctor mem -j > /tmp/mem-fat.json
curl -s --unix-socket $XDG_RUNTIME_DIR/figaro/pprof.sock \
     http://x/debug/pprof/heap -o /tmp/heap-fat.pb.gz
go tool pprof -top -inuse_space -base /var/tmp/heap-boot3.pb.gz /tmp/heap-fat.pb.gz
go tool pprof -top -sample_index=inuse_objects -base /var/tmp/heap-boot3.pb.gz /tmp/heap-fat.pb.gz
# if it is the CLIENT, it has no pprof socket: SIGQUIT it into a stack dump
kill -QUIT <client-pid>   # after copying anything you care about out of the pane
```

One heap profile taken while the process is actually fat settles in thirty
seconds what a storm can only bound from below. Everything above is the floor:
the mechanisms are proven, the rates are measured, and the last 800 MB has a
subpoena out for it.

## Loose threads — numbers observed, no verdict claimed

Recorded because they were measured, not because they are convicted.

- **Endpoints outlive their agents and are never counted down.** The storm
  daemon at rest: `endpoints open=28  attached-clients=0` with `live=1`, and
  `goroutines 16 → 91` over its life. Endpoint survival is deliberate (an
  endpoint must outlive its agent), but nothing here says the count returns to
  zero, and each one is goroutines and buffers. Worth one experiment: N arias
  born and hibernated, then count endpoints.
- **`figwal loads=539`** on a daemon that answered maybe 400 pages, with a
  segment cache 2% full. The load counter suggests the same segment is being
  re-opened rather than cached (see S2(b): the tail probe re-opens per page).
- **`Page.MarshalJSON` was 13.2 MB of the storm's `Server.Update` allocation.**
  Wire encoding per streaming frame is the cost of the delta protocol and is
  probably fine; it is here so nobody re-discovers it as a leak.
- **Gluck's own daemon, mid-storm, from the outside**: `live=4 resident=4`,
  `heap alloc=69.6 MiB inuse=113.4 MiB sys=247.3 MiB`, `ir cache 6.3 MiB`.
  That is the 0.25.0 nix build (`ec3ab03a`), which predates the turn cache —
  so it cannot be the >1 GB build, and whatever grew there grew for other
  reasons. Worth asking Gluck which binary was in the fat session.

### S1 evidence from reading (pre-profile)

- aria/server.go OpenTurn: `s.cache.Append(Turn{ID: id})` — no LTs.
- aria/server.go OpenInquiry: same shape.
- figaro/agent.go:1204: `a.ariaSrv.Seal(nil)` — lts nil keeps the tail
  unbracketed even at seal.
- turncache.go noteLegacy: `len(t.LTs) < 2 || t.LTs[0] == 0` latches
  `legacyWhole` for the WHOLE cache, permanently (never cleared).
- turncache.go account(): `if b == nil || c.legacyWhole { return }` —
  under the latch nothing is accounted, so `doctor mem` shows a small
  ui window while the aria retains everything.

PRESCRIPTION (draft, pending conviction): (a) the latch must be
per-TURN, not per-cache — pin the unbracketed turn, not the world;
(b) a tail that seals via the reconcile path must receive its bracket
(Seal(nil) callers have the LTs available at seal time or one
materialization later); (c) account() must not silently skip — a pinned
turn should still be COUNTED so doctor mem tells the truth even when
eviction declines.

### Finding 0 (release daemon, 90-aria storm, 12:00): the instrument check

Mid-storm -base diff vs boot: growth dominated by sjson.appendRawPaths
+34.6MB (form-patch layer, RELEASE binary) at 90 live arias; heap inuse
145.6MB, goroutines 523. Two lessons: (a) the release daemon has NO
turn cache -- S1/S2/S3 are branch code and CANNOT be convicted here;
the >1GB was observed on the BRANCH devshell daemon, so the storm must
target that. (b) On release, the form/sjson layer is the visible
grower under aria-count load -- a pre-existing shape worth its own
line of investigation (S5: sjson allocation retention per live agent's
board mirror?).

### S1 evidence, branch daemon (12:00-12:30) -- and an anomaly past it

- 40 fresh arias, each ONE live turn: ui window resident=0 B throughout.
  Live-path turns enter via OpenTurn (bracket-less) -> legacyWhole
  latches -> account() skips. CONSISTENT with S1.
- DECISIVE-experiment twist: after daemon restart (boot re-composes
  from the log, turns bracketed) + one dormant read: resident=1 agent,
  ui window STILL 0 B. Boot-committed bracketed turns should account;
  they did not. Either (a) every history contains >=1 unbracketed turn
  (one latches the whole cache -- the per-cache blast radius is the
  bug), or (b) accounting is broken wider than the latch. The READER
  path meters correctly (1.4MiB measured 11:49 on the same tip), so
  the fault is agent-side.

PRESCRIPTION (hardened): 1) per-TURN pin, never per-cache latch;
2) Seal must stamp brackets (Seal(nil) callers have LTs one
materialization later at latest); 3) account() must COUNT pinned turns
even when declining to evict -- a meter that reads zero exactly when
retention is worst is the worst possible meter; 4) add a unit test:
boot-commit a bracketed history under a budget and assert resident>0 --
it fails today (reproduces (a)/(b)) and names the exact line under a
debugger. NEXT WAKE: dlv attach to the dev daemon, break in
TurnCache.account, inspect c.legacyWhole and c.budget on a live agent.

### S1 VERDICT (12:42): CONVICTED, latch-only — (a)

The wake-one-aria wedge: booting one storm aria committed its bracketed
history and the meter read 1.2 KiB — accounting is SOUND for composed
turns. Therefore the storm-long zero had one cause: the live path.
OpenTurn/OpenInquiry mint the tail bracket-less, Seal(nil) leaves it
so, noteLegacy latches the WHOLE cache on first contact, and every
subsequent account() — including later bracketed boot commits on that
cache — is skipped. After its first live turn, an aria retains every
sealed turn forever, unevictable, unmetered. A long-lived session is
exactly the >1GB shape Gluck observed, with doctor mem blind to it.

THE FIX (exact, for whoever applies it — internal/ untouched by me):
1. turncache.go noteLegacy: remove the cache-wide bool; pin per-TURN
   (meta[i].pinned) so one unbracketed turn pins itself only.
2. server.go Seal / agent seal path: stamp LTs at seal (the agent has
   them; agent.go:1204 Seal(nil) is the gap) — or TailMutated may
   backfill the bracket at the next materialization.
3. turncache.go account(): count pinned turns (LRU-exempt, but IN the
   resident total) so the meter can never again read zero at peak
   retention.
4. Regression test: run a live turn through OpenTurn→Seal, then Commit
   bracketed turns, assert they account and evict. Fails today.

REMAINING OPEN: S2 (reader first-sight materialize churn), S3
(aliasing pin), S4 (idle creep ~1.1MB/5s), S5 (release-side sjson
+34.6MB@90 arias). Storm rigs live in /var/tmp/storm/; dev daemon
armed at /var/tmp/figdev-storm; heap series in /var/tmp/storm/prof/.

### S1: LANDED (48c02d7a, 2026-08-14 13:30, by Gluck's order)

Per-turn pin, bracket at seal (turnFirstLT + log tail), counted-when-
pinned. Each prong held by a test that fails on the old code. Empirical
on the dev daemon: 5 live arias, meter 8.1 KiB and moving (was 0 across
40 under the bug). Gate: full suite, race x2, aria benches, nix -- green.
The ui-ir-tree gate is now OPEN.

### S5 VERDICT (13:56): named-as-cost, acquitted-as-leak

pprof -traces attribution: the +34.6MB sjson.appendRawPaths growth at 90
arias is anthropic-sdk-go's request path (MessageService.NewStreaming ->
WithJSONSet -> sjson.SetBytes) -- IN-FLIGHT REQUEST BODIES, one per
concurrent turn, ~380KB average here. Scales with concurrency x context
size; released as turns complete. Not a leak. PRESCRIPTION (optional):
if 100-way concurrency becomes normal, cap concurrent provider calls or
stream request bodies; otherwise accept as the price of parallel turns.

### S4 in progress: idle-creep sample A banked 13:54
(/var/tmp/storm/prof/idle-a.pb.gz, dev daemon idle, 0 arias, inuse
77.2MiB). Sample B + -base diff at next patrol names the creep.

### S4 VERDICT (14:30): NAMED — otel log BatchProcessor retention

-base diff over ~50 idle minutes, 0 arias: 100% of retained growth is
go.opentelemetry.io/otel/sdk/log.(*BatchProcessor).poll -> queue
TryDequeue -> slices.Clone of Records (+627KB). An idle daemon accretes
telemetry records forever -- the earlier ~1.1MB/5s figure was mostly
GC-pending alloc churn; THIS is the true retention component.
PRESCRIPTION: check the log exporter's endpoint config (an undeliverable
exporter requeues forever); bound the queue with drop-on-full; or
disable the otel LOG provider entirely if nothing consumes it.

### Patrol close-out: S2 (reader re-materialize churn) and S3 (aliasing
pin) remain open with rigs standing (/var/tmp/storm, /var/tmp/figdev-
storm armed). Neither is a leak-class suspect post-S1; both are
cost-class. v0.26.0 shipped with S1 fixed and S4/S5 named.
## SESSION 5 CLOSE (94f0752b → 6ec957ee, 2026-08-14 19:00)

Shipped: figaro v0.26.0; figwal v0.18.0 (forest). Certified by full
suites, -race x2, read-path bench parity (6.69 vs 6.78 ns/op, 0 allocs),
crashtest -long, and a 100-aria live storm on the full forest stack:
87.2 MiB heap inuse at 100 arias against the prior release's 157.9 MiB
at 90 -- with segment cache 2.1/32 MiB and ui window 73.2 KiB, both
metered through the new shape.

Verdicts: S1 convicted AND landed (the per-cache latch: the >1GB
grower). S4 named AND fixed by fork db548fc3 (otel log records export
synchronously; zero retention over 26 idle minutes; the bounded-batch
alternative measured +1.0MB and was FALSIFIED). S5 acquitted as cost
(SDK in-flight request bodies). S2/S3 open, cost-class, rigs described
above.

THE LAST LESSON, paid in 99 minutes of apparent death: an exhausted
model tier answers 429 with a Retry-After that anthropic-sdk-go honors
VERBATIM -- no ceiling, no event, no span (spans export on END). figaro
renders that as a hang and `status` reports idle. Before believing an
aria is deadlocked, check the provider tier. Parent aria ac9c3993 is
building the instruments that would have made this legible in seconds:
a wirelog round-trip ledger, a Retry-After clamp with explicit
MaxRetries, and pprof aria labels.

The role @980dc16c now points at 6ec957ee, dressed opus-5, with the
queue in its current-work key. Worktrees clean, reminders disarmed,
scratch roots removed.

### Found at close (2026-08-14 19:30), not fixed: attending a FORM mints an aria

Gluck attended role @980dc16c, typed `q test`, and a NEW ARIA was
minted.

FIRST DIAGNOSIS (WRONG, kept as the falsification): "no node-kind check
on the resolve path." There IS a role redirect there -- redirectRole,
cli/target.go:89 -- and prompt.go:47 documents exactly this case.

ACTUAL MECHANISM, read from the code: `attend` cannot bind a form AT
ALL. handlers.bind calls requireAria, which calls rpc.ValidateAriaID,
which rejects an @-sigiled id. So `attend @980dc16c` FAILS and the
shell is left unbound; the next bare prompt finds resp.Found == false
and takes the else branch -- mustCreateAndBindOutfit -- which mints.

The irony worth recording: prompt.go's role-redirect branch ("Attending
a ROLE: the prompt reaches whoever holds it, resolved per call") is
UNREACHABLE THROUGH attend, because bind structurally forbids the
binding it needs. Role redirection survives only where an @-id is
passed explicitly (send/listen/hup --id @role, via
resolveFigaroTargetEndpoint). Gluck remembers roles intercepting sends:
he is right, and that path still works.

PRESCRIPTION (successor): minting is correct for "nothing attended"; it
is WRONG for "attended to something that cannot hold a turn" -- that is
minting on a typo, and it silently orphans the user's intent. Either
(a) attend refuses a non-conversation node, naming the kind, the way
store.RequireStudyTarget already does for study/cast (KindWord exists
for this), or better (b) attending a ROLE attends its target-aria --
the semantic that matches "send @role reaches whoever holds it", and
which would have put Gluck on 6ec957ee. Either way the autoCreate must
not fire when the attended id EXISTS but is not a conversation.
