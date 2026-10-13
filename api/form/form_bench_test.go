package form_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

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

func BenchmarkApply_SmallPatch(b *testing.B) {
	for _, f := range fixtures() {
		b.Run(f.name, func(b *testing.B) {
			s := f.board()
			keys := f.sampleKeys(3)
			patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
				keys[0]:       json.RawMessage(`"patched"`),
				"bench.fresh": json.RawMessage(`"newly-set"`),
			}, []string{keys[len(keys)-1]})
			b.ResetTimer()
			for b.Loop() {
				sink(boardLen(s.Apply(patch)))
			}
		})
	}
}

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
					sink(len(p.Entries()))
				}
			})
		}
	}
}

// BenchmarkDiffDerived diffs a snapshot against the one it was derived from,
// the real case; BenchmarkDiff's unrelated trees are the worst case.
func BenchmarkDiffDerived(b *testing.B) {
	for _, f := range fixtures() {
		for _, n := range []int{1, 5} {
			b.Run(fmt.Sprintf("%s/%d-key", f.name, n), func(b *testing.B) {
				prev := f.board()
				set := map[string]json.RawMessage{}
				for _, k := range f.sampleKeys(n) {
					set[k] = json.RawMessage(`"derived-change"`)
				}
				next := prev.Apply(form.Build(prev, set, nil))
				b.ResetTimer()
				for b.Loop() {
					p := next.Diff(prev)
					sink(len(p.Entries()))
				}
			})
		}
	}
}

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

func BenchmarkRender(b *testing.B) {
	tmpls, err := form.LoadDefaultTemplates()
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
			patch := form.Build(prev, set, nil)
			b.ResetTimer()
			for b.Loop() {
				out, err := form.Render(patch, prev, tmpls)
				if err != nil {
					b.Fatal(err)
				}
				sink(len(out))
			}
		})
	}
}

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
				sink(len(p.Entries()))
			}
		})
	}
}

func BenchmarkRender_DefaultTemplates_5entries(b *testing.B) {
	tmpls, err := form.LoadDefaultTemplates()
	if err != nil {
		b.Fatal(err)
	}
	prev := buildBoard(map[string]json.RawMessage{})
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cwd":      json.RawMessage(`"/home/figaro"`),
		"root":     json.RawMessage(`"/home/figaro"`),
		"datetime": json.RawMessage(`"Wednesday, April 29, 2026, 10AM EDT"`),
		"model":    json.RawMessage(`"claude-opus-4-6"`),
		"label":    json.RawMessage(`"morning"`),
	}, nil)
	b.ResetTimer()
	for b.Loop() {
		out, err := form.Render(patch, prev, tmpls)
		if err != nil {
			b.Fatal(err)
		}
		sink(len(out))
	}
}

var sinkValue int

func sink(v int) { sinkValue += v }
