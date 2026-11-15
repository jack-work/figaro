# Succeed while the outgoing prompt is still warm

Proposal, 2026-11-15. No implementation or live policy change is included.

## Recommendation

Fix the existing fermata clock, not the prediction model. Recognize successful Responses HTTP and WebSocket rounds as well as Messages, then send the existing succession nudge two minutes before a declared idle budget ends, instead of one minute before an assumed TTL. Keep the existing opt-in, context threshold, dwell and hold controls.

For ChatGPT subscription seats, make that budget explicit: propose `succession-policy.cache-idle-budget = "5m"` for an approved pilot. It is a conservative scheduling choice, not a discovered OpenAI expiry guarantee. With a 120-second lead, the nudge starts after three minutes without another request. Do not infer one-hour retention from an Anthropic cache-control setting or from one earlier cache hit.

Keep d3's P(break) in dry-run. It estimates whether the *next natural turn* will miss, not the instant at which a warm handoff must start. It cannot fix a missing clock or an insufficient queue allowance.

Approval requested: implement this small fermata change and review its offline replay, then name the existing roles for a bounded live pilot. No new workers, no figaro daemon release, no daemon restart. This proposal does not itself authorize either installation or policy edits.

## What already exists

- `tools-figaro-steward@9eda14582cfe9d46ebe3b138249da041f4edd1d7` adds `cache-succession/pbreak.py` and `cache_break_tree_d3.json`, logging `break_tree` from `watch.py`. The rule is unchanged: eligible idle holder, expiry within 60 seconds, sufficient context versus successor cost. The file also contains live ceiling nudges and policy/skill maintenance: running it is not a read-only way to inspect the dry-run model. I read it, never executed it.
- The tree records 42,496 training turn ends before November 12 and a separate 300-row test result (Brier 0.1216, weighted AUC 0.789). Its split features are `ttl1h`, local weekday, `log_last_gap`, and `log_med_gap3`. Its leaves predict P(break) from 0.1318 to 0.771. None is a deadline or a latency budget.
- Local `figla@f351244` is the active sender. `fermata.go` already has `--every 20s`, `--lead 60s`, `--dwell 2h`, per-role `cache-succession`/`cache-threshold`, `fermata-hold`, one ping per seating and a separate `expired-queued` fallback. These are sufficient lifecycle controls. Do not build a second sender in the steward.
- `gauge.go:tripLog.take` accepts only `POST`, URL ending `/messages`, HTTP status 2xx. It reconstructs the request start, not completion, from timestamp minus duration. Responses streaming records use method `SSE` (HTTP) or `WS`, and a successful `stream_status`, without an HTTP status: both are rejected. A transport-level `POST /responses` record does not substitute for a completed stream.
- The default cache duration is five minutes unless `system.cache_control` says otherwise. That is an Anthropic-oriented assumption, not a subscription retention measurement.

### Actual current failure

The latest fermata records for the seven enabled roles show four above-threshold holders stuck on `no-trip`: `c9dc565a` (65.8%), `099ddcff` (30.6%), `ec348a68` (30.4%), `16649988` (37.0%). The other three are below threshold. This is a source-path failure before the timing rule gets a chance to run. Raising P(break) or changing its threshold would do nothing for these holders.

Across the retained fermata log, there were nine pre-expiry pings and two `expired-queued` pings. All nine pre-expiry pings were followed by a first request 0.93 to 12.36 seconds later, with at least 99.9% cache reads. The old trigger did work on those routes. One late ping had 0.6% cache reads; the other had 98.1% on a new subscription route despite a hugely stale clock. An old route's expiry estimate must not be carried into its replacement.

## Idle gaps in real traffic

Snapshot: November 15, 16:38:46 UTC (11:38:46 EST). Source: gluck's read-only figscope SQLite database, ended `figaro.round` spans. All 51,990 rounds in this extract have host `gluck`; this is not Spain data.

I joined consecutive whole turns of the same host and figaro, comparing the previous turn's last round with the next turn's first round. Gap means previous round end to next round start, excluding the previous generation time. The scheduler must still use request start as the conservative cache clock, not this descriptive idle gap.

Only same-provider, same-model, same-`service.version` pairs are compared. A previous turn must end normally. Missing usage and errors are excluded. Of 9,695 observed turns, 6,344 transitions pass the general filters. Exclusions: 517 error/non-stop transitions, 2,017 unversioned, 368 route/version changes, three zero-usage; a seat's initial turn has no predecessor.

For the break tables, the previous last round must already be warm (cache reads >=50% of its total prompt), and the next prompt size must be 0.75 to 1.5 times the previous size. A *break candidate* means the next first round reads less than half the previous prompt from cache. Prompt total is input + cache_read + cache_creation, since input is exclusive. This is a usage-based proxy: it does not prove TTL eviction, unchanged account, unchanged tool schema or byte-identical prefixes. No payload, headers, credentials or arbitrary event attributes were read.

### Anthropic: a clear five-minute edge

3,390 warm, size-comparable transitions; 1,110 break candidates.

| Idle gap | Transitions | Break candidates |
|---|---:|---:|
| <1 min | 1,533 | 0 |
| 1 to <3 min | 481 | 0 |
| 3 to <5 min | 254 | 0 |
| 5 to <10 min | 294 | 282 |
| 10 to <30 min | 488 | 488 |
| 30 to <60 min | 206 | 206 |
| >=60 min | 134 | 134 |

Break-gap p10 / median / p90: 6.74 / 19.18 / 67.06 minutes. Waiting for the next natural message loses the warm handoff on these long gaps. A timer at request-start + TTL - lead is the right mechanism.

Selected per-seat results, not pooled into a single fictional seat:

| Holder | Warm comparable transitions | Break candidates | Median gap before break |
|---|---:|---:|---:|
| Engineer VII, `33799a4b` | 292 | 83 | 10.56 min |
| King XII, `7cff4bf1` | 242 | 69 | 20.73 min |
| Engineer VI, `711ddbc9` | 258 | 65 | 12.79 min |
| `b7f1f42c` | 296 | 55 | 13.76 min |
| Edge, `bb1a22ac` | 168 | 44 | 19.94 min |
| Vigía, `66f55e85` | 51 | 33 | 15.52 min |

The full per-seat table is [per-seat.csv](per-seat.csv). These include historical, retired holders. They are not a list of seats to wake or a counterfactual claim that all would meet today's policy gates.

### ChatGPT on 0.69.9: too little evidence for a precise TTL

Only five warm, comparable transitions on the configured `chatgpt-personal/gpt-6.1-sol` route pass these filters:

| Holder | Observed idle gaps | Result |
|---|---|---|
| Engineer VIII, `e8e5c2fa` | 14.83, 24.33, 41.43 min | All retained >=half the previous prompt |
| `404fd2a2` | 59.92 min | Break candidate |
| `2d4075e8` | 228.50 min | Break candidate |

Six other same-route/version transitions are excluded from this break cohort because they did not start warm. A zero-hit seat is not evidence of *losing* a known warm prefix. These figures are distinct from the earlier rolling fleet cache fractions.

This supports neither a fleet-wide one-hour TTL nor an expiry at five minutes. The proposed five-minute budget is deliberately earlier than these observed warm returns. Its cost is potentially unnecessary earlier succession; its benefit is not waiting until an unmeasured subscription retention deadline. If that trade is unwanted, leave subscription warm-cache succession disabled until a budget is named. Do not silently choose an hour.

The .6 and .8 intervals contain known prefix/route confounders and short-gap misses; do not train a new subscription TTL from those regressions. The .7-versus-.8 causal verdict remains open.

### d3 assessment

The tree has no elapsed-idle input. Its prediction at a turn end is unchanged at one second, three minutes and an hour afterward. It cannot decide *when* to fire without a separate deadline clock.

A chronological replay using the same d3 tree and feature code, but safe round-end timestamps rather than original IR output timestamps, yields for the 3,390 Anthropic transitions: mean predicted break probability 37.21%, observed 32.74%, Brier 0.2211. This is a different, filtered cohort spanning training dates, not a replication or out-of-sample rebuttal of the original 0.1216 result. Subscription .9 has only five observations: no calibration claim is possible.

The highest-risk Anthropic leaf (P=0.771) had 114 breaks in 236 comparable returns, not 77.1%. A P>0.5 gate would omit many of the real five-minute misses in other leaves. Do not let this uncalibrated probability veto the deadline. Leave it logged for later review.

Also, `pbreak.py` derives gaps between output timestamps, including the following generation time, while this report uses actual round start/end boundaries. Its observed `ttl1h` flag is evidence of a previous long-gap hit, not a provider contract. Promoting the tree requires resolving those definitions and a later held-out cohort; neither is needed for this fix.

## Small implementation

One owner: figla fermata. Leave the steward's d3 logging and ceiling rule alone.

1. Repair `tripLog.take`. Keep successful HTTP `POST .../messages`; add completed `SSE .../responses` and `WS .../responses` streams. Parse the existing bounded `stream_status`/`stream_err_class` fields: accept terminal `completed`/`ok` with no stream error. A 429, failed stream, GET, unrelated endpoint or a Responses HTTP 200 header record alone must not refresh the cache clock. Reconstruct request start from the completed-stream timestamp minus duration, not stream finish. Never print URLs, headers or error bodies in decision logs.
2. Fence route changes. Track the holder's provider/model with the reading. After a detected route change, discard its previous clock and wait for a new matching successful request observed after the change. Restart bootstrap that cannot associate the clock with the current route is `clock-unknown`, not a fabricated warm cache. A holder change still resets the seating's ping state. No attempt to prove account identity by reading credentials.
3. Declare the idle budget where retention is unknown. Add one optional duration: `succession-policy.cache-idle-budget`. On Messages routes the existing declared 5m/1h TTL remains the default. On Responses subscription routes require the explicit budget; without it log `retention-unknown` and do not send a warm-expiry nudge. Suggested pilot value: 5m. Caching explicitly off still disables warm-cache nudges. This is a scheduler setting, not a wire cache-control override.
4. Use the existing `--lead`, initially 120s. Due = request start + budget - lead. With the existing 20-second scan, a five-minute budget leaves about 100 to 120 seconds at delivery. The known previous-route nine-ping sample needed <=12.36s to start; the .9 subscription first-round p95 is 22.23s across only 11 comparable-route returns, while the larger .6 route cohort's p99 is 57.36s. These are observations, not worst-case bounds. Two minutes allows the scan plus delivery and normal queue time without inventing a hard queue guarantee. The first request is what must arrive warm; subsequent tool rounds refresh the clock, so do not budget an entire multi-round handoff as one idle period.
5. Keep lifecycle gates and one nudge. `cache-succession`, `cache-threshold`, two-hour dwell, `fermata-hold`, idle state, re-resolve the current holder before sending, deduplicate across its roles. A new natural request invalidates the old deadline. Never interrupt an active turn, repeatedly nudge an in-flight succession, or use the timer as a cache keepalive. Keep expired/queued recovery distinct: it cannot be counted as a warm success.
6. Make the message truthful. Name the actual remaining budget, not the hard-coded `<60 s`. For a declared subscription budget say "warm-cache succession budget ends in ...", not "the provider guarantees expiry in ...". Add clock source, route, budget and remaining seconds to the existing decision log; no new metrics service.

A local scheduler cannot guarantee a cache hit if the upstream evicts early, the account changes or the provider queue exceeds its lead. If the deadline is already past, record a missed opportunity, do not manufacture a warm-cache success or resend a running turn. Do not switch providers.

This preserves the useful work already present: policy, seat deduplication, timer, handoff instruction and the dry-run predictor. No daemon, store, router, custody or model-route change is required. It does not lower the normal 60/70% context thresholds or transplant the steward's 75k successor-cost estimate/Opus dollar prices onto subscription traffic.

## What "keep the cache" means

The outgoing holder writes its handoff while its large prompt is warm. The successor then starts with a smaller prompt. This does not transfer an opaque provider cache into an unrelated fresh prompt. A successor may have a cold first call; the saved work is avoiding a cold re-prime of the much larger outgoing holder. No inference probes or periodic warmup prompts are proposed.

## Bounded check and rollout, if approved

Extend the existing gauge/fermata tests, not a new harness: Messages and Responses HTTP, completed WS, failed/partial stream, route change, unknown retention, 120-second lead boundaries, busy/held/under-threshold seats, new natural request before delivery, role reassignment and a long provider queue. No broad figaro gate for a watcher-only change.

Replay the frozen safe timestamps to show proposed due times before long-gap returns, explicitly retaining policy eligibility and unknown-retention exclusions. That replay can measure timing opportunities, not guaranteed token savings or actual successions. Then install only under King's permission and enable only his named existing roles. Observe ordinary work: first old-holder request's actual cache fraction, nudge-to-first-output delay, role holder transition, successor first-call cost. No model probes. One report after natural traffic; do not mint a fleet to populate the sample.

## Evidence and scope

- [Per-seat table](per-seat.csv), [cohort totals](cohorts.json), [d3 leaf replay](tree-leaves.csv), [read-only analysis script](analyze.py), [tree replay script](score.py).
- Reproduce locally with `python3 analyze.py /tmp/warm-cache-replay`, then `python3 score.py /tmp/warm-cache-replay /path/to/tools-figaro-steward/cache-succession`. The analysis freezes the maximum round end at `1794760718881021038` ns. An optional second argument to `analyze.py` supplies an existing `figaro list -j` snapshot for display handles; it never submits a prompt. No need to run `watch.py`.
- Underlying safe numeric extract and full transition rows remain local at `/tmp/warm-succession-evidence/`; prompt content and credentials were never extracted. The script selects named fields only, never dumps `attrs` or `events`. Later ingestion or retention can affect a rerun even with the frozen cutoff. Both attached scripts were executed successfully against this snapshot.
- Source inspections: steward commit `9eda1458`; figla `f351244`, `gauge.go`, `fermata.go`, `fermata_test.go`; figaro `180d4906`, `internal/wirelog/stream.go`, `inflight.go` and `api/rpc/admin.go`.
- Operational disclosure: `figla fermata --help` ignores the help flag and briefly started a second watcher during discovery. I killed that exact process. Its 11:33 EST records contain seven evaluations and zero pings; no service was restarted. Do not use that command to inspect help. No watcher rule, role policy, daemon, provider configuration or deployed binary was changed for this proposal.
