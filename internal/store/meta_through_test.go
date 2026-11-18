package store

import (
	"math/rand"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
)

// HeldThrough must hold what a fold from LT 1 through the cut holds, from a
// sidecar folded to any watermark, ahead of the cut or behind it, on every
// branch: forward from the watermark, forward from LT 1, back from the
// watermark.
func TestHeldThroughAgreesWithAFoldFromTheStart(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		_, _, log := newConversation(t)
		for turn := uint64(1); turn <= uint64(5+rng.Intn(20)); turn++ {
			put(t, log, ask(turn, "question with some words"))
			for r := 0; r < rng.Intn(4); r++ {
				switch rng.Intn(3) {
				case 0:
					put(t, log, answer(turn, "thinking about it"))
				case 1:
					put(t, log, message.Message{Role: message.RoleInput})
				default:
					m := answer(turn, "used")
					m.Usage = &message.Usage{InputTokens: 100 + rng.Intn(900), OutputTokens: rng.Intn(50), CacheReadTokens: rng.Intn(500)}
					put(t, log, m)
				}
			}
			put(t, log, answer(turn, "a"))
		}
		tail, _ := log.PeekTail()
		for trial := 0; trial < 60; trial++ {
			watermark := 1 + uint64(rng.Int63n(int64(tail.LT)))
			cut := uint64(rng.Int63n(int64(tail.LT) + 1))
			base := foldThrough(log, watermark)
			got, ok := HeldThrough(log, base, cut)
			if !ok {
				t.Fatalf("seed %d: refused a sidecar with watermark %d", seed, watermark)
			}
			want := foldThrough(log, cut)
			want.TokensIn, want.TokensOut, want.CacheReadTokens, want.CacheWriteTokens = 0, 0, 0, 0
			if got != want {
				t.Fatalf("seed %d watermark %d cut %d:\n got %+v\nwant %+v", seed, watermark, cut, got, want)
			}
		}
	}
}

func TestHeldThroughRefusesASidecarWithoutAWatermark(t *testing.T) {
	_, _, log := newConversation(t)
	put(t, log, ask(1, "q"))
	if _, ok := HeldThrough(log, AriaMeta{MessageCount: 3}, 1); ok {
		t.Fatal("a pre-watermark sidecar holds absolute counts and cannot be moved")
	}
}

func foldThrough(log arialog.Log[message.Message], cut uint64) AriaMeta {
	var m AriaMeta
	for _, e := range log.Read() {
		if e.LT <= cut {
			m.Fold(e)
		}
	}
	return m
}
