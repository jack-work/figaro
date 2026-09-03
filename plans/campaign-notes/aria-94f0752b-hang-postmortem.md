# Aria 94f0752b: "all messages hang" postmortem + diagnostic gap analysis

**Date:** 2026-08-14, ~19:00 EDT
**Investigator:** aria ac9c3993
**Binary:** figaro 0.26.0 (b65fb1e804e1, go1.26.1), angelus pid 2655396
**Subject:** aria `94f0752b`, trunk node `n856`, model `claude-fable-5`, provider `anthropic` (SDK path)

---

## 1. Verdict

**94f0752b is not deadlocked, not corrupted, and not "cut off by a fork". It is
rate-limited, the account is out of credit for `claude-fable-5`.**

> **CONFIRMED BY GLUCK, 19:30:** out of fable-5 credits. So the 429 is not a
> transient throttle at all; it persists until the balance/window renews, which
> makes every second figaro spent retrying strictly wasted, and every second it
> spent holding the aria busy strictly harmful.

Every turn since 17:09:36 gets **HTTP 429** from `api.anthropic.com/v1/messages`
within ~1 second. The `anthropic-sdk-go` retry loop then honors the response's
`Retry-After` header **verbatim and uncapped**, and sleeps inside
`RequestConfig.Execute`, emitting nothing to the stream, nothing to the log,
and nothing to the span (spans only export on *end*). The turn is alive and
silent for as long as the provider's reset window. To the user this is
indistinguishable from a hang.

Observed sleeps: **21m41s** and **76m45s**, each terminated not by the retry
completing but by the *user* giving up (`^C` → `context canceled`) or by a
daemon restart.

The bug is not "the aria broke." The bug is **figaro renders a multi-hour
provider backoff as total silence**, and no surface in the product will tell you
that is what is happening.

---

## 2. Timeline (from `traces.jsonl`)

| Span start | Span end | Wall | HTTP events | Outcome |
|---|---|---|---|---|
| 16:48:30 | 16:49:44 | 74s | 4 × 200 | ok |
| 17:09:06 | 17:31:17 | **22m11s** | 200 @17:09:17, **429 @17:09:36** | `context canceled` |
| 17:31:28 | 18:48:13 | **76m45s** | **429 @17:31:29** | `context canceled` |
| 18:48:26 | 18:48:43 | 17s | **429 @18:48:27** | `context canceled` (daemon restart) |
| 18:49:04 | 18:49:35 | 31s | **429 @18:49:06** (req_011Ce3TTTY6NjCj66TBPgp3K) | `context canceled` |

Note the shape: **one** `http.request` event per multi-hour span. It is not a
retry storm. It is a single 429 followed by one enormous sleep.

Confirming live probe (18:56, this investigation): sent one trivial prompt with
`send -f`; goroutine dump 60s later caught it parked in

```
goroutine 1232 [select, 1 minutes]:
  anthropic-sdk-go@v1.42.0/internal/requestconfig.(*RequestConfig).Execute
    requestconfig.go:475
  anthropic-sdk-go@v1.42.0.(*MessageService).NewStreaming  message.go:91
  figaro/internal/provider/anthropicsdk.(*Provider).Send.func1  anthropicsdk.go:219
  figaro/internal/provider/anthropicsdk.(*Provider).callWithAuthRetry  auth.go:72
```

`requestconfig.go:475` is the retry `select`. Probe subsequently cut with
`figaro cut 94f0752b`; aria returned to `idle` cleanly. **No state damage.**

---

## 3. Mechanism, exactly

`anthropic-sdk-go@v1.42.0/internal/requestconfig/requestconfig.go`:

```go
func retryDelay(res *http.Response, retryCount int) time.Duration {
	// If the backend tells us to wait a certain amount of time, use that value
	if retryAfterDelay, ok := parseRetryAfterHeader(res); ok {
		return max(0, retryAfterDelay)      // <-- NO CEILING
	}
	maxDelay := 8 * time.Second             // only applies to the *fallback* path
	...
}
```

Anthropic's 429 for a long-window usage cap (5-hour / weekly plan window, not a
per-minute throttle) returns `retry-after` = seconds until the window resets,
thousands of seconds. The SDK sleeps that. `MaxRetries` defaults to 2, so worst
case is **2 × window** with no output.

`internal/provider/anthropicsdk/auth.go:39-59` builds request options and sets
**neither `option.WithMaxRetries` nor `option.WithRequestTimeout`**. So figaro
inherits the SDK's uncapped policy wholesale.

### The irony worth recording

figaro already contains a *correct* hand-rolled retry policy, in the **other**
anthropic provider, `internal/provider/anthropic/anthropic.go:153-292`:

```go
const maxTransientRetries = 5
var retryBaseDelay = 1 * time.Second
var retryMaxDelay  = 30 * time.Second      // bounded!
func isTransientStatus(code int) bool { return code == 429 || code == 529 || 5xx }
slog.Warn("anthropic transient status, retrying", "status", ..., "attempt", ...)
```

That path caps the delay at 30s *and* logs each retry. **It is not the path in
use.** The live provider is `anthropicsdk`. Two providers, two retry policies,
only one of them observable, and the unobservable one is the default.

**CORRECTION (post-investigation).** I first wrote that the provider package's
`slog` was not wired into the OTel pipeline, because `grep -c 'transient status,
retrying' logs.jsonl` → 0. That inference was wrong. `otel.Init` *does* install
the otelslog bridge as `slog.Default()`, and `anthropicsdk`'s own
`"anthropicsdk cache open failed"` warning is right there in `logs.jsonl` to
prove it. The reason nothing was logged is simpler and worse: **the code path in
use logs nothing at all.** `anthropicsdk` delegates retry to the SDK, and the
SDK is silent. The good implementation was never reached; there was no
implementation to reach.

---

## 4. Why *this* aria and not the others

429s since 16:00, by aria: `94f0752b` = 4, `2afd60d1` = 1, everyone else = 0.
This is not random. 94f0752b is by a wide margin the most expensive object in
the store:

| Metric | Value |
|---|---|
| Lifetime | 2026-08-13 13:43 → 2026-08-14 18:56 (**29h**) |
| Messages | 1,448 IR records, 83 turns in trunk (710 counted incl. lineage) |
| Live context | **707,904 / 1,000,000 tokens (71%)** |
| Cache read | **258,957,482 tokens** |
| Cache write | 23,119,184 tokens |
| Output | 423,796 tokens |
| **Total input-equivalent** | **~282.5M tokens** |
| Avg cache-read per turn | ~365k tokens |
| On-disk | ir/n856 1.9 MiB, translations-v2/anthropic/n856 3.1 MiB |

A quarter-billion cache-read tokens through one conversation in 29 hours. Every
turn re-presents ~708k tokens of context. Long-window quotas count that. **This
aria ate the account's window**, and being the single largest request in the
fleet it is also the first refused and the last to fit once headroom returns,
classic head-of-line starvation of the fattest request.

**This is a capacity story, not a corruption story.** The aria did it to itself
by growing to 71% of a 1M window and then running 83 turns at that size.

---

## 5. What the aria was actually doing (task systematization)

94f0752b is the **fifth holder of the figaro state-layer role `@980dc16c`**
(predecessor `b2b0c543`). cwd `/home/gluck/dev/figaro-qua/incant`. Mantra:
*"Storm triage: profile the burn, prescribe the cure."*

Arc across its 57 user prompts:

1. **Role handoff** (idx 4), inherit state-layer role @980dc16c from session 4.
2. **figwal generalization** (idx 1294-1296), "one Go type with type-parameterized
   units", the decode-IR layer joined to the same tree; rip out the old code.
3. **Fork discipline** (idx 1280), forked a warm-up aria (`db548fc3`), mantra set,
   task registered as a KVP on the role so success is externally visible.
4. **Storm benchmarking** (idx 1373), "get benchmarks and run the 100-aria test".
5. **Verdict + release** (turn 79, 17:09), 100-aria forest storm: heap inuse
   **87.2 MiB @ 100 arias** vs release baseline **157.9 MiB @ 90** (−45% at higher
   load); segment cache 2.1/32 MiB; ui window 73.2 KiB (S1 fix confirmed); no
   leak. Called GREEN, **tagged and pushed figwal v0.18.0** ("forest, one cache
   to shape them all"). That push is the last successful API round-trip.
6. **17:31 onward**, user notices silence, three prompts land unanswered.

The last thing it did was ship. It died on the victory lap, not mid-surgery.
Its work product is committed and pushed; nothing is stranded in context.

Its remaining declared plan was: **uptake of figwal v0.18.0 into figaro's tree
branch** as the next campaign, plus a final report to Gluck.

The `figstorm-bin` process (pid 2398478) with ~100 aria sockets under
`/var/tmp/figstorm-root/run/figaros/` is that storm harness, still resident.

---

## 6. Collateral findings

- **Stale daemons.** Three figaro processes live: `1961781` (0.25.0, since 14:04,
  cwd formdeltas), `1541875` (0.25.0, since 12:07), `2655396` (0.26.0, the real
  angelus). `angelus.startup` shows repeated `another angelus already owns this
  store; exiting` at 03:52, 14:38, 18:48, losers exit, but two 0.25.0 processes
  are still holding fds. Version skew across a shared store is a latent hazard.
- **`angelus.log` is dead.** Last line 2026-05-13. Everything real goes to
  `logs.jsonl` / `metrics.jsonl` / `traces.jsonl`. The file that *looks* like the
  daemon log has been lying for three months. Delete it or write to it.
- **Trace export is end-of-span only.** An in-flight turn contributes *nothing* to
  `traces.jsonl`. The exact situation you most need to debug is the one that
  produces no trace data.
- **`http.req_bytes: 0`** on every request event, the field exists and is never
  populated. Would have instantly shown "this is a 700 KB request".
- **`retry-after` is never recorded** anywhere. The single number that explains
  the entire outage is not captured by any log, metric, or span attribute.
- **Unrelated but present:** `WARN anthropicsdk cache open failed; running
  uncached {aria: ba1f5894, err: xwal: unknown trunk "ba1f5894"}` and `WARN
  tombstone: form unreadable` for the same aria. ba1f5894 has a store-registration
  bug worth its own look.
- 334 MiB in `arias/`, plus ~14 MiB of `arias.legacy-*` / `arias.bak` /
  `arias.pre*` snapshots (18 of them) never collected.

---

## 7. Diagnostic gap analysis: what I had to build by hand

`figaro doctor` today offers: `gc`, `schema`, `term`, `mem`, `librettos`,
`skills`. **All six are about the store and the process. Not one of them can
answer "why is this aria not responding".** Neither can `status`, which cheerfully
reported `state: idle` while three prompts sat unanswered in the IR.

Everything below I had to hand-engineer with `python3`, `grep`, `curl` against
`pprof.sock`, and direct reads of the on-disk WAL. Each row is a proposed
surface.

### 7.1 The missing command: `figaro doctor aria <id>`

There is no per-aria health check. This should be the first thing anyone runs
and it does not exist. What it should print:

```
figaro doctor aria 94f0752b
  state          idle (last turn ENDED IN ERROR 9m ago)
  node           n856    ir 1.9 MiB / xlat 3.1 MiB / form 28 KiB
  context        707,904 / 1,000,000  (71%)  ⚠ large-request risk
  queue          empty
  UNANSWERED     3 input records after last assistant output (lt 1445-1447) ⚠
  last provider  429 Too Many Requests  @18:49:06  req_011Ce3TTTY6NjCj66TBPgp3K
                 retry-after: 3600s  → SDK slept, turn cancelled before retry ⚠
  last 5 turns   200 200 200 429 429
  diagnosis      RATE LIMITED. Not hung. See `figaro doctor provider`.
```

**How I had to get each line instead:**

| Line | What I actually did |
|---|---|
| aria → store node (`n856`) | grepped `logs.jsonl` for "log opened" paths around the restore timestamp. Nothing maps id → node. |
| unanswered inputs | hand-parsed `ir/n856/*.jsonl` in python and eyeballed that the tail was 4 consecutive `role:input` records |
| last provider status | wrote a python scanner over `traces.jsonl`, JSON-dumped the last span, read `Events[].http.status_code` |
| retry-after | **impossible.** Not recorded anywhere. Inferred from span wall-clock. |
| is a turn in flight | `curl --unix-socket pprof.sock 'goroutine?debug=2'` and read Go stack frames |

### 7.2 Gaps, ranked by how much time each cost me

1. **No provider round-trip ledger.** `traces.jsonl` has the truth but it is an
   OTel dump nobody can read without writing a parser, and it only lands on span
   end. Want: `figaro doctor provider [--id <aria>] [-n 20]` → a table of
   timestamp / aria / model / status / duration / request-id / retry-after /
   req-bytes, **including in-flight requests**.
2. **Retries are invisible by construction.** No stream event, no log record, no
   metric. The SDK sleeps and figaro says nothing. Want: emit a first-class
   `provider.retry` UI event (`⏳ rate limited (429), retrying in 58m, ^C to
   abandon`) so `figaro listen` shows it. **Silence is the actual defect.**
   Everything else here is instrumentation for a defect that should not exist.
3. **`status` lies by omission.** `state: idle` is true and useless when the last
   turn died in error. Want `status` to carry `last_turn_result:
   error|ok|cancelled` + `last_error` + `unanswered_inputs`.
4. **pprof goroutines carry no aria identity.** 86 goroutines, several agents,
   no way to tell which stack belongs to which aria. One `pprof.Do(ctx,
   pprof.Labels("aria", id), ...)` at agent start makes every future hang
   trivially attributable. **Cheapest high-value fix in this document.**
5. **No way to read the IR without writing a parser.** `figaro show` renders for
   humans; there is no `figaro doctor ir <id> --tail 5 --raw` that dumps the
   raw records with `_idx`, `role`, `turn_id`. I used python on the WAL, which
   means I was reading a format with no compatibility promise.
6. **Two `figaro doctor` subcommands refuse to run at all while angelus is up**
   (`gc`, `librettos`: "stop it first"). During an incident you do not want to
   kill the daemon to inspect it. Want read-only variants that talk to the
   running daemon.
7. **No fleet view of provider health.** I had to aggregate 429s per aria in
   python to discover this was one aria's problem and not an outage. Want
   `figaro doctor provider --summary`: status-code histogram by hour and by aria.
8. **No context-budget warning.** Nothing ever told anyone that an aria at 708k
   tokens with 259M cache-reads was going to trip a quota. Want a threshold
   warning in `status`/`ls` at e.g. 60% of context limit, and a lifetime-burn
   column. `doctor mem` counts *bytes*; nothing counts *tokens*.
9. **`http.req_bytes` is always 0.** Populate it.
10. **`angelus.log` is a decoy.** Three months stale. It is the first file anyone
    opens.

### 7.3 What already worked well, credit where due

- `pprof.sock` being on by default is the reason this took an hour and not a day.
  `doctor mem` printing the exact `go tool pprof` invocation is excellent CLI
  manners.
- `traces.jsonl` **had every fact needed**, correctly attributed with `figaro.id`.
  The data model is right; only the *interface* to it is missing.
- `figaro cut` cleanly reclaimed a wedged turn with zero state damage.
- The store survived a 77-minute stall, a daemon restart, and a version skew with
  `tornBytes: 0` on every segment recovery. figwal did its job.

---

## 8. Remediation

**To use 94f0752b right now:**
1. The quota window is the gate; nothing else. Wait for reset, or
2. `figaro fork 94f0752b -- <prompt>` at a lower turn to shed context, or
3. re-dress it onto a smaller-context model for the wind-down, then
4. hand its role `@980dc16c` to a sixth holder, 708k of context is past the
   point where continuing is economical. Its work (figwal v0.18.0) is tagged
   and pushed; the handoff note is the only thing owed.

**Code, in priority order:**
1. `option.WithMaxRetries(2)` **and** a `retryDelay` ceiling in
   `internal/provider/anthropicsdk/auth.go`, never sleep more than ~60s on a
   `Retry-After`. Past that, **fail loudly with the retry-after value in the
   error**. A one-hour silent sleep is never the right product behavior.
2. Emit a `provider.retry` event onto the aria stream. This alone converts
   "figaro is broken" into "figaro is waiting, and says so".
3. Wire the provider packages' `slog` into the OTel log pipeline. Right now
   `slog.Warn` in `internal/provider/*` goes nowhere.
4. `pprof.Labels("aria", id)` around the agent goroutine.
5. Then build `figaro doctor aria <id>` and `figaro doctor provider` (§7.1, §7.2).

**Housekeeping:** kill pids 1961781 and 1541875; delete or revive `angelus.log`;
`figaro doctor gc` the 18 `arias.*` snapshots; look into `ba1f5894`'s
`xwal: unknown trunk` warnings.

---

## Appendix: repro commands used

```sh
figaro status 94f0752b -j
figaro queue ls --id 94f0752b -j                 # empty, proves prompts were consumed
tail -c 6000 ~/.local/state/figaro/arias/ir/n856/*.jsonl   # 4 consecutive role:input at tail
curl -s --unix-socket /run/user/1000/figaro/pprof.sock 'http://x/debug/pprof/goroutine?debug=2'
python3 - <<'EOF'   # the scanner that found it
import json
for line in open('/home/gluck/.local/state/figaro/traces.jsonl','rb'):
    d=json.loads(line)
    if '94f0752b' not in json.dumps(d): continue
    for e in (d.get('Events') or []):
        if e['Name']=='http.request':
            print(e['Time'][11:19], {a['Key']:a['Value'].get('Value') for a in e['Attributes']})
EOF
```

That 12-line scanner is the missing product feature. It should be `figaro doctor
provider --id 94f0752b`.


---

## 9. Shipped (2026-08-14, aria ac9c3993)

Five PRs against `jack-work/figaro`. Two stacks and two independents.

| PR | branch | base | what |
|---|---|---|---|
| [#17](https://diffshub.com/jack-work/figaro/pull/17) | `fix/wirelog-round-ledger` | `main` | telemetry: record `retry-after`, the ratelimit headers, real `req_bytes`, aria attribution; add the in-memory round-trip ledger with in-flight rows |
| [#18](https://diffshub.com/jack-work/figaro/pull/18) | `fix/anthropicsdk-retry-cap` | #17 | a spent quota **fails the turn at once** (`x-should-retry: false`) instead of sleeping through it; explicit `MaxRetries`; loud log + loud error |
| [#19](https://diffshub.com/jack-work/figaro/pull/19) | `perf/pprof-aria-labels` | `main` | `pprof.Do` labels the agent goroutine and its whole subtree with the aria id |
| [#20](https://diffshub.com/jack-work/figaro/pull/20) | `feat/doctor-provider` | #17 | `figaro doctor provider [--id] [-c N] [-j]`, the round-trip table plus a verdict paragraph; new `angelus.provider_ledger` RPC |
| [#21](https://diffshub.com/jack-work/figaro/pull/21) | `feat/status-last-turn-result` | `main` | `status` gains `last-turn:` and `unanswered:`; `listen` routes error reasons to the status bar notice instead of stderr |

Diffshub: `https://diffshub.com/<owner>/<repo>/pull/<N>`.

### What #18 changed relative to §8's original prescription

The first draft merely *capped* the wait (clamp `Retry-After` to 60s, retry
twice, worst case 2 minutes). Once Gluck confirmed the cause was a spent
balance, capping was revealed as still wrong in kind: retrying a spent quota is
pointless at any interval, and any wait at all holds the agent loop so the user
cannot send the message that would fix it (fork, downshift, top up). The shipped
behavior is **refuse the retry outright** past the cap and hand the aria back
within the second. Short throttles are still ridden out, the cap is a ceiling,
not a refusal.

### Still not built

- `figaro doctor aria <id>` (§7.1), the single-command health check. #20 and
  #21 cover most of its lines between them; the aria→store-node mapping and the
  raw IR tail dump are still hand work.
- A `provider.retry` / rate-limit event on the **aria stream** as its own message
  type. #18 makes the error visible via the existing turn-error path and #21
  puts it in the status row, which covers the reported need without a
  `provider.Bus` interface change touching every implementer.
- Housekeeping from §6: stale 0.25.0 daemons, the decoy `angelus.log`, the 18
  uncollected `arias.*` snapshots, `ba1f5894`'s `xwal: unknown trunk`.

### Warning for whoever picks this up

Another aria is committing in `figaro-qua` concurrently, a duplicate of the
pprof commit landed on `feat/doctor-provider` mid-session and had to be rebased
out, and there is an existing `fix/provider-error-visible` branch that may
overlap #21. Check `git worktree list` and the branch set before assuming a
clean base.
