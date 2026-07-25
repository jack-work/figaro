package chalkboard_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// Chalkboard microbenchmarks — the BEFORE measurement for the
// persistent-tree migration (see DESIGN.md, branch chalk/bench).
//
// Everything here uses only the public chalkboard API, and every
// Snapshot construction/read goes through the seam in
// bench_seam_test.go. Fixtures live in bench_fixtures_test.go.
//
// Standard invocation (see scripts/chalkbench.sh and BASELINE.md):
//
//	go test ./internal/chalkboard -bench=. -benchmem -count=10 |
//	    tee bench-before.txt

// --- Clone ---------------------------------------------------------

func BenchmarkClone(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			s := f.board()
			b.ResetTimer()
			for b.Loop() {
				sink(boardLen(s.Clone()))
			}
		})
	}
}

// --- Apply ---------------------------------------------------------

// A small patch onto a big board: two sets and one removal, which is
// what `figaro set` and a turn's context update actually look like.
func BenchmarkApply_SmallPatch(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			s := f.board()
			keys := f.sampleKeys(3)
			patch := chalkboard.Patch{
				Set: map[string]json.RawMessage{
					keys[0]:       json.RawMessage(`"patched"`),
					"bench.fresh": json.RawMessage(`"newly-set"`),
				},
				Remove: []string{keys[len(keys)-1]},
			}
			b.ResetTimer()
			for b.Loop() {
				sink(boardLen(s.Apply(patch)))
			}
		})
	}
}

// --- Diff ----------------------------------------------------------

func BenchmarkDiff(b *testing.B) {
	cases := []struct {
		name    string
		changed int // -1 = every key
	}{
		{"identical", 0},
		{"one-key", 1},
		{"all-different", -1},
	}
	for _, f := range fixtures() {
		for _, c := range cases {
			b.Run(f.name+"/"+c.name, func(b *testing.B) {
				prev := f.board()
				next := prev
				if c.changed != 0 {
					next = buildBoard(f.mutated(c.changed))
				}
				b.ResetTimer()
				for b.Loop() {
					p := next.Diff(prev)
					sink(len(p.Set) + len(p.Remove))
				}
			})
		}
	}
}

// --- Reads ---------------------------------------------------------

func BenchmarkGet(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			s := f.board()
			keys := f.sampleKeys(32)
			i := 0
			b.ResetTimer()
			for b.Loop() {
				v, ok := boardGet(s, keys[i%len(keys)])
				if !ok {
					b.Fatalf("missing key %q", keys[i%len(keys)])
				}
				sink(len(v))
				i++
			}
		})
	}
}

// Lookup is the string-decoding read the agent uses for system.model,
// system.cwd and friends; it unmarshals on every call.
func BenchmarkLookup(b *testing.B) {
	s := buildBoard(defaultBoardMap())
	if s.Lookup("system.model") == nil {
		b.Fatal("fixture lacks system.model")
	}
	b.ResetTimer()
	for b.Loop() {
		p := s.Lookup("system.model")
		if p == nil {
			b.Fatal("nil lookup")
		}
		sink(len(*p))
	}
}

// --- Render --------------------------------------------------------

func BenchmarkRender(b *testing.B) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	if err != nil {
		b.Fatal(err)
	}
	for _, f := range []fixture{{"default", defaultBoardMap}, {"large", largeBoardMap}} {
		b.Run(f.name, func(b *testing.B) {
			prev := f.board()
			keys := f.sampleKeys(5)
			set := make(map[string]json.RawMessage, len(keys))
			for _, k := range keys {
				v, _ := boardGet(prev, k)
				set[k] = mutateValue(v)
			}
			patch := chalkboard.Patch{Set: set}
			b.ResetTimer()
			for b.Loop() {
				out, err := chalkboard.Render(patch, prev, tmpls)
				if err != nil {
					b.Fatal(err)
				}
				sink(len(out))
			}
		})
	}
}

// --- JSON ----------------------------------------------------------
//
// The wire shape is a flat object. chalkboard.json on disk, the
// ChalkboardResponse RPC and chalkboardReduce all marshal/unmarshal a
// whole Snapshot, so this is a hot path in its own right.

func BenchmarkMarshalJSON(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			s := f.board()
			data, err := json.Marshal(s)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				out, err := json.Marshal(s)
				if err != nil {
					b.Fatal(err)
				}
				sink(len(out))
			}
		})
	}
}

func BenchmarkUnmarshalJSON(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			data, err := json.Marshal(f.board())
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				s, err := unmarshalBoard(data)
				if err != nil {
					b.Fatal(err)
				}
				sink(boardLen(s))
			}
		})
	}
}

// The full round trip is what chalkboardReduce pays per WAL record.
func BenchmarkJSONRoundTrip(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			s := f.board()
			b.ResetTimer()
			for b.Loop() {
				data, err := json.Marshal(s)
				if err != nil {
					b.Fatal(err)
				}
				back, err := unmarshalBoard(data)
				if err != nil {
					b.Fatal(err)
				}
				sink(boardLen(back))
			}
		})
	}
}

// --- Legacy shapes (kept from the original bench file) -------------

func BenchmarkSnapshot_Diff_Small(b *testing.B) {
	for _, c := range []struct{ n, diffs int }{{10, 1}, {50, 1}, {50, 5}} {
		b.Run(fmt.Sprintf("%dkeys_%ddiff", c.n, c.diffs), func(b *testing.B) {
			prevM := make(map[string]json.RawMessage, c.n)
			nextM := make(map[string]json.RawMessage, c.n)
			for i := range c.n {
				key := fmt.Sprintf("k%d", i)
				prevM[key] = json.RawMessage(`"value-` + key + `"`)
				if i < c.diffs {
					nextM[key] = json.RawMessage(`"changed-` + key + `"`)
				} else {
					nextM[key] = prevM[key]
				}
			}
			prev, next := buildBoard(prevM), buildBoard(nextM)
			b.ResetTimer()
			for b.Loop() {
				p := next.Diff(prev)
				sink(len(p.Set))
			}
		})
	}
}

func BenchmarkRender_DefaultTemplates_5entries(b *testing.B) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	if err != nil {
		b.Fatal(err)
	}
	prev := buildBoard(map[string]json.RawMessage{})
	patch := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"cwd":      json.RawMessage(`"/home/figaro"`),
			"root":     json.RawMessage(`"/home/figaro"`),
			"datetime": json.RawMessage(`"Wednesday, April 29, 2026, 10AM EDT"`),
			"model":    json.RawMessage(`"claude-opus-4-6"`),
			"label":    json.RawMessage(`"morning"`),
		},
	}
	b.ResetTimer()
	for b.Loop() {
		out, err := chalkboard.Render(patch, prev, tmpls)
		if err != nil {
			b.Fatal(err)
		}
		sink(len(out))
	}
}

// sink keeps the optimizer honest without allocating.
var sinkValue int

func sink(v int) { sinkValue += v }
