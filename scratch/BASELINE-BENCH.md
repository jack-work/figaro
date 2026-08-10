# Baseline (f85b6ed, 0.22.1) — AMD Ryzen 7 5800X, -benchtime=100ms

chalkboard: Diff/huge/identical 6.3ns  Diff/large/one-key 436µs
            DiffDerived/huge/5-key 841ns  Get/huge 76ns
            MarshalJSON/default 131µs (116 MB/s)  JSONRoundTrip/default 309µs
store:      ChalkboardReduceFold M=30/N=100 43.5µs/record  M=500/N=100 685µs/rec
            ChalkboardOpenReplay M=30/N=2000 5.16µs/rec  M=5000/N=2000 12.6µs/rec
            ChalkboardState10000 123ms / 73.9MB / 970k allocs
            ChalkboardPatches10000 123ms / 74.5MB / 970k allocs
outfit:     FoldCold 627µs/476KB/1579  FoldWarm 76.6µs/23.7KB/175
            FoldWarmEightArias 598µs/198KB/1400

# After (form actor + spec collapse), same box, -benchtime=100ms

form:   Diff/huge/identical 6.4ns  DiffDerived/huge/5-key 856ns  Get/huge 80ns
        MarshalJSON/default 130µs (117 MB/s)  JSONRoundTrip/default 318µs
        — unchanged; the tree did not move.
store:  FormReduceFold M=30/N=100 43.5µs/rec  (was 43.5) — unchanged
        FormOpenReplay M=30/N=2000 5.19µs/rec (was 5.16) — unchanged
        FormState10000    5.3ms /  1.8MB /  36k allocs  (was 123ms / 74MB / 970k)
        FormPatches10000 50.6ms / 17.7MB / 345k allocs  (was 123ms / 74MB / 970k)
        ^ the Form publishes once and Snapshot is an atomic load; the old cache
          re-cloned the board and re-copied every patch on every call.
outfit: FoldCold 643µs/471KB/1568   (was 627µs/477KB/1579)
        FoldWarm 72µs/18KB/164      (was 77µs/24KB/175)
        FoldWarmEightArias 568µs/155KB/1312 (was 598µs/199KB/1400)
        ^ the shared-prefix economics hold: eight arias on one outfit still
          fold once each against a warm cache.
figaro: DressFirstTime 75µs   MaterializeWarm 69µs
        MaterializeNoLayers 11ns  ← every plain `fig set` pays only this
        ParsePatch 2.2µs (client-side, no disk)

# Concurrency, by scenario (form branch, nix build, real provider)

| scenario | result |
|---|---|
| 12 ephemeral arias, one daemon, own devshells | 12/12 answered |
| 12 BACKED arias (birth + dress + turn + store) | 12/12, 41.7s wall, 56 MB daemon RSS, 1.5 MB store |
| distinct ids / mantras / birth-patch hashes | 12 distinct, no shared state |
| `layers` on any board afterwards | none — materialized and stripped |

# Migration, measured on a copy of a real store (463 conversations, 230 MB)

| | |
|---|---|
| boards read after generation 1 -> 2 | 463 / 463 |
| non-empty | 462 (one aria never wrote a patch) |
| `fig ls` renders lineage | yes, including pre-0.22 stumps |
| OUTFIT column, arias older than the loadout->outfit rename | was blank for all of them; now named |

# Everyday operations: main (f85b6ed) vs form, -benchtime=200ms

New benchmarks, because none existed for the verbs a human actually runs.
Same box, back to back. Birth/Fork/Kill measure what `fig new`, `fig fork` and
`fig kill` do to the store; ListWithForms is `fig ls` including the per-row
label read, which is where the cost lives.

| op | main | form | |
|---|---|---|---|
| Birth (mint + spawn + boot patch) | 1.231 ms | 1.217 ms | −1% |
| Fork (branch + dress the alt) | 467 µs | 481 µs | +3% |
| Kill (close, remove, collect) | 2.177 ms | 2.167 ms | ~0 |
| ls, 10 arias | 643 ns | 728 ns | +13% |
| ls, 100 arias | 4.78 µs | 4.74 µs | ~0 |
| Conversations (topology walk) | 700 ns | 734 ns | +5% |
| Vectors, 10k branches | 2.229 ms | 2.220 ms | ~0 |
| AgentInfo, 10k msgs | 19.8 ns | 20.4 ns | ~0 |
| InterruptRepair, 10k msgs | 45.4 ns | 45.1 ns | ~0 |

Run-to-run variance on Birth alone was measured at 6% (1.258 vs 1.334 ms in two
consecutive runs), so everything in the ±5% band here is noise, not signal. The
only real movements are elsewhere: FormState10000 (123 ms -> 5.3 ms) and
FormPatches10000 (123 ms -> 50.6 ms).

# The everyday verbs, properly: 6 runs each, main (f85b6ed) vs form

Single runs could not resolve anything (Birth alone varies 6-9% run to run), so
this is `-count=6` on both trees with the spread reported. sd is the coefficient
of variation, which is what says whether a delta means anything.

| benchmark | main | form | delta | main sd | form sd | |
|---|---|---|---|---|---|---|
| Birth | 1.06 ms | 1.07 ms | +0.4% | 9.5% | 6.6% | noise |
| **Fork** | **429.6 µs** | **456.1 µs** | **+6.2%** | **1.0%** | **1.6%** | **REAL** |
| Kill | 2.19 ms | 2.13 ms | −2.8% | 1.2% | 2.8% | real, faster |
| ls, 10 arias | 0.7 µs | 0.6 µs | −4.7% | 1.1% | 1.2% | real, faster |
| ls, 100 arias | 4.3 µs | 4.3 µs | −0.2% | 3.2% | 1.5% | noise |

## Attribution: the fork regression is ForkWith, and it is the price of the rule

+26 µs, 6.2%, and it is not noise — the spread is 1-2% on both sides.

Before, a plain `fig fork` was `ForkTail` and nothing else; a dress patch was a
separate `ApplyForm` only when `-O` asked for one. Now every fork writes a form
patch AND a renderable birth record AND an `x.SyncCoherent()` before it returns,
because the child's identity is the hash of what it was born carrying and the
patch must be durable before anything can spawn beneath it.

So the 26 µs buys: the fork and its patch in ONE critical section (no window
where a patch is ACKed on the parent and misses the child), an identity for the
branch, and an aria that knows its own id. Cheaper than that is achievable —
batching the fsync, or letting a dressing-free fork skip the record — but both
trade back the property the record exists to provide, and 26 µs on an operation
a human performs by hand is not the place to spend that.

Kill got faster because closing a Form is cheaper than the cache teardown it
replaced; ls/10 because a published snapshot is an atomic load where the old
path re-cloned the board per row.

## Transcript rig (from review, 6 runs each, benchstat)

Every line ~ except TranscriptHeavyEnter at −2.08% (p=0.009) — faster. No
regression from the actor extraction or the form fanout.
