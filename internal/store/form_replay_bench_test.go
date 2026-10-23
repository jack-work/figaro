package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// Form replay benchmarks: the figwal reducer fold (O(bound form) per record, so
// O(N*M) over a replay) and the end-to-end cold open.
//
//	go test ./internal/store -run XXX -bench 'Form' -benchmem -benchtime=1x -count=5

var replayGrid = []struct{ m, n int }{
	{30, 100}, {30, 2000},
	{500, 100}, {500, 2000},
	{5000, 100}, {5000, 2000},
}

// replayBoundFormJSON: m keys of ~64-byte values, the size a real key runs.
func replayBoundFormJSON(m int) []byte {
	bound := make(map[string]json.RawMessage, m)
	for k, v := range replayBoundFormMap(m) {
		bound[k] = v
	}
	data, err := json.Marshal(bound)
	if err != nil {
		panic(err)
	}
	return data
}

func replayBoundFormMap(m int) map[string]json.RawMessage {
	rng := rand.New(rand.NewSource(7))
	out := make(map[string]json.RawMessage, m)
	for i := range m {
		out[replayKey(i)] = replayValue(rng, 64)
	}
	return out
}

func replayKey(i int) string { return fmt.Sprintf("ns%02d.key%06d", i%20, i) }

func replayValue(rng *rand.Rand, size int) json.RawMessage {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789 "
	var sb strings.Builder
	sb.Grow(size)
	for sb.Len() < size {
		sb.WriteByte(alpha[rng.Intn(len(alpha))])
	}
	b, err := json.Marshal(sb.String())
	if err != nil {
		panic(err)
	}
	return b
}

// replayPatches: n single-key patches cycling over the bound form's own keys.
func replayPatches(m, n int) []message.Patch {
	rng := rand.New(rand.NewSource(11))
	out := make([]message.Patch, 0, n)
	for i := range n {
		out = append(out, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			replayKey(i % max(m, 1)): replayValue(rng, 64),
		}, nil))
	}
	return out
}

// BenchmarkFormReduceFold folds n patch records onto an m-key bound form through
// the real reducer; ns/record makes the scaling in m readable directly.
func BenchmarkFormReduceFold(b *testing.B) {
	for _, g := range replayGrid {
		b.Run(fmt.Sprintf("M=%d/N=%d", g.m, g.n), func(b *testing.B) {
			initial := replayBoundFormJSON(g.m)
			patches := replayPatches(g.m, g.n)
			raw := make([][]byte, len(patches))
			for i, p := range patches {
				enc, err := json.Marshal(p)
				if err != nil {
					b.Fatal(err)
				}
				raw[i] = enc
			}
			b.ResetTimer()
			for b.Loop() {
				state := initial
				for _, p := range raw {
					next, err := formReduce(state, p)
					if err != nil {
						b.Fatal(err)
					}
					state = next
				}
				storeSink += len(state)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*g.n), "ns/record")
		})
	}
}

// BenchmarkFormOpenReplay is the cold-open cost of materializing an aria's form.
func BenchmarkFormOpenReplay(b *testing.B) {
	for _, g := range replayGrid {
		b.Run(fmt.Sprintf("M=%d/N=%d", g.m, g.n), func(b *testing.B) {
			root, aria := seedFormAria(b, g.m, g.n)
			b.ResetTimer()
			for b.Loop() {
				be, err := NewXwalBackend(root, 0)
				if err != nil {
					b.Fatal(err)
				}
				snap, err := be.FormState(aria)
				if err != nil {
					b.Fatal(err)
				}
				storeSink += replaySnapLen(snap)
				if err := be.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*g.n), "ns/record")
		})
	}
}

// seedFormAria writes a store with one conversation whose
// form channel holds an m-key seed patch followed by n
// single-key patches. Returns the store root and the aria id.
func seedFormAria(tb testing.TB, m, n int) (string, string) {
	tb.Helper()
	root := tb.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		tb.Fatal(err)
	}
	outfit, err := be.CreateOutfit("bench", form.Build(form.Snapshot{}, replayBoundFormMap(m), nil))
	if err != nil {
		tb.Fatal(err)
	}
	aria, err := be.CreateConversation(outfit)
	if err != nil {
		tb.Fatal(err)
	}
	for _, p := range replayPatches(m, n) {
		if _, err := be.ApplyForm(aria, p); err != nil {
			tb.Fatal(err)
		}
	}
	if err := be.Close(); err != nil {
		tb.Fatal(err)
	}
	return root, aria
}

// replaySnapLen is the seam for reading a Snapshot's size.
func replaySnapLen(s form.Snapshot) int { return s.Len() }

var storeSink int
