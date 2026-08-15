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

## Phase 3 — VERDICT: the decoded layer does not need forest

Measured by IDENTITY, not by heap (heap is the wrong ruler and has misled this
project before):

    shared record LT 50: childA duplicated=true, childB duplicated=true
    shallow copy: 200 of 202 entries compared, every payload string shared

So the duplication is real -- opening a fork decodes the shared prefix again
and mints strings the parent already holds -- and a shallow copy shares them.
Seeding a child's view from its ancestor's resident rows at open therefore
kills the duplication outright, with no forest in the path.

What forest could still buy this layer, and who already has the job:

| candidate | already done by |
|---|---|
| shared prefix across forks | seeding at open (shallow copy shares strings) |
| a bound on the tail | the window, which survives |
| serving rows neither holds | the pass-through, which scan pollution says to keep |

All three are covered, so **phase 3 collapses from a re-seat into a
seed-at-open**: no mutex introduced, no lock-free path disturbed, the 516ns
benchmark untouched, and nothing added to delete later.

The residue forest would have owned -- rows the ancestor has already trimmed
that a child still needs -- is served by the pass-through, and caching it is
what scan pollution measured as harmful.

This does not speak for phase 4. The composed-UI layer has its own accountant,
its own pins, and S1's law to preserve; whether IT needs forest is a separate
measurement.

## Phase 3 as originally planned (not taken)

### Where the duplication actually is: the FORK BASE, not the window edge

Measured before building (`fork_residency_test.go`). Fork a 300-message aria
near its tip and open the branch:

    fork 6cc7bfa5: base=299, reads 298 rows -- 298 inherited from the parent,
    0 its own

A fresh fork's resident view is ENTIRELY its parent's rows, decoded a second
time. So the sharing the ruling asks for is inside the resident window, not
below it, and the re-seat's seam is at the fork base:

  - rows at or above `base` are the fork's OWN: its lock-free view, untouched
  - rows below `base` are inherited: served through the shared cache, so N
    branches of one trunk pay one residency

The below-window fall-through stays a pass-through, which the scan-pollution
measurement independently argued for. Backing it with the cache would have
bought little and cost the neighbours their tails.

### What the shared path costs, and why a fork must be seeded

`forest.Cache.Range` against `cachedLog`'s lock-free view, both 64 warm units:

    forest Range   parallel 1218 ns/op  4512 B/op   4 allocs
    forest Range   serial    650 ns/op  4512 B/op   4 allocs
    cachedLog      parallel  516 ns/op  4880 B/op   1 alloc
    cachedLog      serial    551 ns/op  4864 B/op   1 alloc

Serially they are close. Under readers they diverge and the sign flips:
forest goes 650 -> 1218 (the mutex), the lock-free view goes 551 -> 516.
2.4x apart at 16 readers, and widening.

That matters because a fresh fork reads 298 inherited rows and 0 of its own,
so under the corrected seam EVERY read of a new branch would take the shared
path. Forking is how this project works; the new-branch case cannot be the
slow one.

So the child's view is SEEDED from the ancestor's resident rows at open. It
starts warm and lock-free, and the cache serves only what neither holds.

The seeding does not reintroduce the duplication, because a shallow copy of
`[]Entry[T]` copies struct headers while the payload strings stay shared. The
memory cost today is that each cachedLog DECODES the prefix separately, minting
separate strings -- not that two slices point at them.

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

## Phase 4 — VERDICT: also a seed, not a re-seat

Same question, same instrument, one layer up. Two branches composing one
history, compared by identity:

    120 strings compared, 40 SHARED, 80 MINTED

The 40 shared are prose, which compose passes through. The 80 minted are the
tool nodes' Input and Output, which compose REBUILDS -- S3's finding exactly
("nodes copy, they do not alias"), and by bytes the minted ones dominate, since
a tool result is hundreds of lines against a one-line prose block.

So the duplication Gluck called generational is REAL: two branches of one trunk
each compose the shared prefix into separate strings.

And it dies the same way it did downstairs:

    shallow copy of 40 turns: 120 node strings compared, every one shared

Seeding a child's turn cache from its ancestor's resident turns shares every
string. So phase 4 is a seed-at-open too, and forest is not needed here either.

The difference from phase 3 is only in what was being duplicated: downstairs a
second DECODE, upstairs a second COMPOSITION. Both are paid once by an
ancestor that already holds the result.

S1's law is untouched by this: the turn cache keeps its accountant, its pins
stay COUNTED, and nothing about eviction changes.

## Phase 4 as originally planned (not taken)

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
