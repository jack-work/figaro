package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// Form replay benchmarks: the BEFORE measurement for the
// persistent-tree migration (branch chalk/bench, see DESIGN.md).
//
// Two paths are measured:
//
//  1. BenchmarkFormReduceFold: the figwal reducer,
//     formReduce (xwal_store.go). figwal folds it over every
//     sealed record on segment open (xwal.reducibleFold), and it
//     unmarshals the WHOLE board, applies one patch, and re-marshals
//     the WHOLE board per record. Cost per record is O(board), so a
//     replay of N records over an M-key board is O(N*M): the
//     quadratic we expect to see, and the reason this file exists.
//
//  2. BenchmarkFormOpenReplay: the end-to-end open path:
//     XwalBackend.FormState on a cold backend, which runs
//     loadFormLocked (a per-record map fold, not the reducer)
//     plus whatever figwal does on open.
//
// This is the one place in the fleet's benchmark work that lives in
// `package store`: formReduce is package-private.
//
// Standard invocation:
//
//	go test ./internal/store -run XXX -bench 'Form' -benchmem \
//	    -benchtime=1x -count=5 | tee bench-store-before.txt
//
// -benchtime=1x because a single fold at M=5000,N=2000 already takes
// seconds; repetition buys noise, not signal.

var replayGrid = []struct{ m, n int }{
	{30, 100}, {30, 2000},
	{500, 100}, {500, 2000},
	{5000, 100}, {5000, 2000},
}

// replayBoardJSON returns the marshalled starting board: m keys of
// modest ~64-byte values, the size a real `figaro set`/outfit key runs.
func replayBoardJSON(m int) []byte {
	board := make(map[string]json.RawMessage, m)
	for k, v := range replayBoardMap(m) {
		board[k] = v
	}
	data, err := json.Marshal(board)
	if err != nil {
		panic(err)
	}
	return data
}

func replayBoardMap(m int) map[string]json.RawMessage {
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

// replayPatches returns n single-key patches, cycling over the board's
// own keys: i.e. what a long aria's form channel actually holds
// (one `figaro set`-shaped record at a time).
func replayPatches(m, n int) []message.Patch {
	rng := rand.New(rand.NewSource(11))
	out := make([]message.Patch, 0, n)
	for i := range n {
		out = append(out, message.Patch{
			Set: map[string]json.RawMessage{
				replayKey(i % max(m, 1)): replayValue(rng, 64),
			},
		})
	}
	return out
}

// BenchmarkFormReduceFold folds n patch records onto an m-key
// board through the real reducer, exactly as figwal does on segment
// open. Reports ns/record so the scaling in m is readable directly.
func BenchmarkFormReduceFold(b *testing.B) {
	for _, g := range replayGrid {
		b.Run(fmt.Sprintf("M=%d/N=%d", g.m, g.n), func(b *testing.B) {
			initial := replayBoardJSON(g.m)
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

// BenchmarkFormOpenReplay measures the cold-open cost of
// materializing an aria's form: a fresh XwalBackend over a store
// seeded with an m-key board plus n single-key patch records.
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
	outfit, err := be.CreateOutfit("bench", message.Patch{Set: replayBoardMap(m)})
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

// replaySnapLen is the seam for reading a Snapshot's size from this
// file; on main a Snapshot was a map, now it is a tree handle.
func replaySnapLen(s form.Snapshot) int { return s.Len() }

var storeSink int
