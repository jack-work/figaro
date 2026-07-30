# Understump: making `figaro new` cheap again

**Status:** analysis + recommendation. No product code changed on this branch.
**Measured on:** `main` @ `a29725f` (figaro 0.16.1), figwal `v0.8.1`, Windows,
AMD EPYC 7763, NTFS with Defender live scanning.
**Fixture:** the author's real aria tree — 382 arias, 476 nodes in the `ir`
channel, 65 loadout stumps, 9 channels, 385 MB.

---

## 1. The complaint, quantified

`figaro new` takes **1.1 – 7.0 s** on that tree. On an empty tree the same
code path takes **0.16 s**. Nothing about the aria being created changed; the
forest around it did.

Daemon-side phase timings, from `slog` marks inserted into
`(*handlers).create` (patch in §8), four consecutive mints, milliseconds:

| phase | #1 | #2 | #3 | #4 |
|---|---:|---:|---:|---:|
| `reloadConfigIfChanged` | 0 | 0 | 0 | 0 |
| `outfitter.Load` | 39 | 609 | 73 | 105 |
| provider factory | 0 | 6 | 0 | 1 |
| `backend.CreateLoadout` | 1 | 1 | 2 | 4 |
| **`backend.CreateConversation`** | **765** | **5490** | **1325** | **3552** |
| `backend.ApplyChalkboard` | 99 | 93 | 162 | 547 |
| `backend.ChalkboardState` | 2 | 2 | 4 | 1 |
| `NewAgent` + `Registry.Register` | 6 | 4 | 4 | 25 |
| **total in the daemon** | **913** | **6209** | **1574** | **4240** |

CLI wall clock for the same six runs: 1102, 2318, 1228, 6965, 2159, 4979 ms.

**One call is 84–90 % of the cost:** `XwalStore.CreateConversation` →
`xwal.(*Trunks).SpawnUnderStump`. Everything else is rounding error by
comparison. This document is about that one call.

---

## 2. Where the time actually goes

CPU profile of `BenchmarkCreateConversation`, 100 mints, 24.4 s wall,
24.09 s of samples. **`runtime.cgocall` is 93 % of all samples** — this is a
pure Windows-filesystem-syscall workload, not a CPU or allocation problem.

```
SpawnUnderStump                                  94.9%
├─ spawnTrunkAt                                  86.7%
│  ├─ forkChild                                  79.0%
│  │  ├─ openForkSource                          42.3%
│  │  │  └─ forkTopologyStructurallyComplete     38.7%
│  │  │     └─ channelNodeStructurallyComplete   38.0%
│  │  │        ├─ readForkBaseFile (os.ReadFile) 34.0%
│  │  │        └─ os.Stat                        13.8%   (overlaps above)
│  │  └─ forkJoint                               31.8%
│  │     ├─ ForkRehome (per channel)             19.5%
│  │     └─ writeForkMarker                      10.8%
│  └─ rebuild → walk (full forest)               16.4%
└─ beginTopologyMutation (flushHot + rebuild)     8.2%
```

Syscall mix, share of total samples:

| syscall | share | what calls it |
|---|---:|---|
| `CreateFile` (open) | 38.6 % | `readForkBaseFile`, `readTrunkID`, segment opens |
| `FlushFileBuffers` (fsync) | 14.2 % | fork markers, segment durability |
| `GetFileAttributesEx` (stat) | 13.6 % | `channelNodeStructurallyComplete` |
| `CloseHandle` | 7.5 % | every one of the above |
| `ReadDir` | 8.7 % | `Trunks.walk`, `hasSubdirs` |
| `CreateDirectory` | 2.0 % | the 9 new channel dirs |
| `MoveFileEx` | 1.5 % | marker rename |

Reading that table: we are spending the user's second opening, statting and
closing files that describe a *forest*, in order to add *one leaf*.

---

## 3. Three independent scaling terms

### 3.1 Two full-forest walks per spawn — the dominant term

`Trunks.rebuild()` throws away the in-memory index and re-walks every
directory of the main channel, reading a `.trunk` marker in each
(`figwal/xwal/trunks.go:269` and `:314`). A single `SpawnUnderStump` runs it
**twice**:

```go
// trunks.go:802 — beginTopologyMutation
if err := t.flushHot(); err != nil { ... }
if err := t.rebuild(); err != nil { ... }        // walk #1
return func() { end(); t.mu.Unlock() }, nil

// trunks.go:1616 — spawnTrunkAt
func (t *Trunks) spawnTrunkAt(parentBranch []string) (TrunkID, error) {
    childDir := t.mintNode()
    childTrunk := t.mintTrunk()
    if _, err := t.forkChild(...); err != nil { ... }
    return childTrunk, t.rebuild()                // walk #2
}
```

Measured cost of exactly one walk on the real tree — `Trunks.Refresh()`,
which is `beginTopologyMutation` + `rebuild`, five consecutive runs:

```
329ms   373ms   416ms   352ms   350ms
```

Two of those is ~700 ms, which is precisely the observed floor of
`backend.CreateConversation` (765 ms in the best of four runs above). This
term alone explains the complaint.

Isolated scaling — siblings pinned at 0, only forest size varies
(`TestCreateVsForestSize`):

```
forest nodes ~  0- 50: mean create  89ms
forest nodes ~ 50-100: mean create 110ms
forest nodes ~100-150: mean create 149ms
forest nodes ~150-200: mean create 175ms
forest nodes ~200-250: mean create 299ms
forest nodes ~250-300: mean create 247ms
```

Linear in forest size, roughly 0.9 ms per node per mint on this hardware.

### 3.2 A preflight rescan that the cache does not actually skip

`openForkSource` (`figwal/xwal/trunks.go:1012`):

```go
func (t *Trunks) openForkSource(branch []string) (*XWAL, error) {
    t.retireRootHotPreservingValidation()
    validated := t.forkPreflightValidated(branch)
    if validated {
        complete, err := forkTopologyStructurallyComplete(t.root, t.cfg, branch)
        if err != nil { return nil, err }
        validated = complete
    }
    if !validated {
        // ... repairBranchChannels / repairRehomeDescendants
    }
    return Open(t.root, t.cfg, branch...)
}
```

The `validated` cache gates only the *repair* pass. The structural scan runs
on **every** spawn, cache hit or miss. And that scan is not cheap
(`figwal/xwal/xwal.go:656`):

```go
for _, mc := range man.Channels {                       // 9 channels
    channelNodeStructurallyComplete(<parent dir>, ...)  // Stat + ReadFile(.fork)
}
entries, _ := os.ReadDir(mainDir)                       // every sibling
for _, entry := range entries {
    for i, mc := range man.Channels {                   // x 9 channels again
        channelNodeStructurallyComplete(<sibling dir>, ...)
    }
}
```

`(1 + siblings) × 9 × ~3` syscalls, per mint. Measured
(`TestCreateVsSiblings`, one stump, fan-out growing):

```
siblings   0- 10: mean 334ms
siblings  10- 25: mean 348ms
siblings  25- 50: mean 393ms
siblings  50-100: mean 374ms
siblings 100-150: mean 630ms
siblings 150-200: mean 1590ms
```

Today the busiest stump on the real tree has 26 children, so this is the
*second* term, not the first. But it is superlinear in the one dimension a
popular loadout grows along, and it is the term that turns a 1 s annoyance
into a 5 s one as `default@<hash>` accumulates conversations.

### 3.3 Fixed marker/fsync cost

Floor with zero siblings and a near-empty forest (`TestCreateFloor`):
**79–85 ms**. `forkJoint` writes a fork plan, rehomes 9 channel logs, writes
9 fork markers, applies the commit and removes the plan; `FlushFileBuffers`
is 14 % of all samples, ≈ 34 ms per mint. This is the honest price of a
crash-safe N-ary fork across 9 channels and is the least interesting number
here — but it is also the floor that terms 3.1 and 3.2 should be collapsing
toward, and are not.

**Model.** `create_ms ≈ 80 + 0.9·(forest nodes) + f(siblings × channels)`,
where the middle term is entirely avoidable and the third is mostly
avoidable.

---

## 4. Two cliffs found while measuring

Neither is a throughput problem; both are correctness-adjacent failures that
a user experiences as "figaro is broken", and both were hit on the first try.

### 4.1 A root-wide topology lock starves aria creation

`SpawnUnderStump` → `beginTopologyMutation` → `ensureNoOpenHeads` /
`waitRootBorrowers`, which block until **every open head across the entire
forest** is released, with a 3 s `topologyWaitTimeout`
(`figwal/xwal/trunks.go:113`). The very first mint attempted during this
investigation:

```
$ figaro new --loadout default -j
error: create figaro: jsonrpc error -32000: create conversation:
  xwal store: spawn conversation:
  xwal: topology mutation timed out waiting for 1 open head(s)
real    0m4.150s
```

One active aria — someone mid-turn — can make new-aria creation fail
outright. The blast radius is wrong: minting under stump A cannot interact
with an open head on trunk B. The lock is scoped to the registry root
because `rebuild()` is scoped to the registry root; fix §5.1 and this
scope becomes defensible to narrow.

### 4.2 Cold daemon start reliably beats the CLI's bootstrap deadline

- `xwal.OpenStore` on the real tree: **8.1 s** (measured, `TestForestWalkCost`).
- Observed time for a cold daemon to bind its socket: **12 s**.
- `ensureAngelus` waits **5 s** before declaring failure
  (`internal/cli/angelus_client.go`).

So the first `figaro` command after any daemon death fails with
`angelus did not start within 5 seconds`, the user retries, and the second
command works because the daemon finished starting in the meantime. This is
a guaranteed papercut that scales with the same forest size as everything
else, and it will get worse.

---

## 5. Recommendations

Ordered by (measured win) / (risk × effort). 1 and 2 are in figwal;
5 is in figaro.

### 5.1 Make the spawn's index update incremental — *the fix*

A spawn adds exactly one leaf under a known parent branch. The resulting
topology delta is fully known before any I/O happens: the child node, its
trunk id, the parent's `frozen` flag flipping true, and one new entry in
`heads`. Patching `t.nodes` / `t.heads` / `t.nodeSeq` / `t.trunkSeq` in place
and bumping `t.version` is O(1) and needs **zero** filesystem calls.

Removes the 0.9 ms/node term entirely: ~700 ms → ~0 on the current tree, and
the curve in §3.1 goes flat. Same argument applies to `ForkTail`,
`ForkAt`, `CreateStump` and `Remove`, each of which knows its own delta.

Keep `rebuild()` as the escape hatch — `Refresh()`, recovery after a fork
plan is found armed at open, and any path where an external writer may have
moved markers. It is the correct *repair*; it is the wrong *steady state*.

### 5.2 Delete the duplicate rebuild — *the free half of 5.1*

Even without 5.1, `spawnTrunkAt`'s trailing `t.rebuild()` re-walks the forest
that `beginTopologyMutation` walked moments earlier inside the same critical
section. The only thing that changed between them is the leaf this function
just created. Halving 700 ms to 350 ms is a one-line change and lands today;
5.1 subsumes it.

### 5.3 Honour the preflight cache

Make `openForkSource` skip `forkTopologyStructurallyComplete` when
`forkPreflightValidated(branch)` is true and `Version()` has not moved, which
is exactly the condition the cache already computes and then ignores. If the
scan is retained as a paranoia check, gate it behind a debug build or an
opt-in `StoreOptions` flag rather than paying `(1+siblings) × 9 × 3` syscalls
on the hot path.

Secondary, if the scan must stay: it re-derives `.fork` bases that
`rebuild()` could have cached during its walk, and it re-reads them for every
sibling on every mint even though only the parent and the new child can have
changed. Scanning the parent alone is sufficient for the invariant it claims
to protect.

### 5.4 Scope the topology lock to the lineage

`ensureNoOpenHeads` / `waitRootBorrowers` currently quiesce the whole
registry root. `lockLineage` / `holdLineageHead` already demonstrate the
finer-grained pattern in the same file. Once 5.1 lands, a spawn no longer
reads the whole forest, so waiting on the whole forest is no longer
justified — narrow it to the affected lineage, and §4.1's hard failure
disappears rather than merely becoming rarer.

### 5.5 Make the angelus bootstrap deadline survive a large store

Raise the 5 s deadline in `ensureAngelus` and, better, have the daemon bind
its socket *before* opening the backend (answering `status` while reporting
"opening store"), so the CLI can wait on a live process with a progress
signal instead of a fixed timer. Today the CLI cannot distinguish "slow cold
open" from "dead daemon"; it guesses, and on a large tree it guesses wrong
every time.

### 5.6 Not worth doing yet

`outfitter.Load` at 39–105 ms per mint (re-reads the loadout and its skill
files on every create) is real and cacheable by loadout content hash, but it
is 5 % of the problem. `backend.ApplyChalkboard` at 93–547 ms is the boot
patch hitting the reducible chalkboard channel; revisit after 5.1, because
part of its cost is the same forest-wide contention. Neither should be
touched before 5.1–5.3, or the win will be invisible under the noise.

---

## 6. Expected result

| term | today | after 5.1–5.3 |
|---|---:|---:|
| forest walk (×2) | ~700 ms | ~0 |
| sibling rescan | ~50–300 ms (26 children) | ~0 |
| fork plan + 9 markers + fsyncs | ~80 ms | ~80 ms |
| `outfitter.Load` + chalkboard | ~150 ms | ~150 ms |
| **daemon-side total** | **~900 ms – 5.5 s** | **~230 ms** |

And, more importantly, flat: the cost of minting aria number 400 stops being
a function of arias 1 through 399.

---

## 7. Reproducing

Probes live in `internal/store/mintperf_test.go` (added on this branch,
test-only, no product code touched):

```bash
go test ./internal/store/ -run TestCreateFloor        -v -count=1
go test ./internal/store/ -run TestCreateVsSiblings   -v -count=1
go test ./internal/store/ -run TestCreateVsForestSize -v -count=1

# Against a real forest. COPY the tree first — never point a second
# daemon or test binary at a live arias directory.
robocopy "$env:USERPROFILE\.local\state\figaro\arias" C:\tmp\arias-copy /E /MT:16
FIGARO_PERF_ROOT=C:/tmp/arias-copy go test ./internal/store/ -run TestForestWalkCost -v -count=1

# The profile in §2
go test ./internal/store/ -bench BenchmarkCreateConversation -benchtime 100x \
  -count=1 -cpuprofile /tmp/create.cpu -o /tmp/store.test
go tool pprof -top -cum -nodecount=45 /tmp/store.test /tmp/create.cpu
go tool pprof -peek 'syscall.Syscall' /tmp/store.test /tmp/create.cpu
```

End-to-end, against an isolated daemon over a copied tree:

```bash
export FIGARO_STATE_DIR=/c/tmp/figperf/state FIGARO_RUNTIME_DIR=/c/tmp/figperf/run
# ...copy arias into $FIGARO_STATE_DIR/arias, then wait for run/angelus.sock
# to appear (12 s on this fixture) before timing any mint.
for i in 1 2 3; do
  s=$(date +%s%N); figaro new --loadout default -j; e=$(date +%s%N)
  echo "$(( (e-s)/1000000 ))ms"
done
```

---

## 8. Daemon instrumentation used for §1

Not committed — the phase marks are noise in a shipping daemon. Applied to
`internal/angelus/protocol.go`, `(*handlers).create`:

```go
perfT0 := time.Now()
perfLast := perfT0
perfMark := func(phase string) {
    now := time.Now()
    slog.Info("PERF create phase", "phase", phase,
        "ms", now.Sub(perfLast).Milliseconds(),
        "total_ms", now.Sub(perfT0).Milliseconds())
    perfLast = now
}
defer func() { perfMark("RETURN") }()
```

with `perfMark("<name>")` after `reloadConfigIfChanged`, `outfitter.Load`,
the provider factory, `backend.CreateLoadout`, `backend.CreateConversation`,
`backend.ApplyChalkboard`, `backend.ChalkboardState`, `figaro.NewAgent` and
`Registry.Register`. Read the results back with:

```bash
jq -r 'select(.Body.Value=="PERF create phase")
       | ([.Attributes[]|select(.Key=="phase").Value.Value]
        + [.Attributes[]|select(.Key=="ms").Value.Value]
        + [.Attributes[]|select(.Key=="total_ms").Value.Value])|@tsv' \
  "$FIGARO_STATE_DIR/logs.jsonl"
```

---

## 9. Cited source locations

| what | where |
|---|---|
| `beginTopologyMutation` (walk #1) | `figwal/xwal/trunks.go:802` |
| `spawnTrunkAt` (walk #2) | `figwal/xwal/trunks.go:1616` |
| `rebuild` / `walk` | `figwal/xwal/trunks.go:269`, `:314` |
| `openForkSource` (ignored cache) | `figwal/xwal/trunks.go:1012` |
| `forkTopologyStructurallyComplete` | `figwal/xwal/xwal.go:656` |
| `channelNodeStructurallyComplete` | `figwal/xwal/xwal.go:704` |
| `ensureNoOpenHeads` / `waitRootBorrowers` | `figwal/xwal/trunks.go:468`, `:741` |
| `topologyWaitTimeout = 3s` | `figwal/xwal/trunks.go:113` |
| `SpawnUnderStump` | `figwal/xwal/trunks.go:1583` |
| `CreateConversation` | `internal/store/xwal_store.go:252` |
| `(*handlers).create` | `internal/angelus/protocol.go:215` |
| `ensureAngelus` 5 s deadline | `internal/cli/angelus_client.go` |
