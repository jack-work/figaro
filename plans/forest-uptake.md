# Forest uptake — phases 2-5

Working branch `phase/forest-uptake`, worktree `/home/gluck/dev/figaro-qua/forest`.
Successor to the storm-triage plan; S-numbers refer to that file's verdict table.

## Phase 2 — figwal v0.18.0 (DONE, 5f359789)

go.mod bumped, `vendorHash = sha256-4NyISpnIjRb33vIY3bEJut5CvILouY9BkQgfNcmWVRw=`,
nix build green, unit + `-race -count=3` green on store/figaro/angelus/provider.
Dependency only; nothing re-seats on it yet.

## The constraint phase 3 must not break

`cachedLog` publishes an immutable `logView` behind an `atomic.Pointer`.
Readers take no lock. Measured before any change (5261d0da):

    ReadFromParallel-16   516 ns/op   4880 B/op   1 allocs/op
    ReadFromSerial-16     551 ns/op   4864 B/op   1 allocs/op
    Append-16             657 ns/op   1645 B/op   5 allocs/op

Parallel beats serial: the read scales with readers. `forest.Cache.Range`
takes a mutex, so a naive re-seat reinstalls the lock the atomic.Pointer was
installed to remove, and repeals the same law forest itself carries (recency
is an epoch *because* per-read stamping got slower with more readers).

**Rule: the resident tail stays lock-free. Forest backs the frozen prefix only.**

Forest is built for this and says so: `Budget.EpochNow()` is a plain atomic
load, `Cache.Recency` takes the lower layer's own lock-free recency oracle so
eviction order respects reads that never touched forest's mutex, and `Evicted`
fires outside every lock so the layer below can clear its atomic pointer
without inverting.

## Phase 3 — decoded IR onto forest.Cache

### The seam

Every read path already forks on one predicate:

```go
if v.belowWindow(figaroLT) { return c.inner.ReadFrom(figaroLT, n) }   // re-decode
...serve from v.rows                                                  // resident
```

That fall-through is where forest goes. Today a read below the window costs a
full re-decode from xwal, and two forks of one trunk each pay for the shared
prefix separately.

- `trim` stops dropping the prefix into the void and `Put`s it as a run.
- `belowWindow` reads go to `Range(lineage, from, to)`, Source = `c.inner`.
- Forks of one trunk hit the ancestor's runs: N branches, one residency.

### What this DELETES

cachedLog's bespoke accountant goes to forest.Budget: `compact`, `trim`'s byte
arithmetic, `slackNum`/`slackDen`, `windowSlack`, the `window`/`budget`/`sizeOf`
triple, `ResidentBytes`. ~90 lines replaced by a Sizer and a Keyer.

`logView`, the atomic.Pointer, and every read path stay exactly as they are.

### Scan pollution, and the policy it forces

MEASURED, not argued: a whole-history read through `forest.Cache` evicts other
nodes' hot tails -- one scan cost two primed neighbours a rematerialization
each, and five listing-shaped scans did the same. forest is not at fault; a
cache that caches what it reads is correct, and bounding a scan is not
something it promises.

So the consumer sets policy, because only the caller knows its intent:

  - bounded reads NEAR the window route through the cache
  - whole-history and backward-paging reads pass through to the source and
    retain nothing

This is not a new rule. cachedLog already states it for backward paging, in
its own words: *"a scroll must not permanently re-resident a prefix nobody
will read again."* The re-seat EXTENDS that policy rather than routing
everything through `Range`. No figwal change and no new API: the pass-through
is the code that is already there.

`scan_pollution_test.go` asserts the pollution HAPPENS, so if forest ever
bounds scans itself the test fails and the policy can be simplified.

### Open plumbing

`cachedLog` does not know its lineage. `[]forest.Ref{{Node, Base}}` has to come
from the backend, which owns trunks and fork bases. This is phase 3's real work.

### Gate

The three benchmarks above at parity, plus a fork-prefix residency test showing
N branches paying one residency, plus the hazard test below.

### Evicted takes no lock

forest fires `Evicted` outside its own locks so a lower layer can clear a fast
pointer. The inversion is the hazard: a consumer calling `Put` under its write
lock can have eviction pick one of its own runs, so `Evicted` runs with that
lock held. Measured, 20/20 runs: it fires under the held lock every time.

So the hook is a pointer swap and nothing else -- which cachedLog can afford,
because `logView` is already published through an `atomic.Pointer`. Anything
more publishes a successor view instead. Pinned in `evicted_nolock_test.go`;
a deadlock fails as a timeout rather than wedging the suite.

### The held-view hazard, per the role bearer

Hollowing a frozen run must publish a NEW view, never edit the one readers
hold. Editing in place is the study-patch mutation class of bug; a per-LT cache
makes the damage permanent. Test: take a `Read()`, hollow underneath it, require
the held slice to still read correctly.

## Phase 4 — composed UI onto the same type

Second bespoke accountant deleted. Preserve S1's law: pins are COUNTED, never
silently skipped. Add S3's prescribed per-node constant (~400 B) to `turnBytes`,
since `nodeSize` charges marshaled JSON and under-charges node-heavy turns.

**Phases 3 and 4 measure SEPARATELY.** S3 is ACQUITTED as an aliasing
amplifier — re-run on the phase-2 build:

    go run ./scratch/s3probe -msgs 200 -lines 5000
    decoded fig IR          67.41 MiB
    after dropping the IR    2.75 MiB retained by the nodes alone
    amplification: 1.15x

Nodes copy, they do not alias; dropping the decoded prefix frees 96% of it with
composed turns resident. Phase 3's win is visible on its own. ("S3 open" means
the per-node overhead is unfixed, not that the aliasing charge stands.)

The `saved=0.0 MiB` observation that once suggested otherwise is explained and
is not evidence: `TestUIWindowBoundsAResidentAgent`'s harness did a boot
`log.Read()` that left the whole decoded IR resident, so a 2 MiB UI bound was
noise against it, measured with `HeapInuse` — too coarse a ruler for a 2 MiB
delta. A fixture fault on the wrong axis, not a product one.

## Phase 5 — the open turn (S6, the 157.7 MB)

`composeTurn` re-reads `figLog.ReadFrom(turnStart+1, 0)` and recomposes every
node of the turn on every frame: 157.7 MB, 44% of live heap, while `doctor mem`
correctly reported `ui window 9.1 MiB of 16`. The turn cache bounds SEALED
turns; the open one has no bound. The wire is incremental; the producer is not.

Compose only past `lastComposedLT`, mutate the live tail, hand the frozen prefix
below `Live.From` to the cache as an ordinary run.

## Measurement notes

- **Bytes by aria need a METER, not a profiler.** Go's heap profiler ignores
  pprof labels; only CPU and goroutine profiles carry them. Measured:
  a live labelled goroutine allocating 128 MB — `HEAP names the aria: false`,
  `GOROUTINE names the aria: true`. So `perf/pprof-aria-labels` (#19) answers
  "which aria is wedged", never "which aria is fat".
- **RSS tracks sys, not inuse.** 1.86 GB RSS against 855 MB heap inuse and
  2.2 GiB sys under GOMEMLIMIT 2.0 GiB: much of the gap is arena the runtime
  had no reason to return. `soft_limit_mb` is a zero-code lever to MEASURE
  before attributing anything else — and to measure on an isolated storm root,
  not on the live daemon.
