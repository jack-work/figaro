package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The opening-preamble work sits on two paths that run more than once, so both
// are measured here rather than argued about.
//
//   - withSeed is on the PER-FRAME path: buildIndex calls resetToTail on every
//     frame while the pager follows, and resetToTail is where the seed is
//     merged. The default (no seed — every session that opened its own turn,
//     and every `figaro listen`) must cost nothing measurable.
//   - pageCarriesInquiry was on the per-notify-frame path until it was gated;
//     the benchmark is what says how much that gate is worth.

func benchSeedMessages(n int) []aria.Message {
	msgs := make([]aria.Message, 0, n)
	for k := range n {
		msgs = append(msgs, aria.Message{
			Turn: k + 1, Inquiry: fmt.Sprintf("question %d", k), Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "an answer of ordinary length"}},
		})
	}
	return msgs
}

// benchSeedTranscript builds a pager over a client holding `held` committed
// messages, seeded with `seed` catch-up messages.
func benchSeedTranscript(b *testing.B, held, seed int) *transcript {
	b.Helper()
	client := aria.NewClient()
	for _, m := range benchSeedMessages(held) {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{
			Turn: aria.Turn{ID: uint64(m.Turn), Inquiry: m.Inquiry, Sealed: true, Nodes: m.Nodes},
		}}})
	}
	tr := newTranscript(&nullWriter{}, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	if seed > 0 {
		// The seed is older history: turns below everything the client holds.
		msgs := benchSeedMessages(seed)
		for k := range msgs {
			msgs[k].Turn = -seed + k
		}
		client.Merge(msgs, nil)
	}
	tr.enter()
	return tr
}

type nullWriter struct{}

func (nullWriter) Write(p []byte) (int, error) { return len(p), nil }

// BenchmarkTranscriptResetToTail is the window rebuild, with and without a seed
// to merge. "seed=0" is the one that must not regress: it is every session that
// opened its own turn, plus every `figaro listen`.
//
// The rebuild runs when the client's closed revision moves — a newly committed
// message — NOT on every frame; BenchmarkTranscriptResetToTailSteady is what a
// frame actually pays, and it is the early return.
func BenchmarkTranscriptResetToTail(b *testing.B) {
	for _, c := range []struct{ held, seed int }{{40, 0}, {40, 4}, {400, 0}, {400, 4}} {
		b.Run(fmt.Sprintf("held=%d/seed=%d", c.held, c.seed), func(b *testing.B) {
			tr := benchSeedTranscript(b, c.held, c.seed)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr.storeWindow = false // force the rebuild a newly committed message causes
				tr.resetToTail()
			}
		})
	}
}

// BenchmarkTranscriptResetToTailSteady is the same call on the FRAME path: the
// revision has not moved, so the window already is the tail.
func BenchmarkTranscriptResetToTailSteady(b *testing.B) {
	for _, c := range []struct{ held, seed int }{{400, 0}, {400, 4}} {
		b.Run(fmt.Sprintf("held=%d/seed=%d", c.held, c.seed), func(b *testing.B) {
			tr := benchSeedTranscript(b, c.held, c.seed)
			tr.resetToTail()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr.resetToTail()
			}
		})
	}
}

// BenchmarkPageCarriesInquiry is the discriminator's cost per aria frame. It is
// gated to the opening of the session (watchInquiry), so this is what the gate
// saves on every frame after the first — measured rather than assumed.
func BenchmarkPageCarriesInquiry(b *testing.B) {
	prompt := "Run a bash sleep of 6 seconds, then reply with exactly one word: DATE"
	frames := map[string]aria.Page{
		"live-delta": {Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 7, Live: &aria.Live{From: 0, V: 3, Nodes: []aria.NodeDelta{
				{ID: 0, Patch: map[string]livedoc.Delta{"markdown": {}}},
			}},
		}}}},
		"our-inquiry": {Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 7, Inquiry: prompt}}}},
	}
	for name, p := range frames {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				sinkBool = pageCarriesInquiry(p, prompt)
			}
		})
	}
}

var sinkBool bool
