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
