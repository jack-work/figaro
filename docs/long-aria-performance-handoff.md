# Long-aria performance handoff

## Repository state

- Figaro main is untouched at `10dd80d`.
- Integration worktree:
  `C:\Users\jokellih\dev\figaro-worktrees\integration`
- Integration branch: `perf/long-aria`
- Figwal main is untouched at `22223ab` / `v0.7.7`.
- Figwal experiment worktree:
  `C:\Users\jokellih\dev\figaro-worktrees\figwal`
- Figwal experiment branch: `perf/long-aria`
- Windows Figaro was never restarted during this work.
- Runtime and end-to-end checks use the NixOS WSL distribution.

The detailed intent and staged architecture are in
`plans/long-aria-performance.md`.

## User-confirmed architecture

1. XWAL should natively keep parallel IR, translation, chalkboard, and future UI
   branches aligned with the aria tree.
2. Forks should reference the same physical parent prefix on disk and the same
   immutable parent snapshot in memory.
3. Translation work must be proportional to untranslated messages, normally
   one or two, except intentional fingerprint/model invalidation.
4. Provider request serialization may be proportional to prompt bytes.
   Translation/cache validation may not add another full-history pass.
5. A running aria must survive a fork. The fork is actor-coordinated and the
   stable continuation resumes without socket/process interruption.
6. Transcript memory must remain bounded and only the viewport/window should
   render.
7. Dead code, compatibility shims, duplicate metadata, and precautionary locks
   should be deleted rather than preserved.

## Baseline evidence

All figures are NixOS WSL at Figaro `10dd80d`.

| Path | 1k | 10k | 50k |
|---|---:|---:|---:|
| `cachedLog.Read` | 0.14 ms / 172 KB | 1.94 ms / 1.69 MB | 5.48 ms / 8.41 MB |
| Live-tail composition | 0.13 ms / 173 KB | 1.82 ms / 1.69 MB | 3.72 ms / 8.41 MB |
| Metric refresh | 0.31 ms / 312 KB | 3.98 ms / 3.06 MB | 12.20 ms / 15.21 MB |
| Warm Responses projection | 0.15 ms / 59 KB | 1.75 ms / 977 KB | 13.77 ms / 6.32 MB |
| Warm transcript frame | 3.65 ms / 1.75 MB | 33.31 ms / 19.27 MB | 175.00 ms / 96.39 MB |
| UI reconstruction | 0.67 ms / 489 KB | 8.92 ms / 6.01 MB | 42.35 ms / 32.16 MB |

Additional baseline:

- Fresh XWAL head open, 600 messages / roughly 1.2 MB:
  14-16 ms, 526 KB, 1,685 allocations.
- Isolated warm `figaro list`:
  - 0 arias: roughly 75 ms
  - 100 arias: roughly 214 ms
  - 300 arias: roughly 624 ms
- Warm `figaro show -n 10` on one 600-message aria: roughly 61 ms.
- 50k recent page:
  94-122 microseconds versus roughly 5 microseconds at 1k.
- SDK typed request reconstruction at 50k:
  751 ms, 296.7 MB, 3.65 million allocations.
- Direct Anthropic reconstruction at 50k:
  192 ms, 52.9 MB.
- Responses final JSON marshal at 50k:
  113 ms, 13.1 MB.

Profiles are stored outside the repository in the session artifacts:

- `transcript-50k.cpu.pprof`
- `transcript-50k.mem.pprof`
- `metrics-50k.cpu.pprof`
- `metrics-50k.mem.pprof`

## Integrated Figaro changes

### Benchmarks and plan

- `ba3c3f9` benchmark long-aria hot paths
- `2591b1b` benchmark UI reconstruction
- `8e594dd` isolated performance forest generator
- `959c50c` systemic refactor plan
- `03fa295` normalize plan line endings

### Incremental provider projections

Integrated commits:

- `f8fc06d` incremental translator catch-up
- `e475a94` O(1) tail fingerprint validation
- `086e581` remove inert derivation sidecars
- `c847c1f` one-lineage provider cache state
- `d84873c` shared `ProjectIncrementally`
- `c9cf6e6` retry encode failures without advancing watermark
- `1b89b9e` fixed-size translator benchmark fixtures

Corrected fixed-prefix/two-message-suffix results at 50k:

| Provider | Encode suffix | Cached suffix | No-change steady |
|---|---:|---:|---:|
| Direct Anthropic | 14.2 us | 6.1 us | 247 ns |
| Anthropic SDK | 95.8 us | 61.0 us | 420 ns |
| Copilot Responses | 13.0 us | 8.1 us | 560 ns |

The translator cache bytes, fingerprints, and Copilot transport namespaces are
unchanged. Cold catch-up is still proportional to history; native XWAL suffix
views are the remaining upstream dependency.

### SDK request assembly

- `18769b3` retain immutable parsed Anthropic request state

At 50k messages:

- typed `buildParams`: about 1.9-2.5 ms, 2.8 MB, 3 allocations;
- final SDK marshal: about 259 ms, 49 MB.

The former is avoidable reconstruction and is fixed. The latter is the
remaining unavoidable/full-prompt serialization path. Byte-equivalence,
cache-control, tags, thinking, tools, coalescing, and immutability tests pass.

### Bounded transcript

Integrated commits:

- `1f90371` bounded history window
- `6afefde` descriptor/payload replay caches and selection rehydration
- `5dbd3d9` remove compatibility paths
- `6ce478e` cancellable historical search
- `7f5cc40` remove superseded benchmark

At 50k messages:

- startup: about 4-6 ms, independent of history;
- warm render: about 0.4-0.5 ms;
- search of retained window: about 0.4-0.5 ms;
- selection navigation: about 0.4 ms;
- resize: about 3.7-4.0 ms;
- live update: about 0.5-1.3 ms;
- descriptor fallback: about 35 microseconds and 15 probes;
- full 50k range copy: about 52 ms and 13 MB, proportional to copied text.

Retention is three active pages, three payload-LRU pages, 64 descriptors, and a
60-message live tail. Search and selection copy page asynchronously with
cancellation/generation guards. No wire or disk format changed.

### Server/actor branch being merged

Source branch: `perf/long-aria-server`

Commits:

- `e45ea28` paged reads, incremental metrics, cached state
- `6886345` XWAL cache invariant documentation
- `b59b64e` lineage-local write requirement
- `c902b6c` metadata-only dormant listing
- `a65f6f2` actor-coordinated live fork
- `cece55a` remove write-only live-state lock
- `c0ebaf9` actor-snapshot metadata publication
- `01b627f` remove dead derivations
- `c9c9fb7` metadata path benchmarks
- `fdff4f5` delete inert server/storage paths
- `2f0fc78` live-frame IO benchmark
- `57bed43` remove legacy aria backup probe

Measured server improvements:

- 10k paged read: 1.249 ms / 1.71 MB to 74.6 us / 44.6 KB
- 10k metric refresh: 949.5 us to 183 ns
- metadata publication: 1.010 ms / 1.37 MB to 211 ns / 384 B
- cached count: 1.065 ms to 102 ns
- live frame after dead `_live` removal: 418 us to 5.8 us, no disk IO
- 300-aria list handler: 7.38 ms to 0.84 ms

Live forks no longer kill or restart the actor. Provider and tool-stream tests
cover FIFO fork coordination and stable continuation identity. External RPC and
disk formats are unchanged.

## Dead code removed by the server branch

- inert durable derivation registry and workers;
- unread translation metadata sidecars and backend methods;
- dead `Backend.List` / `AriaInfo` path;
- unread `_live` blob persistence and every per-frame write/rename;
- write-only `liveMu` / `liveActive`;
- no-op `Touch` and unused node timestamps;
- unused `ariaHandle.id`, `XwalBackend.Store`, `kindOf`, and translation helper;
- legacy inbox aliases and tests-only actor APIs;
- obsolete pre-XWAL backup and pre-stumps migration shims;
- unused log operations and sustaining tests.

The legacy migration removal is intentional under the repository's forward-only
development rule. Old pre-stump stores no longer auto-migrate.

## Figwal experiment: preserve, do not integrate yet

Committed on Figwal `perf/long-aria`:

- `d7dbb3b` accelerate long XWAL histories
- `582cd42` keep XWAL heads hot per lineage
- `2e36f5f` preserve snapshots across fork appends
- `f6b044e` remove obsolete XWAL compatibility code
- `45dbdc1` retain exported typed-log API

The branch also has unfinished uncommitted work:

- modified:
  - `log/log.go`
  - `xwal/bench_test.go`
  - `xwal/fork.go`
  - `xwal/trunks.go`
  - `xwal/xwal.go`
- untracked:
  - `log/store.go`
  - `log/store_test.go`
  - `xwal/records_test.go`

The committed work improves hot heads, open/rescan behavior, FK tails, and
snapshot safety, but XWAL channels still hold `*disk.Log`. They do not yet
expose the immutable shared-prefix channel snapshots and range-after-main-LT
API required to remove Figaro `cachedLog`.

Do not bump Figaro's Figwal dependency from `v0.7.7` yet. The next Figwal pass
should:

1. finish or discard the dirty `log.Store`/channel-snapshot work;
2. make channel snapshots share parent pointers across forks;
3. expose O(1) channel tail plus zero-copy suffix-after-main-LT;
4. provide indexed dynamic-channel lookup and state/patch lookup at main LT;
5. run full, race, fork-crash, and 100k-history benchmarks;
6. commit, tag/release, then update Figaro `go.mod` and the Nix vendor hash;
7. remove Figaro `cachedLog`, backend hot maps/locks, and linear XWAL fallback
   only after that API is released.

Estimated focused completion time: two to four hours.

## Large follow-on actor refactor

Providers currently append canonical IR from their network goroutine. A stricter
actor model should:

1. remove writable `FigLog` from `SendInput`;
2. replace `PushFigaro` with synchronous
   `CommitAssistant(candidate, cacheBytes) -> CommitAck{LT}`;
3. let the actor commit IR, metrics, metadata, and cache linkage atomically;
4. reject late commits by turn generation after interrupt;
5. serialize fork immediately before or after that commit section.

This should wait for a native XWAL multi-channel publication API and dedicated
interrupt/fork/cache-failure tests.

## Validation commands

Current integrated Figaro branch:

```bash
CGO_ENABLED=0 go test ./... -count=1
CGO_ENABLED=0 go vet ./...
nix build --no-link
```

Targeted race suite:

```bash
go test -race ./internal/figaro ./internal/angelus ./internal/store \
  ./internal/provider/... ./internal/cli
```

Isolated list fixture:

```bash
FIGARO_PERF_FIXTURE=/tmp/figaro-perf/state/arias \
FIGARO_PERF_ARIAS=300 \
FIGARO_PERF_MESSAGES=2 \
go test ./internal/store -run TestGeneratePerformanceFixture -count=1
```

Session helper scripts outside the repository:

- `benchmark-list.sh`
- `benchmark-show.sh`

## Remaining wrap-up checklist

1. Finish the current server merge conflict resolution.
2. Run focused tests, then full Nix tests/vet/build.
3. Run targeted race tests in Nix if the toolchain has a C compiler; otherwise
   run non-race concurrency tests and record the limitation.
4. Rerun long-aria benchmarks and isolated 0/100/300 list fixture.
5. Update `ARCHITECTURE.md` / `agents.md` facts without rewriting the intent
   plan.
6. Commit the merge resolution and this handoff.
7. Push only the Figaro feature branch for review. Preserve the dirty Figwal
   worktree and do not merge or tag it.
