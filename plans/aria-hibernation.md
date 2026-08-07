# Aria hibernation — returning a live agent to dormant

Research memo, second pass. The first pass proposed gating hibernation on
"no bound pids, no subscribers, no running sessions". That is wrong: it makes
hibernation impossible for exactly the arias that cost the most. This pass
removes all three gates by moving what they protect out from under the agent.

The target invariant:

> **An aria's liveness is a memory decision, invisible to everyone attached to
> it.** Attending does not wake. Listening does not wake. Going dormant does not
> disconnect a client, kill a background process, or lose a frame. Waking
> resumes the stream mid-flight.

## What already exists (do not rebuild it)

- **The vocabulary is already three-valued**: `dormant | idle | active`.
  `dormant` is already documented as "not loaded in memory; nothing is running
  and the aria costs nothing to keep". We need the edge back to it, not a new
  word.
- **Lazy restore is the daily path.** `restoreByID`/`restoreOne`
  (`protocol.go:1223`), serialized per aria by `restoreLocks`.
- **Restore is lossless.** `refreshMetricsFrom` (`agent.go:406`) recomputes every
  counter from the IR; identity comes off the chalkboard channel;
  created/lastActive off the meta sidecar. Nothing in `figaro status` changes
  across a round trip.
- **`figaro list` does not wake anything** — dormant rows come from the
  `AriaMeta` sidecar. Only `bind` and `attach` restore today.
- **Cache eviction below the agent shipped** in v0.19.0 (`a0ccced`):
  `XwalBackend.EvictIdle(live, 15m)` drops the cached IR, translations, board
  and metadata of arias with **no live agent** — and refuses to touch a live
  one, because one `cachedLog` per (aria, channel) is shared between the writing
  agent and concurrent readers.

That refusal is the whole argument. A live-but-idle agent pins the single
largest allocation in the daemon and nothing can reclaim it. Agent hibernation
is not a separate memory feature; it is the unlock that lets the eviction we
already have reach the arias that cost.

## Per-agent memory

| Holder | Cost | Freed by |
|---|---|---|
| `backend.open[id].ir` — decoded IR | 3–5x on-disk bytes | `EvictIdle`, only once no agent is live |
| `backend.open[id].trans[p]` — wire cache | ~O(prompt bytes) | same |
| `ariaSrv` — **whole UI IR**, eagerly composed in `NewAgent` (`agent.go:233`) | 6 MB @10k msgs, 32 MB @50k | agent teardown only |
| `backend.chalk[id]` — board + every patch | board × versions | `EvictIdle` |
| goroutines: drain loop, listener, per-conn servers | small, N per aria | agent teardown |

The `ariaSrv` row is built at construction whether or not anyone reads it, and
`EvictIdle` cannot see it. Only dropping the agent frees it.

---

## 1. Background processes: what actually happens today

Asked for elaboration, so precisely. Three facts:

- `ExecSession.supervise` is **deliberately context-free** — "only the timeout
  (or an explicit Kill) ends the process early; a tool-call abort never does"
  (`session.go:79`). Processes come from `exec.Command` with `Setpgid`, so they
  are children of the *daemon*, not of any turn.
- **`Agent.Kill` never touches `a.tools`.** It cancels the agent context, waits
  for the drain loop, nils the subscribers, closes the chalkboard. That is all
  (`agent.go:697`).
- The `SessionRegistry` is **constructed per agent** inside
  `DefaultRegistryForAria` (`registry.go:143`), and `restoreOne` builds a fresh
  one on every restore.

So on kill (and, as proposed, on hibernate) a running background job:

1. keeps running, as an unreferenced child of the daemon, until it exits or the
   daemon does;
2. keeps its `proc.Wait()` and supervisor goroutines alive, so the
   `ExecSession` and its buffers are not freed either — no memory is reclaimed,
   only reachability is lost;
3. becomes **unaddressable**: the new registry starts `seq` at zero, so after a
   wake the aria's `process list` is empty and a fresh job is minted as `bg-1`
   — the same id the orphan still answers to inside a dead map.

This is a live bug in `figaro kill` today, not something hibernation would
introduce. Hibernation would just make it routine.

**The fix is smaller than the bug.** `SessionRegistry` is *already scope-keyed*:
every method takes a `scope`, sessions carry `Scope`, and
`DefaultRegistryForAria` already passes the **aria id** as that scope. The
registry is per-agent purely by accident of construction. Hoist one instance to
the daemon, hand it to `DefaultRegistryForAria`, and:

- background processes survive dormancy untouched, still addressable after a
  wake, with stable ids;
- `seq` is daemon-global, so ids never collide across a restore;
- session buffers are reclaimed by the existing TTL reaper instead of leaking;
- running sessions stop being a hibernation criterion entirely — a job that a
  turn is *waiting on* keeps the turn active, which already blocks hibernation,
  and a genuinely backgrounded job has no reason to hold an agent in memory.

Open question worth deciding at the same time: whether `figaro kill` should reap
its aria's sessions. I think yes — kill is a deletion, dormancy is not.

## 2. Subscribers: the aria endpoint must outlive the agent

Today the **agent** owns its socket: `restoreOne` does `go agent.StartSocket(ctx)`,
the listener lives on the agent context, and each accepted connection is
`Subscribe`d to the agent (`protocol.go:52`). Kill the agent and every client
connection dies with it. A subscriber count would only let us *avoid* the
problem; it would also mean any open transcript pins an aria forever, which is
the common case, not the rare one.

So invert the ownership.

**`ariaHub`, angelus-side, one per aria, lifetime independent of the agent.**

- Owns the listener at `figaros/<id>.sock` and the set of client connections.
- Created on demand — the first dial, or the first restore. Creating a hub is a
  listener and a map; it does **not** build an agent.
- Is the agent's *only* `Notifier`. `Agent.Subscribe` stays exactly as it is;
  the hub registers itself on bind, and the agent's subscriber set is always
  ≤1 and lifetime-independent.
- Fans one `figaro.aria` frame out to every connection.
- Torn down only when it has no connections **and** no agent — so a hub with a
  parked transcript client survives dormancy at the cost of a listener and a
  socket buffer, which is nothing.

Hibernate becomes `hub.Unbind()` + agent teardown. Wake becomes `hub.Bind(agent)`.
Client connections never notice either, and on wake frames resume to everyone
attached, immediately, because the fanout object never changed.

### Which methods wake, and which do not

Ten methods on the aria socket (`server.go:20`). The hub routes them:

| Method | Dormant behaviour |
|---|---|
| `figaro.qua` | **wake**, then submit |
| `figaro.interrupt` | no-op success (nothing is running) |
| `figaro.queued` / `queueUpdate` / `queueDelete` | empty / not-found; a dormant aria's inbox is empty by construction |
| `figaro.chalkboard` | serve from `backend.ChalkboardState` |
| `figaro.read` | serve from the store — **see below** |
| `figaro.context` | serve from the store (`backend.Open` + `unwrapMessages`) |
| `figaro.set` / `figaro.loadout` | **wake** in phase 1 |

`figaro.set` on a dormant aria could append a chalkboard patch through the
backend without waking — the one-writer invariant is satisfied trivially when
there is no writer. That is a genuinely nice property (a `figaro set` from a
script would stop resurrecting arias) but it needs care against the wake race,
so it is deliberately phase 2.

`figaro.read` is the one that matters, because it is what a transcript pager
calls to page history. It can be served without an agent **today**: `hydrate`
already proves the projection composes from the log alone
(`a.ariaSrv.AdoptIfEmpty(a.projTurns(a.Context()))`, `read.go:22`), and
`uiir.New(r *tool.Registry)` **ignores its registry** — the projector is pure
over messages (`uiir/…:30`). So a store-backed reader needs the log and nothing
else. Cost is one compose (~4.2 ms at 800 turns), uncached, exactly as the live
path pays it on first read. If repeated paging of a dormant aria proves hot, the
hub can hold a composed `aria.Server` under the same idle-eviction discipline as
every other cache.

## 3. Attending must not wake

Three call sites conspire to make attendance wake an aria:

- **`bind` restores first** (`protocol.go:1067`) — drop the restore. The guard
  it provides (does this aria exist?) should become a store lookup, since
  `Registry.Bind` currently requires `figaros[id]` to be populated. Bind is a
  map write about a *shell*; it has no business constructing an agent.
- **`resolve` returns `Found:false` when the agent is absent**
  (`protocol.go:1104`) — the sharp edge, because the CLI reads that as "nothing
  bound" and a bare `figaro -- …` would mint a **new** aria instead of
  continuing the attended one. It must answer from `pidToFigaro` alone and hand
  back the **hub** endpoint, which exists (or is created) without an agent.
- **`attach` restores** (`protocol.go:1086`) — same treatment: hand back the hub
  endpoint. The wake is then driven by what the client actually *does* on that
  endpoint, which is the whole point of routing methods through the hub.

Which answers the last question directly: **yes**, `RestoreBindings`
(`bindings.go:68`) today builds an agent for every surviving bound pid at daemon
boot — the exact cost hibernation exists to avoid, paid up front, before anyone
has asked for anything. Once bind is lazy, its `AriaRestorer` callback becomes
unnecessary: restore the pid map, build nothing. Boot gets faster and colder,
and the first prompt in each shell pays its own tens of milliseconds.

## 4. What is left of the criteria

With sessions daemon-scoped, subscribers hub-held, and attendance lazy, the
eligibility predicate collapses to what it should have been:

```
state == "idle"                       // no turn in flight, inbox empty
&& now - LastActive > hibernate_after // the only real criterion
&& !retiring && !draining
```

Sweep on the existing `pidMonitor` ticker. Suggested defaults: `hibernate_after`
~15–30 min, checked every 60 s. Add an LRU cap (`max_live_arias`) as a second
policy once the mechanism exists — the time rule alone does not bound a daemon
that touches arias faster than they age out. Memory-pressure triggering
(`ReadMemStats` against the 2 GiB `GOMEMLIMIT` already armed in
`cli/angelus.go:79`) only if measurement demands it.

Two-stage reclaim composes for free: hibernate at T, the aria stops being
`live`, and the existing 15-minute cache sweep drops IR/translations/board at
T+15m. Immediate release for the single id is safe once no agent is writing —
existing readers hold their own pointer; only a concurrent *second instance*
against a live writer was ever the danger.

## 5. Remaining hazards

- **The wake race.** A `qua` can arrive between the eligibility check and
  teardown. Hold the per-aria `restoreLock` across the whole hibernate; add a
  `retiring[id]` flag mirroring `killing`; re-check `TurnActive()` immediately
  before teardown and abort if it flipped. A prompt that loses the race must
  wake and re-deliver, never fail.
- **Double-instance.** `Registry.Kill` already keeps the id published until
  teardown completes so a concurrent restore cannot build a second agent
  against a still-sealing log. `Hibernate` must keep that discipline exactly —
  it differs from `Kill` only in preserving pid bindings and the trunk.
- **Thrash.** Restore is O(history): ~15 ms xwal head open at 600 messages,
  ~1.9 ms log decode at 10k, ~8.9 ms UI reconstruction at 10k / 42 ms at 50k.
  Fine once, bad at a 30-second flap. Key the age on `LastActive`, and never
  hibernate an aria restored within the last few minutes.
- **No pprof.** The daemon exposes none, so the 3.0 GB figure in the `EvictIdle`
  comment is an observation, not an attribution. Put `net/http/pprof` on a unix
  socket in the runtime dir behind an env flag, and add live/hibernated/resident
  counts to `angelus.status`, before tuning any constant.

## Corrections to this memo

Written against a reading of the source that was wrong in one place. Kept
visible rather than silently edited, because the same mistake is easy to make
twice.

- **"`DefaultRegistryForAria` already passes the aria id as that scope" — it did
  not.** `ScopeFn` was nil on both the bash and process tools, so every aria's
  sessions were filed under `defaultScope` (`"default"`). Harmless while each
  agent held a private registry; cross-aria leakage the instant one registry
  served everyone. Step 1 therefore had a prerequisite the memo did not name:
  wire the scope before sharing the map. Done in the same commit.

## Progress

**Step 1 — daemon-scoped sessions.** `Angelus.Sessions` is one
`tool.SessionRegistry` for the whole daemon, handed to every aria via the new
`tool.WithSessions`. `DefaultRegistryForAria` now passes the aria id as the
session scope to both tools, which is the isolation boundary. `seq` is
daemon-global, so a wake cannot mint a `bg-1` that an orphan still answers to.
`SessionRegistry.KillScope` reaps an aria's jobs on `figaro kill` — deletion
takes its children, dormancy will not. Callers who pass no registry still get a
private one, so nothing outside the daemon changed.

**Step 2 — measurement.** `angelus.status` carries a `MemStatus` block: live
arias, resident aria handles, session count, goroutines, heap alloc/inuse/sys,
total sys, GC count, the armed `GOMEMLIMIT`, and the pprof socket path when
armed. `figaro doctor mem [-j]` renders it, and says out loud when every
resident aria has a live agent — the state in which idle eviction can free
nothing, which is the whole thesis of this document.

`net/http/pprof` serves on `$FIGARO_RUNTIME_DIR/pprof.sock` when `FIGARO_PPROF`
is set, 0600, same trust boundary as `angelus.sock`. Off by default: pprof's
handlers stop the world and leak argv, and this daemon is long-lived.

```sh
FIGARO_PPROF=1 figaro …            # arm at daemon start
figaro doctor mem                  # the counters
go tool pprof -http=: 'http+unix://$XDG_RUNTIME_DIR/figaro/pprof.sock/debug/pprof/heap'
```

Baseline, fresh daemon, zero arias: heap alloc 4.1 MiB, sys 22.4 MiB, 16
goroutines. The next number worth having is the same reading with a dozen live
arias, which is now one command instead of a guess.

## 6. Build order

Each step is shippable alone and useful alone.

1. **Daemon-scoped `SessionRegistry`.** ✅ *shipped.* Fixes the existing
   `figaro kill` orphan bug on its own merits. Removes sessions from the
   hibernation question.
2. **pprof socket + `angelus.status` counters.** ✅ *shipped.* Baseline heap
   profile.
3. **Store-backed `figaro.read` / `figaro.context` / `figaro.chalkboard`.** A
   reader that needs no agent. Independently valuable: dormant arias become
   readable without a restore.
4. **`ariaHub`** — angelus owns the listener and the fanout; the agent becomes a
   bindable producer. The largest step; everything else waits on it.
5. **Lazy `bind` / `resolve` / `attach`; `RestoreBindings` stops restoring.**
   Attending stops waking.
6. **`Registry.Hibernate` + the idle sweep.** The payoff, and by now nearly
   trivial.
7. **LRU cap.** Memory-pressure trigger only if measured need.

### Tests that gate the merge

- Attend a dormant aria: state stays `dormant`, `Resident()` unchanged, no
  agent constructed.
- `resolve` after hibernate returns the **same** id and a working endpoint; a
  bare prompt in that shell appends to the same trunk — no new aria, no fork.
  This is the regression that would hurt most.
- A `figaro listen` client stays connected across a hibernate, receives no EOF,
  and receives the first frame of the next turn after an unrelated shell wakes
  the aria.
- A transcript pager pages history with the aria dormant, and the aria stays
  dormant.
- A background `bash -b` job survives hibernate/wake with the same session id
  and readable buffered output.
- Race loop under `-race`: prompt vs. hibernate, N iterations, never a second
  agent instance, never a dropped prompt.
- `figaro status -j` byte-identical across a hibernate.
- `Resident()` returns to zero after hibernate + `EvictIdle`.
