# Performance baseline

Measured 2026-10-13 on the owner's machine against a copy of the real store
(1485 entries, 1338 conversations, 1.4 GB). OLD is 0.38.2 at `a62f412e`
(`main`), NEW is `a4481082` (`terraform`), both flake builds. Raw captures,
scripts and per-run tables are under `/var/tmp/perf/{A,B,C,D,D2}`.

## What real usage spends time on

Five days of the live daemon's telemetry (`metrics.jsonl`, `traces.jsonl`,
`logs.jsonl` under the state dir, plus the `_meta` sidecars).

| | value |
|---|---|
| user turns per day | 133 |
| provider rounds per turn | 8.7 |
| turn wall p50 / p90 / max | 52 s / 308 s / 66 min |
| one provider round p50 / mean (claude-opus-5) | 8.7 s / 12.4 s |
| time to first byte p50 / p90 | 1.84 s / 4.35 s |
| gap between rounds p50 / p90 (tools + catch-up + body) | 10.2 s / 48 s |
| bash tool p50 / p90 / p99 | 108 ms / 3 s / 59 s |
| request body p50 (anthropic / copilot) | 1.19 MB / 2.1 MB, about 2 GB uploaded per day |
| tokens lifetime: cache_read / cache_write / uncached input | 44.7 G / 2.46 G / 51 M |
| prompt cache hit rate | 94.7% |
| context p50 / p90 | 106k / 516k tokens |
| round failure / turn failure | 0.32% / 3.8% |
| translator catch-up (old build logs) p50 / p90 | 322 ms / 2.6 s, `visited=0` at the median |
| sweeps | every ~14 min, evicting p50 1226 handles; 3.9 GB/day segment-cache churn |
| `wal.sync.batch` | 1 on all 13,476 fsyncs (group commit never coalesces) |

Provider streaming is about 78% of turn wall, the inter-round gap about 20%,
store and CLI under 1%.

## The metric set

Registered in `internal/otel/otel.go` `initInstruments`; all land in
`metrics.jsonl` through the existing file exporter. Latency histograms carry
explicit ms boundaries.

| metric | attrs | site |
|---|---|---|
| `figaro.request.duration` | provider, model, status | provider round, `figaro/turn.go` |
| `figaro.provider.ttft` | provider, model | first content delta, `figaro/turn.go` |
| `figaro.turn.duration`, `figaro.turn.rounds` | provider, model, status | turn end, `figaro/turn.go` |
| `figaro.tool.duration` | tool, sandbox, err | around `Execute`, `figaro/turn.go` |
| `figaro.translator.catchup`, `.visited`, `.encoded` | provider | `provider/catchup.go` |
| `figaro.tokens` | kind, provider, model | usage fold, `figaro/agent.go` |
| `figaro.provider.request_bytes` | host | counting body reader, `wirelog` |
| `figaro.rpc.duration` | method, status | handler dispatch, both doors |
| `figaro.store.open` | | cold open miss path, `store/xwal_backend.go` |
| `figaro.sweep.duration`, `figaro.sweep.evicted` | phase | evict tick, `angelus/angelus.go` |
| `figaro.mem.rss`, `.heap_inuse`, `.heap_released`, `.resident_arias`, `.goroutines` | | observable gauges, 30 s |
| `figaro.wal.sync.duration`, `.batch` | | form channel fsync (pre-existing) |
| marks `boot.begin`, `boot.ready`, `ls`, `show` | | `FIGARO_MARKS=<file>` |

Trace span events no longer drop: the per-frame fanout events are gone, so
a long turn keeps its http and tool events.

## Baseline numbers, OLD vs NEW

### Daemon and CLI on the real-store copy (4 passes per build, ABAB)

| metric | n | old med | new med | ratio |
|---|---|---|---|---|
| cold start to first `ls --json` | 4 | 1300 ms | 1340 ms | 1.03 |
| `ls --json` warm | 28 | 64 ms | 66 ms | 1.03 |
| `ls -g -a` warm | 28 | 59 ms | 59 ms | 1.00 |
| `show -n 3` cold / warm | 160 | 44 / 41 ms | 45 / 42 ms | 1.02 / 1.04 |
| `status -j` | 160 | 56 ms | 57 ms | 1.02 |
| `state` | 160 | 16 ms | 16 ms | 1.00 |
| open 200 arias | 4 | 9.3 s | 9.0 s | 0.96 |
| daemon RSS idle / after 200 opens | | 256 / 420 MB | 236 / 426 MB | 0.92 / 1.01 |
| client RSS `ls --json` / `show` | | 40.6 / 32.7 MB | 37.7 / 31.5 MB | 0.93 / 0.96 |

### Live turns (own store copy, alternating builds)

| step | n | old med | new med |
|---|---|---|---|
| fresh aria, one-word reply | 4 | 3.2 s | 2.2 s |
| `send` on a 3013-message aria, warm cache | 3 | 4.2 s | 4.2 s |
| tool turn, local | 5 | 4.2 s | 4.2 s |
| tool turn, bwrap (new only) | 5 | | 4.2 s |
| `bash` tool elapsed for `true`, local / bwrap | 5 | 631 ms | 622 / 693 ms |
| long-aria request body | | 941 KB | 941 KB |
| long-aria cache_read per turn | | 349k | 349k |

bwrap costs about 71 ms per tool call over local.

### First touch of a cold aria, same arias, fresh copies per build

Pre-request time is the translator catch-up before the HTTP request leaves.

| aria msgs | old | new | ratio |
|---|---|---|---|
| 113 | 0.43 s | 0.41 s | 0.95 |
| 111 | 0.45 s | 0.46 s | 1.01 |
| 1261 | 4.9 s | 5.2 s | 1.06 |
| 1588 | 10.9 s | 6.7 s | 0.62 |
| 2209 | 8.5 s | 8.5 s | 1.00 |
| already materialised, ~2300 | 0.31 s | 0.15 s | 0.50 |

Cost scales at roughly 4 ms per message on both builds. The cause is in
`provider/catchup.go`: the hole check walks the whole translator log and the
whole IR on every cold open, and a hole anywhere clears the cache and
re-encodes from zero. The fix is to repair from the gap forward instead of
clearing, and to record coverage at append time so the check needs no walk.

### Go micro-benchmarks

See `/var/tmp/perf/C/` (benchstat, `-count=4`). Under matched load every
package compared so far is flat within 2%.

## Reproducing

Binaries: `nix build <tree>#default -o /var/tmp/fig-<name>`. Isolated
daemon: `FIGARO_RUNTIME_DIR`, `FIGARO_STATE_DIR` (a `cp -a` of the store),
`FIGARO_CONFIG_DIR=$HOME/.config/figaro`, `TMPDIR=/tmp` for the shared hush
agent, `FIGARO_MARKS=<file>` for the client marks. One daemon per state
dir; `figaro stop` before switching builds. The per-family harness scripts
are `pass.sh` (B), `turn.sh` (D, D2) and the `analyze.py` beside each.
