package store_test

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

// The presentation pass runs inside every snapshot rebuild, so its cost is
// paid by `fig ls`, by `fig status`, and by anything that resolves a node.
// Three arms, because the question is not "how fast is a promote" (it is
// two map writes) but "what does carrying a second hierarchy cost the
// listing that never uses it".
//
//	trunkless  the capability off: no edges, no second walk
//	idle       the capability on, nothing promoted
//	promoted   one in eight arias promoted
func benchForest(b *testing.B, n int, trunks bool, promoteEvery int) *store.XwalBackend {
	b.Helper()
	root := b.TempDir()
	back, err := store.NewXwalBackend(root, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { back.Close() })
	if err := wire.Install(back.Store(), root, wire.Capabilities{Trunks: trunks}); err != nil {
		b.Fatal(err)
	}
	outfit, err := back.CreateOutfit("bench", birthPatch(0))
	if err != nil {
		b.Fatal(err)
	}
	// A forest of eight-deep chains: the shape a promote actually lives in.
	var chain string
	for i := 0; i < n; i++ {
		var id string
		if i%8 == 0 || chain == "" {
			id, err = back.CreateConversation(outfit)
		} else {
			_, id, err = back.Fork(chain)
		}
		if err != nil {
			b.Fatal(err)
		}
		chain = id
		if promoteEvery > 0 && i%promoteEvery == 0 && i%8 > 1 {
			if _, err := back.Promote(id, 1); err != nil {
				b.Fatal(err)
			}
		}
	}
	return back
}

func BenchmarkListSnapshot(b *testing.B) {
	arms := []struct {
		name         string
		trunks       bool
		promoteEvery int
	}{
		{"trunkless", false, 0},
		{"idle", true, 0},
		{"promoted", true, 8},
	}
	for _, n := range []int{64, 512} {
		for _, arm := range arms {
			b.Run(fmt.Sprintf("arias=%d/%s", n, arm.name), func(b *testing.B) {
				back := benchForest(b, n, arm.trunks, arm.promoteEvery)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if rows := back.Conversations(); len(rows) != n {
						b.Fatalf("rows = %d, want %d", len(rows), n)
					}
				}
			})
		}
	}
}

// A promote invalidates the snapshot, so this is the cold path: the whole
// forest walk plus the presentation pass, once per promote.
func BenchmarkPromoteThenList(b *testing.B) {
	back := benchForest(b, 512, true, 0)
	ids := back.ConversationIDs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := ids[i%len(ids)]
		if _, err := back.Promote(id, 1); err != nil {
			continue // already at its outfit; the listing below is the point
		}
		back.Conversations()
	}
}
