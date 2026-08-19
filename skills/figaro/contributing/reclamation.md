# Reclamation: what figaro holds in memory, and how it lets go

**When to read this.** You are changing `internal/angelus` (the hub, the sweep),
`internal/store` (the row caches), or tuning `[memory]` in `config.toml`. Or a
daemon is bigger than you expect and you want to know which of several caches is
holding it.

The short version: **an aria's liveness is a memory decision, invisible to
anyone attached to it.** Attending does not wake. Listening does not wake. Going
dormant does not disconnect a client, kill a background job, or lose a frame.
Waking resumes the stream mid-flight.

## What a live aria costs

Measured on two real arias, not estimated
(`internal/angelus/realaria_probe_test.go`, env-gated: point it at a *copy* of
a store):

| holder | cf3fc17d (2556 msgs) | e83ae209 (1760 msgs) |
|---|---|---|
| **decoded IR** (`cachedLog`) | **12.5 MiB: 86%** | **14.3 MiB: 63%** |
| composed UI (`aria.Server`) | 2.0 MiB: 14% | 2.4 MiB: 10% |
| translations, per provider | 1.2 KiB: 0% | 6.0 MiB: 26% |
| form | 48 KiB | 43 KiB |
| **total** | **14.5 MiB** | **22.7 MiB** |

The decoded IR runs **4-5x its encoded bytes** and dominates. Two consequences
that are easy to get backwards:

- **Synthetic data lies about this.** Uniform small prose messages make the
  composed UI look like 1.5x the decoded IR. On real arias it is 0.2x: tool
  calls and large results inflate the IR far more than the projection of them.
  Any claim about which holder dominates has to be made against a real store.
- **Translations are wildly variable.** Kilobytes on a single-provider aria,
  megabytes on one with a provider switch in its lineage.

## Three layers reclaim, each owning only its own

| layer | drops | trigger | where |
|---|---|---|---|
| **figwal** | a lineage's in-RAM head; buffers flushed then unloaded | `IdleUnload`, 5 min | `figwal/xwal/store.go` `evictIdle` |
| **backend cache** | decoded IR, translations, board, meta | `EvictIdle(live, dormantAfter)`, refuses anything live | `store/xwal_backend.go` |
| **agent** | the whole agent: composed UI, goroutines, provider binding | `Registry.Hibernate`, `dormant_after_minutes` | `angelus/registry.go` |

They cascade: hibernate drops the agent → the aria stops being `live` → the
backend sweep can now reach its caches → figwal unloads the head beneath that.
Each layer only reclaims what it owns and never has to know about the one above.

**That cascade is the whole argument for hibernation.** `EvictIdle` always knew
how to drop the 86% row and was *forbidden* to while an agent was live, because
one `cachedLog` is shared between the writing agent and concurrent readers.
Hibernation is not a separate memory feature; it is the unlock.

## The endpoint outlives the agent: `ariaHub`

The inversion everything else rests on. The **angelus** owns the listener at
`figaros/<id>.sock` and the set of client connections; the agent is a producer
the hub binds. See `angelus/hub.go`.

- Creating a hub is a listener and a map. It constructs **no agent**. That is
  required, not incidental: a unix socket has no lazy activation, so the
  endpoint must be listening before a client dials, and only the daemon can
  promise that.
- The hub is the agent's **only** subscriber, so the agent's subscriber set is
  always ≤1 and lifetime-independent of any client. `Agent.OnTeardown` carries
  the unbind, because the endpoint must survive the agent and therefore cannot
  be owned by it.
- Subscription is per-(conn, aria), which today buys nothing: one conn is one
  aria. It means multiplexing later (one pooled connection, many arias, target
  id in the envelope) is a change of *listener count*, not of architecture.

Requests route on liveness. `rpc.MethodNeedsAgent` classifies the surface:
`figaro.read`, `figaro.context` and `figaro.form` are pure functions of
the store and are served from it while the aria is dormant; everything else
wakes. **Default is `true`**, a method added later and left unclassified wakes
rather than being answered from stale bytes, because a needless restore costs
milliseconds and a stale mutation costs correctness.

## Hibernation

`Registry.Hibernate` is `Kill` minus the deletion. What survives is the feature:

- **pid bindings**, so a bound shell's next bare prompt lands on the same trunk
  rather than minting a new aria;
- **the hub**, so attached terminals stay connected;
- **background sessions**, which live on the daemon (`Angelus.Sessions`), keyed
  by aria id as scope. `figaro kill` reaps them via `KillScope`; hibernation
  deliberately does not. Deletion takes its children; dormancy does not.

The sweep's predicate is only what it needs to be:

```
state == "idle" && now - LastActive > dormant_after
```

Notably absent: bound pids, attached clients, running sessions. Each would have
made hibernation impossible for exactly the arias that cost most, a terminal
left open all afternoon is the common case, and each is gone because what it
protected moved elsewhere. Keyed on `LastActive`, so an aria woken a moment ago
is not reclaimed on the next tick; restore is O(history) and that would be a
flap costing more than the memory it saves.

**There is no fourth state.** A reclaimed aria *is* `dormant`; the vocabulary
stays `dormant | idle | active`. "Was it reclaimed or never loaded" is a
*reason*, not a state.

### The race that matters

A prompt can arrive between the sweep's decision and the teardown. `Hibernate`
holds a `retiring[id]` flag mirroring `killing`, re-checks `TurnActive()`
immediately before teardown, and keeps the id published until teardown
completes so a concurrent restore cannot build a second agent against a
still-sealing log. **Losing that race costs a skipped reclamation, never a
dropped prompt.**

## The IR window

`cachedLog` keeps a *tail window* and falls through to the inner log below it,
with identical semantics either way. See `store/cached_log.go`.

**A window, not an LRU, deliberately.** The access pattern is: append, translate
the tail past a watermark, render recent turns, occasionally page backward on a
scroll. That is a window, a slice header and a counter: where an LRU would be
a map, a list, per-entry bookkeeping and a second lock for the same reclamation.
Backward paging is served from the store by the angelus reader without touching
the window.

Two findings that shaped it, both from benchmarks:

- **Trimming on every append cost 4.4 µs and 51 KB** against 308 ns and zero
  allocations unwindowed, a 14x regression on the daemon's hottest write path,
  since each compaction copies the window. Compaction is batched behind a slack
  allowance (362 ns, zero extra allocations). Construction still compacts
  *exactly*, since that is the moment the whole log was just materialized.
- **Row count is the wrong bound.** Dropping 80% of *rows* released 26% of
  *bytes*: a long agentic conversation puts short prose at the head and large
  tool results at the tail. So `ir_window_mb` is the knob to reach for, and
  `ir_window` is a row-count safety.

Entries are sized from `Entry.EncodedBytes * irDecodeInflation`. Sizing from the
decoded struct was tried and abandoned: guessing allocator rounding for every
string, slice and boxed map value came out **3x low**. The encoded size is known
for free at decode, and the ratio is stable enough (4.0x and 5.3x on two real
arias) that one constant beats a model.

### Reading only the tail

`xwalLog.TailBudgeted` reads **backward** from the channel tail and decodes only
what it keeps, because `xwal.ReadAt` is random access by LT and a record's
encoded size is known *before* it is decoded. Opening a 2000-message aria:

| | time | bytes | allocs |
|---|---|---|---|
| unbounded | 23.0 ms | 7.03 MB | 54,109 |
| `budget=256KiB` | 5.0 ms | 1.35 MB | 11,827 |

Beware one number when profiling this: **figwal's `OpenNode` costs ~27.5 MiB of
transient allocation the first time an aria is opened**: loading the lineage
head, and ~0 on every open after. It dominates a wake and is figwal-side, not
ours. Cold reads through a trimmed window are cheap once that head is warm.

## Who controls the caches

The reaper says *when*; the cache decides *how*. `Backend.TrimResident(live,
keep)` is reached through an optional interface, the same pattern `idleEvictor`
already uses, and skips live arias for the same reason.

The window **also self-trims on append**, because a log growing through a long
autonomous turn has to be bounded now rather than at the next sweep, and that
needs no goroutine, which keeps the daemon on **one sweep clock**. A second
timer inside `internal/store` would be a second scheduler with no shared view of
liveness.

The cache is otherwise invisible: `cachedLog` satisfies `Log[T]`, so consumers
cannot tell whether a row is resident. `store.Snapshot` was deleted for exactly
this reason: it returned the cache's own backing slice when the log happened to
be materialized, which made "all of it is in RAM" free at the call site and
therefore load-bearing everywhere. Consumers that want a suffix call
`store.TailAfter`; one that genuinely needs every entry calls `Read` and pays
for the copy.

## Configuration: `[memory]` in `config.toml`

Daemon policy, not conversation state, so it lives in `config.toml` and never in
an outfit or the form.

```toml
[memory]
dormant_after_minutes  = 15   # 0 disables reclamation entirely
sweep_interval_seconds = 120  # floored at 1s
max_live_arias         = 0    # 0 = unbounded; SOFT cap
ir_window_mb           = 4    # 0 = unbounded; the knob that matters
ir_window              = 0    # row-count safety, floored at 64
soft_limit_mb          = 2048 # the daemon's heap ceiling; 0 = none
actor_linger_ms        = 2000 # how long a form's writer waits before leaving
handle_idle_minutes    = 0    # figwal head unload; 0 = figwal's default (5m)
form_patch_window      = 2048 # resident decoded patches per form; 0 = all
segment_cache_mb       = 32   # raw figwal payloads, whole process; 0 = none
```

**`ir_window_mb` defaults to 4 now**, not unbounded. The decoded IR is 63 to
86 percent of a real aria's footprint, so leaving the one bound that governs
it switched off meant every aria anyone touched kept all of it.

**AND THE DEFAULT IS THE STORE'S, NOT THE CONFIG'S** (2026-08-19). It used to
live in `internal/config` and reach the store through three calls in the
daemon's boot path, which meant a `store.NewXwalBackend` built anywhere else —
`figaro doctor`, every test, any future embedding — was UNBOUNDED. The store
now bounds itself at construction (`store.DefaultIRBudgetBytes`,
`store.DefaultTranslationBudgetBytes`) and these keys TUNE it: a key you do
not set is a key that leaves the store's own bound alone, and an explicit `0`
is still a real answer meaning unbounded. Measured on a 400-message aria of
8 KiB messages: 16.4 MB resident unbounded against 5.7 MB bounded.

**`soft_limit_mb` is a ceiling AND a licence.** Go collects harder only as it
approaches, so a high one leaves the runtime no reason to hand memory back:
a live daemon measured 115 MB allocated against 416 MB of `heap_sys` under
the 2 GiB default. Lowering it is the one-knob answer to a daemon holding
too much, paid for in GC cycles. `GOMEMLIMIT` in the environment always wins.

**`form_patch_window` is the store's last unbounded retention, closed.** A
form kept every patch it ever took, decoded, for the life of the process.
Below the window `PatchesBetween` re-reads from the log, which costs a walk
and happens only on a cold retranslate; the hot path is still a zero-copy
view. Trimming COPIES the tail rather than re-slicing, because the published
array is shared by construction and a header into the middle pins all of it,
and it copies only across a slack allowance, because copying per write is
the O(history) cost the view exists to avoid.

**`segment_cache_mb` is the bound the other three sit on.** `ir_window_mb`,
`translation_window_mb` and `form_patch_window` all cap DECODED copies of
bytes figwal was holding raw and without any limit: opening a channel used to
copy its whole history into memory, so one `fig ls` on a 515-aria store
retained 116 MiB with no aria loaded. figwal now loads a segment's payloads
on the read that lands in one and drops them, least recently used first, at
this budget. The same listing retains 48 MiB at the 32 MiB default and 17.6
MiB at 4 MiB, so residency is a dial. A dropped block costs only the next
read of it, because the file has every byte; `doctor mem` prints
`segment-cache=X of Y` beside `loaded-heads`, and **loaded-heads stopped
being a proxy for memory** -- a head now costs its index, not its history.

**Three clocks, one policy.** `dormant_after_minutes` evicts agents and their
caches, `handle_idle_minutes` unloads figwal's in-RAM heads, and
`actor_linger_ms` is how long a form's writer goroutine survives a drained
queue. They enforce the same intent at three layers and should be set
together or not at all. The last two must be set BEFORE the store and the
forms open, which is why they are package-level and armed in `runAngelus`
rather than read per call.

`max_live_arias` is soft by design: an aria mid-turn counts toward it and is
skipped, because hitting a number is never worth killing a turn. It logs once
per situation when it cannot be met, and exempts anything woken in the last
minute.

## Seeing it

`figaro doctor mem [-j]` reports live arias, resident handles, resident IR rows
and bytes, figwal's loaded heads and segment cache, open endpoints, attached clients, sessions, goroutines and the heap.
It names out loud the state in which idle eviction can free nothing (every
resident aria having a live agent).

`net/http/pprof` serves on `$FIGARO_RUNTIME_DIR/pprof.sock` when `FIGARO_PPROF`
is set: 0600, same trust boundary as `angelus.sock`. Off by default: pprof's
handlers stop the world and leak argv, and this daemon is long-lived.

```sh
FIGARO_PPROF=1 figaro …
go tool pprof -http=: 'http+unix://$XDG_RUNTIME_DIR/figaro/pprof.sock/debug/pprof/heap'
```

`scripts/hibernate-demo.sh` is the same thing with your own eyes: N seeded
arias, a `figaro listen` pane on each, an aggressively short dormancy window,
and the counters as the sweep runs. What to look for is two numbers at once -
`live=0` with `attached-clients=N`.

## Known gaps

- **Side-channel suffix seeks are O(log N), not O(1).** The main channel is
  identity (main-LT == channel-LT), so `ReadFrom` seeks directly; translations
  and the form binary-search. figwal already builds a main-LT index for
  `Lookup`, and exposing a "first channel-LT at or after this main-LT" query
  would remove the search. `TODO(perf)` in `store/xwal_log.go`.
- **The form patch list is not windowed.** It holds the board plus every
  patch. Measured at ~45 KiB, so this is design tidiness, not a memory win.
- **The first `fig ls` on a 515-aria store takes 2.7 s and nobody knows why
  yet.** It is NOT the segment scan: figwal now opens only the newest segment
  of a node (the rest on the read that lands in them), and the wall time did
  not move. Measured in-process, the whole listing is ~390 ms (topology and
  labels 302 ms, recency 84 ms), so most of that 2.7 s is somewhere between
  the CLI and the daemon's list handler -- meta sidecars, JSON, or the boot
  it races. Profile the daemon-side handler before believing any story about
  it, including this one.
- **Memory-pressure triggering does not exist.** The time rule and the cap are
  the only policies. Add one only if measurement demands it.
