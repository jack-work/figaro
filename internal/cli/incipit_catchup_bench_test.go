package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

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

func benchSeedTranscript(b *testing.B, held, seed int) *transcript {
	b.Helper()
	client := aria.NewClient()
	for _, m := range benchSeedMessages(held) {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{
			Turn: aria.Turn{ID: uint64(m.Turn), Inquiry: m.Inquiry, Sealed: true, Nodes: m.Nodes},
		}}}, aria.Notify)
	}
	tr := newTranscript(&nullWriter{}, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	if seed > 0 {
		var p aria.Page
		for k, m := range benchSeedMessages(seed) {
			p.Parts = append(p.Parts, aria.TurnPart{Turn: aria.Turn{
				ID: uint64(-seed + k), Inquiry: m.Inquiry, Sealed: true, Nodes: m.Nodes,
			}})
		}
		client.Apply(p, aria.Quiet)
	}
	tr.enter()
	return tr
}

type nullWriter struct{}

func (nullWriter) Write(p []byte) (int, error) { return len(p), nil }

// seed=0 is the path every session that opened its own turn pays.
func BenchmarkTranscriptResetToTail(b *testing.B) {
	for _, c := range []struct{ held, seed int }{{40, 0}, {40, 4}, {400, 0}, {400, 4}} {
		b.Run(fmt.Sprintf("held=%d/seed=%d", c.held, c.seed), func(b *testing.B) {
			tr := benchSeedTranscript(b, c.held, c.seed)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				tr.from = aria.Anchor{}
				tr.resetToTail()
			}
		})
	}
}

// The per-frame path: the revision has not moved.
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
