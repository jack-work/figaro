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
